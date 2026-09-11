// Package warcraftlogs is a client for the Warcraft Logs v2 GraphQL API.
//
// It authenticates with the OAuth 2.0 client credentials flow, which grants
// access to the public API at /api/v2/client. Private reports would require
// the authorization code flow against /api/v2/user instead.
package warcraftlogs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultTokenURL = "https://www.warcraftlogs.com/oauth/token"
	defaultAPIURL   = "https://www.warcraftlogs.com/api/v2/client"

	// Tokens are renewed this long before they actually expire, so a request
	// never leaves with a token that dies in flight.
	expiryLeeway = time.Minute
)

// Client is a Warcraft Logs API client. It is safe for concurrent use; the
// access token is cached and renewed on demand.
type Client struct {
	id     string
	secret string
	http   *http.Client

	// The two endpoints are separate because the real service puts them on
	// unrelated paths: the OAuth endpoint is not under /api. A test server has
	// to answer both, so WithBaseURL takes both.
	tokenURL string
	apiURL   string

	// maxResponse bounds an API response body. A body over it is an error
	// naming that cause, not a truncated JSON document failing to decode.
	maxResponse int64
	// backoffBase is the first retry's ceiling; a field so a test does not
	// have to wait for it.
	backoffBase time.Duration

	// The token cache. mu guards the fields below and is held only to read
	// or replace them — never across the network. refreshing is non-nil
	// while one caller is fetching a token on everyone's behalf, and is
	// closed when that fetch is done, so the others wait on it rather than
	// each making the same request. lastErr remembers a failed refresh for a
	// moment so that twenty waiters do not turn one failure into twenty.
	mu         sync.Mutex
	token      string
	expiry     time.Time
	refreshing chan struct{}
	lastErr    error
	lastErrAt  time.Time
}

// The retry policy for the wire — a 429, a 5xx, or a transport error. Never
// a 4xx and never a GraphQL-level error: those are the document's, and asking
// again gets the same answer. Three attempts with doubling backoff and full
// jitter is about 3.5s in the worst case, well inside a request's deadline.
const (
	maxAttempts    = 3
	backoffBase    = 500 * time.Millisecond
	maxResponseAPI = 32 << 20
	maxResponseTok = 1 << 20

	// A failed token refresh is remembered this long, so waiters that were
	// queued behind it get the error rather than each retrying at once.
	refreshErrTTL = time.Second
)

// userAgent says who is calling, so Warcraft Logs can find the project before
// blocking it. The default Go value says nothing.
const userAgent = "wowinsight (+https://github.com/Gabriel2048/wow-insights)"

// ErrResponseTooLarge is returned when the API's answer exceeds the client's
// cap. It is wrapped with ErrUpstream: too much is still not the data asked for.
var ErrResponseTooLarge = errors.New("warcraftlogs: response too large")

// Option configures a Client. Go has neither constructor overloads nor optional
// parameters, so this is how New takes anything beyond the credentials.
type Option func(*Client)

// WithHTTPClient replaces the HTTP client used for both the token request and
// the API request. A nil client is ignored rather than installed, so a caller
// cannot accidentally strip the default timeout.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// WithBaseURL points the client at different token and API endpoints. Both are
// required: they are unrelated paths on the real service, so a test server has
// to serve both.
func WithBaseURL(tokenURL, apiURL string) Option {
	return func(c *Client) {
		c.tokenURL = tokenURL
		c.apiURL = apiURL
	}
}

