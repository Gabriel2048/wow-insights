package warcraftlogs

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// The tests in this file pin how the wire behaves under a blip, a rotation, a
// big answer and load — #41.

// A 429 or a 5xx is asked again, with backoff, and a blip is absorbed.
func TestRetriesA429ThenSucceeds(t *testing.T) {
	api := &fakeAPI{statuses: []int{http.StatusTooManyRequests, http.StatusOK}}
	c := api.start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.RateLimit(ctx); err != nil {
		t.Fatalf("RateLimit() returned %v after a 429 then a 200, want success", err)
	}
	if api.queries != 2 {
		t.Errorf("query endpoint hit %d times, want 2 (one retry)", api.queries)
	}

	api = &fakeAPI{statuses: []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusOK}}
	c = api.start(t)
	if _, err := c.RateLimit(ctx); err != nil || api.queries != 3 {
		t.Errorf("two 5xx then a 200: err=%v queries=%d, want success on the third", err, api.queries)
	}
}

// After the attempts are spent the last answer is the error, with its status.
func TestGivesUpAfterThreeAttempts(t *testing.T) {
	api := &fakeAPI{statuses: []int{503, 503, 503, 200}}
	c := api.start(t)
	_, err := c.RateLimit(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 503 {
		t.Fatalf("error = %v, want the 503 after three attempts", err)
	}
	if api.queries != maxAttempts {
		t.Errorf("query endpoint hit %d times, want %d", api.queries, maxAttempts)
	}
}

// A 4xx is the request's fault and a GraphQL error is the document's; asking
// again gets the same answer, so neither is retried.
func TestDoesNotRetryWhatCannotChange(t *testing.T) {
	api := &fakeAPI{status: http.StatusBadRequest}
	c := api.start(t)
	if _, err := c.RateLimit(context.Background()); err == nil || api.queries != 1 {
		t.Errorf("400: err=%v queries=%d, want one attempt and an error", err, api.queries)
	}

	api = &fakeAPI{body: `{"errors":[{"message":"no such field"}]}`}
	c = api.start(t)
	if _, err := c.RateLimit(context.Background()); err == nil || api.queries != 1 {
		t.Errorf("GraphQL error: err=%v queries=%d, want one attempt and an error", err, api.queries)
	}
}

// The backoff waits on the context, so a caller that is gone stops retrying.
func TestRetryHonoursTheContext(t *testing.T) {
	api := &fakeAPI{status: http.StatusServiceUnavailable}
	c := api.start(t)
	c.backoffBase = time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.RateLimit(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want the context's", err)
	}
	if time.Since(start) > time.Second {
		t.Error("the retry did not stop when the context did")
	}
	if api.queries != 1 {
		t.Errorf("query endpoint hit %d times, want 1 (the backoff was interrupted)", api.queries)
	}
}

// Twenty requests arriving on a cold start share one token fetch. Before,
// each queued behind the mutex across the network call — and a mutex is not
// context-aware, so a caller whose client had left still waited.
func TestTokenRefreshIsCoalesced(t *testing.T) {
	api := &fakeAPI{tokenDelay: 50 * time.Millisecond}
	c := api.start(t)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := c.RateLimit(context.Background()); err != nil {
				t.Errorf("RateLimit() returned %v", err)
			}
		})
	}
	wg.Wait()
	if api.tokens != 1 {
		t.Errorf("token endpoint hit %d times for twenty concurrent first requests, want 1", api.tokens)
	}
	if api.queries != 20 {
		t.Errorf("query endpoint hit %d times, want 20", api.queries)
	}
}