// New returns a Client authenticating with the given OAuth credentials.
func New(id, secret string, opts ...Option) *Client {
	c := &Client{
		id:          id,
		secret:      secret,
		http:        &http.Client{Timeout: 30 * time.Second, Transport: newTransport()},
		tokenURL:    defaultTokenURL,
		apiURL:      defaultAPIURL,
		maxResponse: maxResponseAPI,
		backoffBase: backoffBase,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// newTransport is the default transport with the two limits that matter for
// a process talking to one host: DefaultTransport keeps two idle connections
// per host, which is the binding constraint on reuse here, and no cap on
// outbound concurrency at all.
func newTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 16
	t.MaxConnsPerHost = 32
	return t
}

// accessToken returns a cached token, fetching a new one if none is held or
// the current one is close to expiring. Concurrent callers on a cold start
// share one fetch: the first claims it, the rest wait on the channel it
// closes when done, then re-check. The fetch runs detached from any one
// caller's context, so a client that disconnects mid-refresh cannot fail it
// for everyone behind it.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	if c.id == "" || c.secret == "" {
		return "", ErrNoCredentials
	}
	for {
		c.mu.Lock()
		if c.token != "" && time.Now().Before(c.expiry.Add(-expiryLeeway)) {
			token := c.token
			c.mu.Unlock()
			return token, nil
		}
		if c.lastErr != nil && time.Since(c.lastErrAt) < refreshErrTTL {
			err := c.lastErr
			c.mu.Unlock()
			return "", err
		}
		if wait := c.refreshing; wait != nil {
			c.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		done := make(chan struct{})
		c.refreshing = done
		c.mu.Unlock()

		token, expiry, err := c.fetchToken(context.WithoutCancel(ctx))

		c.mu.Lock()
		if err == nil {
			c.token, c.expiry, c.lastErr = token, expiry, nil
		} else {
			c.lastErr, c.lastErrAt = err, time.Now()
		}
		c.refreshing = nil
		close(done)
		c.mu.Unlock()
		if err != nil {
			return "", err
		}
		return token, nil
	}
}

// invalidate drops the cached token if it is still the one the caller was
// using. A token the API has rejected is not worth keeping; a newer one that
// another caller fetched in the meantime is.
func (c *Client) invalidate(token string) {
	c.mu.Lock()
	if c.token == token {
		c.token, c.expiry = "", time.Time{}
	}
	c.mu.Unlock()
}

// fetchToken does the OAuth client-credentials exchange.
func (c *Client) fetchToken(ctx context.Context) (string, time.Time, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.SetBasicAuth(c.id, c.secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: token request: %w", ErrUpstream, err)
	}
	defer resp.Body.Close()

	body, err := readBody(resp.Body, maxResponseTok)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: reading token response: %w", ErrUpstream, err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		// The OAuth endpoint answers a wrong client id or secret with one of
		// these; it is a deployment fault, not an outage.
		return "", time.Time{}, fmt.Errorf("%w: token endpoint returned HTTP %d", ErrBadCredentials, resp.StatusCode)
	default:
		return "", time.Time{}, &APIError{Status: resp.StatusCode, Body: keepBody(body), RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}
	}

	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &token); err != nil {
		return "", time.Time{}, fmt.Errorf("%w: decode token: %w", ErrUpstream, err)
	}
	if token.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("%w: token response carried no access token", ErrUpstream)
	}
	return token.AccessToken, time.Now().Add(time.Duration(token.ExpiresIn) * time.Second), nil
}

// readBody reads at most limit bytes and says so when there were more. An
// io.LimitReader alone signals truncation as EOF, and a truncated JSON
// document then fails to decode with a message naming the wrong cause.
func readBody(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w: over %d bytes", ErrResponseTooLarge, limit)
	}
	return body, nil
}

// backoff is how long to wait before retrying after attempt n (0-based): the
// base doubled per attempt, with full jitter, so retries from many callers do
// not land together.
func (c *Client) backoff(attempt int) time.Duration {
	ceiling := c.backoffBase << attempt
	if ceiling <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(ceiling)))
}

// retryable reports whether asking the wire again could get a different
// answer: a 429, a 5xx, or a failure the network reported. An error the
// transport itself produced — the replay saying it has no such recording —
// is not the network's and will not change.
func retryable(status int, err error) bool {
	if err == nil {
		return status == http.StatusTooManyRequests || status >= 500
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// http.Client wraps whatever the transport returned in a *url.Error,
	// which is itself a net.Error — so the question is asked of what is
	// inside it.
	inner := err
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		inner = urlErr.Err
	}
	var netErr net.Error
	return errors.As(inner, &netErr)
}

// graphQLError is a single error entry returned by the API. Path names the
// field the error sits on, so a caller can say which part of a document was
// lost.
type graphQLError struct {
	Message string `json:"message"`
	Path    []any  `json:"path"`
}

func (e graphQLError) path() string {
	parts := make([]string, 0, len(e.Path))
	for _, p := range e.Path {
		parts = append(parts, fmt.Sprint(p))
	}
	return strings.Join(parts, ".")
}

// Query runs a GraphQL query and unmarshals the "data" object into out.
//
// GraphQL permits a 200 carrying both data and errors: one failing field in a
// document of eleven leaves the other ten intact. So the data is decoded into
// out before the errors are reported, and the error returned is an *APIError
// naming the fields that failed — a caller that can use a partial document
// checks for it with errors.As and carries on with what it has.
func (c *Client) Query(ctx context.Context, query string, variables map[string]any, out any) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}

	retriedAuth := false
	for attempt := 0; ; attempt++ {
		token, err := c.accessToken(ctx)
		if err != nil {
			return err
		}
		status, body, header, err := c.post(ctx, token, payload)

		// A 401 means the token the API was given is no longer good — the
		// secret was rotated, or the token was revoked. Drop it and go once
		// more with a fresh one; a second 401 is a real credentials problem.
		if status == http.StatusUnauthorized && !retriedAuth {
			retriedAuth = true
			c.invalidate(token)
			continue
		}
		if retryable(status, err) && attempt+1 < maxAttempts {
			select {
			case <-time.After(c.backoff(attempt)):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return &APIError{Status: status, Body: keepBody(body), RetryAfter: retryAfter(header.Get("Retry-After"))}
		}
		return decodeResponse(body, out)
	}
}

// post sends one request and reads its whole body. A transport failure and a
// body over the cap come back as errors; any status comes back as a status.
func (c *Client) post(ctx context.Context, token string, payload []byte) (int, []byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: query: %w", ErrUpstream, err)
	}
	defer resp.Body.Close()

	body, err := readBody(resp.Body, c.maxResponse)
	if err != nil {
		return resp.StatusCode, nil, resp.Header, fmt.Errorf("%w: reading response: %w", ErrUpstream, err)
	}
	return resp.StatusCode, body, resp.Header, nil
}

// decodeResponse unpacks a 200: the data into out, and the GraphQL errors, if
// any, into an *APIError returned alongside.
func decodeResponse(body []byte, out any) error {
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []graphQLError  `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("%w: decode response: %w", ErrUpstream, err)
	}
	if out != nil && len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("%w: decode data: %w", ErrUpstream, err)
		}
	}
	if len(envelope.Errors) > 0 {
		apiErr := &APIError{Status: http.StatusOK}
		for _, e := range envelope.Errors {
			apiErr.Messages = append(apiErr.Messages, e.Message)
			apiErr.Paths = append(apiErr.Paths, e.path())
		}
		return apiErr
	}
	return nil
}

// rateLimitResponse is the envelope the rate limit query returns.
type rateLimitResponse struct {
	RateLimitData RateLimit `json:"rateLimitData"`
}

// RateLimit describes the API points budget for the current hour.
type RateLimit struct {
	LimitPerHour        int     `json:"limitPerHour"`
	PointsSpentThisHour float64 `json:"pointsSpentThisHour"`
	PointsResetIn       int     `json:"pointsResetIn"`
}

// RateLimit fetches the current points budget. It is the cheapest query the API
// offers, which makes it a good check that credentials work.
func (c *Client) RateLimit(ctx context.Context) (RateLimit, error) {
	var data rateLimitResponse
	const query = `query { rateLimitData { limitPerHour pointsSpentThisHour pointsResetIn } }`
	err := c.Query(ctx, query, nil, &data)
	return data.RateLimitData, err
}