// A waiter whose own request is cancelled stops waiting; the refresh itself
// carries on for the others.
func TestAWaiterCanLeaveWithoutFailingTheRefresh(t *testing.T) {
	api := &fakeAPI{tokenDelay: 100 * time.Millisecond}
	c := api.start(t)
	go func() { _, _ = c.RateLimit(context.Background()) }() // claims the refresh
	time.Sleep(10 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.RateLimit(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a waiter with a dead context got %v, want its context's error", err)
	}
	// The refresh the first caller started still completes and is reused.
	time.Sleep(150 * time.Millisecond)
	if _, err := c.RateLimit(context.Background()); err != nil {
		t.Errorf("after the refresh completed, RateLimit() returned %v", err)
	}
	if api.tokens != 1 {
		t.Errorf("token endpoint hit %d times, want 1", api.tokens)
	}
}

// The acceptance criterion behind the 401 rule: the secret is rotated, the
// cached token dies, and the next query recovers by fetching a new one —
// without a restart. A second 401 is a real credentials problem.
func TestARotatedSecretRecoversWithoutARestart(t *testing.T) {
	api := &fakeAPI{}
	c := api.start(t)
	if _, err := c.RateLimit(context.Background()); err != nil {
		t.Fatal(err)
	}
	api.rotate() // every token issued so far is now dead
	if _, err := c.RateLimit(context.Background()); err != nil {
		t.Fatalf("after a rotation RateLimit() returned %v, want recovery on a fresh token", err)
	}
	if api.tokens != 2 || api.queries != 3 {
		t.Errorf("tokens=%d queries=%d, want 2 tokens and 3 queries (the 401 in the middle)", api.tokens, api.queries)
	}

	// A token endpoint that keeps issuing dead tokens: one retry, then the 401.
	api = &fakeAPI{}
	c = api.start(t)
	api.rotated = 1 << 30
	_, err := c.RateLimit(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized || api.queries != 2 {
		t.Errorf("err=%v queries=%d, want the 401 after exactly one retry", err, api.queries)
	}
}

// A body over the cap is named as such, not reported as a JSON document that
// failed to decode.
func TestAnOversizedResponseIsNamed(t *testing.T) {
	api := &fakeAPI{body: `{"data":{"rateLimitData":{"limitPerHour":3600,"pointsSpentThisHour":1,"pointsResetIn":900}}}` + strings.Repeat(" ", 1000)}
	c := api.start(t)
	c.maxResponse = 200
	_, err := c.RateLimit(context.Background())
	if !errors.Is(err, ErrResponseTooLarge) || !errors.Is(err, ErrUpstream) {
		t.Errorf("error = %v, want ErrResponseTooLarge wrapping ErrUpstream", err)
	}
	if strings.Contains(err.Error(), "unexpected end of JSON") {
		t.Error("the error names a decode failure instead of the real cause")
	}
}

func TestEveryRequestSaysWhoIsCalling(t *testing.T) {
	api := &fakeAPI{}
	c := api.start(t)
	if _, err := c.RateLimit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(api.userAgent, "wowinsight") || !strings.Contains(api.userAgent, "github.com") {
		t.Errorf("User-Agent = %q, want the project and where to find it", api.userAgent)
	}
}

func TestDefaultTransportRaisesTheIdleLimit(t *testing.T) {
	c := New("id", "secret")
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok {
		t.Fatal("the default client has no explicit transport")
	}
	if tr.MaxIdleConnsPerHost <= http.DefaultMaxIdleConnsPerHost || tr.MaxConnsPerHost == 0 {
		t.Errorf("MaxIdleConnsPerHost=%d MaxConnsPerHost=%d; want the idle limit raised and a concurrency cap", tr.MaxIdleConnsPerHost, tr.MaxConnsPerHost)
	}
}

// A failure the network reports is worth a retry; one the transport itself
// produced — the replay saying it has no such recording — is not.
func TestOnlyNetworkFailuresAreRetried(t *testing.T) {
	calls := 0
	c := New("id", "secret", WithHTTPClient(&http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if strings.HasSuffix(r.URL.Path, "/token") {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"t","expires_in":3600}`)), Header: http.Header{}}, nil
		}
		return nil, errors.New("fixture: nothing recorded for this")
	})}))
	c.backoffBase = time.Millisecond
	if _, err := c.RateLimit(context.Background()); err == nil {
		t.Fatal("want the transport's error")
	}
	if calls != 2 { // the token, then the one query
		t.Errorf("transport called %d times, want 2: a transport's own error is not retried", calls)
	}

	// A refused connection is the network's, and is retried.
	api := &fakeAPI{}
	c = api.start(t)
	if _, err := c.RateLimit(context.Background()); err != nil {
		t.Fatal(err)
	}
	var netErr net.Error = &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	if !retryable(0, netErr) {
		t.Error("a net.Error is not retryable")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
