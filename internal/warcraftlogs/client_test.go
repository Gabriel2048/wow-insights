package warcraftlogs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAPI serves the two endpoints a Client talks to and counts what it was
// asked for. The token endpoint and the API endpoint are unrelated paths on the
// real service, which is why WithBaseURL takes both and why one server has to
// answer both here.
type fakeAPI struct {
	mu          sync.Mutex
	tokens      int    // how many times the OAuth endpoint was hit
	queries     int    // how many times the GraphQL endpoint was hit
	expires     int    // seconds to report for the token; 3600 when zero
	status      int    // status for the GraphQL endpoint; 200 when zero
	statuses    []int  // per-call statuses for the GraphQL endpoint, consumed in order, before status
	body        string // body for the GraphQL endpoint
	retryAfter  string // Retry-After header for the GraphQL endpoint, when set
	tokenStatus int    // status for the OAuth endpoint; 200 when zero
	tokenDelay  time.Duration
	// rotated is the number of tokens issued before the secret was rotated:
	// tokens issued up to then are rejected with 401. Zero means never.
	rotated   int
	userAgent string // the last User-Agent seen
}

// rotate invalidates every token issued so far, as a secret rotation does.
func (f *fakeAPI) rotate() {
	f.mu.Lock()
	f.rotated = f.tokens
	f.mu.Unlock()
}

func (f *fakeAPI) start(t *testing.T) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(f.tokenDelay)
		f.mu.Lock()
		f.tokens++
		n := f.tokens
		f.userAgent = r.Header.Get("User-Agent")
		f.mu.Unlock()
		if f.tokenStatus != 0 && f.tokenStatus != http.StatusOK {
			http.Error(w, "invalid_client", f.tokenStatus)
			return
		}
		expires := f.expires
		if expires == 0 {
			expires = 3600
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"access_token":"tok-` + itoa(n) + `","expires_in":` + itoa(expires) + `}`)); err != nil {
			t.Errorf("write token response: %v", err)
		}
	})
	mux.HandleFunc("POST /api", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.queries++
		f.userAgent = r.Header.Get("User-Agent")
		status := f.status
		if len(f.statuses) > 0 {
			status, f.statuses = f.statuses[0], f.statuses[1:]
		}
		rotated := f.rotated
		f.mu.Unlock()
		// A token issued before the rotation is dead.
		if issued, _ := strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "tok-")); rotated > 0 && issued <= rotated {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.retryAfter != "" {
			w.Header().Set("Retry-After", f.retryAfter)
		}
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
		}
		body := f.body
		if body == "" {
			body = `{"data":{"rateLimitData":{"limitPerHour":3600,"pointsSpentThisHour":1,"pointsResetIn":900}}}`
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write query response: %v", err)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New("id", "secret", WithBaseURL(srv.URL+"/oauth/token", srv.URL+"/api"))
	c.backoffBase = time.Millisecond // retries are tested for count, not for patience
	return c
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// The token is worth caching: every query would otherwise pay for a second
// round trip, and the OAuth endpoint is rate limited alongside everything else.
func TestTokenIsCachedAcrossQueries(t *testing.T) {
	api := &fakeAPI{}
	c := api.start(t)

	for i := range 2 {
		if _, err := c.RateLimit(context.Background()); err != nil {
			t.Fatalf("RateLimit() call %d returned error: %v", i+1, err)
		}
	}
	if api.tokens != 1 {
		t.Errorf("token endpoint hit %d times, want 1 (the token must be reused)", api.tokens)
	}
	if api.queries != 2 {
		t.Errorf("query endpoint hit %d times, want 2", api.queries)
	}
}

// A token that expires mid-request is worse than one fetched slightly early, so
// the client renews expiryLeeway before the stated expiry. Reporting a lifetime
// shorter than that leeway is enough to exercise it — no clock injection needed.
func TestTokenIsRefreshedWhenItExpiresInsideTheLeeway(t *testing.T) {
	api := &fakeAPI{expires: 30} // under the 60s expiryLeeway
	c := api.start(t)

	for i := range 2 {
		if _, err := c.RateLimit(context.Background()); err != nil {
			t.Fatalf("RateLimit() call %d returned error: %v", i+1, err)
		}
	}
	if api.tokens != 2 {
		t.Errorf("token endpoint hit %d times, want 2 (a token expiring inside the leeway must be replaced)", api.tokens)
	}
}

// GraphQL reports failure inside a 200, so a status check alone would treat a
// failed query as a successful one and decode an empty result. Every message
// is worth surfacing — on the error's fields, for the log, and never in
// Error(), which is one err.Error() away from a user's screen.
func TestQueryReportsErrorsInsideA200(t *testing.T) {
	api := &fakeAPI{body: `{"errors":[{"message":"You do not have permission","path":["reportData","report"]},{"message":"and another thing"}]}`}
	c := api.start(t)

	_, err := c.RateLimit(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("RateLimit() returned %v for a response carrying a GraphQL errors array, want an *APIError", err)
	}
	if !errors.Is(err, ErrUpstream) {
		t.Errorf("error does not unwrap to ErrUpstream")
	}
	for _, want := range []string{"You do not have permission", "and another thing"} {
		if !slices.Contains(apiErr.Messages, want) {
			t.Errorf("Messages = %v, want %q among them", apiErr.Messages, want)
		}
		if strings.Contains(err.Error(), want) {
			t.Errorf("Error() = %q carries upstream text; it must stay on the fields", err)
		}
	}
	if apiErr.Paths[0] != "reportData.report" {
		t.Errorf("Paths = %v, want the first error's path", apiErr.Paths)
	}
}

// A non-200 carries the status and the body — the body on a field, capped,
// never in Error().
func TestQueryReportsANon200(t *testing.T) {
	api := &fakeAPI{status: http.StatusServiceUnavailable, body: "upstream is down " + strings.Repeat("x", 1000)}
	c := api.start(t)

	_, err := c.RateLimit(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !errors.Is(err, ErrUpstream) {
		t.Fatalf("error = %v, want an *APIError unwrapping to ErrUpstream", err)
	}
	if apiErr.Status != 503 || !strings.HasPrefix(apiErr.Body, "upstream is down") {
		t.Errorf("Status=%d Body=%q, want 503 and the body", apiErr.Status, apiErr.Body)
	}
	if len(apiErr.Body) > maxBodyKept+3 {
		t.Errorf("Body is %d bytes; it is for a log line and must be capped", len(apiErr.Body))
	}
	if strings.Contains(err.Error(), "upstream is down") {
		t.Errorf("Error() = %q carries the upstream body", err)
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("Error() = %q, want it to carry the status", err)
	}
}

// The points budget spent is a 429, and what the API says about when to try
// again is worth carrying to the page.
func TestQueryReportsRateLimiting(t *testing.T) {
	api := &fakeAPI{status: http.StatusTooManyRequests, body: "slow down", retryAfter: "120"}
	c := api.start(t)

	_, err := c.RateLimit(context.Background())
	var apiErr *APIError
	if !errors.Is(err, ErrRateLimited) || !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
	if apiErr.RetryAfter != 2*time.Minute {
		t.Errorf("RetryAfter = %v, want 2m from the header", apiErr.RetryAfter)
	}

	// The same thing said inside a 200 by GraphQL.
	api = &fakeAPI{body: `{"errors":[{"message":"This user has exceeded the rate limit"}]}`}
	c = api.start(t)
	if _, err := c.RateLimit(context.Background()); !errors.Is(err, ErrRateLimited) {
		t.Errorf("error = %v for a GraphQL rate limit message, want ErrRateLimited", err)
	}
}

// A partial document: data for what worked, errors for what did not. Both
// come back, so a caller that paid for ten fields keeps them when the
// eleventh fails.
func TestQueryDecodesPartialDataBeforeReportingErrors(t *testing.T) {
	api := &fakeAPI{body: `{"data":{"rateLimitData":{"limitPerHour":3600,"pointsSpentThisHour":1,"pointsResetIn":900}},
		"errors":[{"message":"no phase data","path":["reportData","report","phases"]}]}`}
	c := api.start(t)

	limit, err := c.RateLimit(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want an *APIError alongside the data", err)
	}
	if limit.LimitPerHour != 3600 {
		t.Errorf("the data was not decoded: %+v", limit)
	}
	if fields := apiErr.Fields(); len(fields) != 1 || fields[0] != "phases" {
		t.Errorf("Fields() = %v, want [phases]", fields)
	}
}

// Wrong credentials are a deployment fault and must not read as an outage.
func TestRejectedCredentialsAreNotAnUpstreamFailure(t *testing.T) {
	api := &fakeAPI{tokenStatus: http.StatusUnauthorized}
	c := api.start(t)
	if _, err := c.RateLimit(context.Background()); !errors.Is(err, ErrBadCredentials) || errors.Is(err, ErrUpstream) {
		t.Errorf("error = %v, want ErrBadCredentials and not ErrUpstream", err)
	}
}

// A report that does not exist, or is private, comes back as a null report
// inside a successful response. This is the branch that stands in for a
// buildReport that deliberately does not exist.
func TestReportNotFoundIsAnError(t *testing.T) {
	api := &fakeAPI{body: `{"data":{"reportData":{"report":null}}}`}
	c := api.start(t)

	_, err := c.Report(context.Background(), "ExampleReport123")
	if err == nil {
		t.Fatal("Report() returned no error for a null report")
	}
	if !errors.Is(err, ErrReportNotFound) {
		t.Errorf("error = %v, want ErrReportNotFound", err)
	}
}

func TestReportDecodesAFightList(t *testing.T) {
	api := &fakeAPI{body: `{"data":{"reportData":{"report":{
		"code":"ExampleReport123","title":"Fixture raid night",
		"owner":{"name":"Testmage"},"zone":{"name":"The Venomous Abyss"},
		"startTime":1700000000000,"endTime":1700000600000,
		"fights":[
			{"id":12,"name":"The Coiled Altar","kill":true,"difficulty":4,"startTime":1000,"endTime":301000},
			{"id":13,"name":"Trash","startTime":301000,"endTime":320000}
		]}}}}`}
	c := api.start(t)

	report, err := c.Report(context.Background(), "ExampleReport123")
	if err != nil {
		t.Fatalf("Report() returned error: %v", err)
	}
	if report.Title != "Fixture raid night" {
		t.Errorf("Title = %q, want Fixture raid night", report.Title)
	}
	if got := len(report.BossFights()); got != 1 {
		t.Errorf("len(BossFights()) = %d, want 1 (trash carries no difficulty)", got)
	}
	if got := report.Kills(); got != 1 {
		t.Errorf("Kills() = %d, want 1", got)
	}
}

// Without credentials the client must not reach the network at all: a request
// that cannot possibly succeed should not cost a round trip, and the caller
// needs to tell a deployment fault from an upstream one.
func TestQueryWithoutCredentialsNeverReachesTheNetwork(t *testing.T) {
	c := New("", "", WithBaseURL("http://127.0.0.1:0/token", "http://127.0.0.1:0/api"))
	if _, err := c.RateLimit(context.Background()); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("error = %v, want ErrNoCredentials", err)
	}
}

// WithTransport is the seam the recorder and the replay hang on. It swaps
// the transport only: the timeout that bounds a hung upstream stays, and a
// nil cannot strip either.
func TestWithTransportKeepsTheTimeout(t *testing.T) {
	rt := roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("x") })
	c := New("id", "secret", WithTransport(rt))
	if c.http.Timeout != requestTimeout {
		t.Errorf("Timeout = %v after WithTransport, want %v", c.http.Timeout, requestTimeout)
	}
	if _, ok := c.http.Transport.(roundTripperFunc); !ok {
		t.Errorf("Transport = %T, want the one installed", c.http.Transport)
	}
	c = New("id", "secret", WithTransport(nil))
	if c.http.Transport == nil || c.http.Timeout != requestTimeout {
		t.Error("a nil transport was installed")
	}
}

// The timeline document asks for eleven fields. One failing — phases on an
// encounter with no phase metadata — used to discard the other ten and blank
// the whole timeline. Now the ten are built and the eleventh is named.
func TestTimelineBuildsFromAPartialDocument(t *testing.T) {
	api := &fakeAPI{body: `{"data":{"reportData":{"report":{
		"casts":{"data":[{"timestamp":2000,"type":"begincast","sourceID":7,"targetID":-1,"abilityGameID":133},
		                  {"timestamp":4000,"type":"cast","sourceID":7,"targetID":-1,"abilityGameID":133}],"nextPageTimestamp":null},
		"lust":{"data":[]},"procs":{"data":[]},"cooldowns":{"data":[]},"raidCDs":{"data":[]},
		"damage":{"data":{"series":[]}},"taken":{"data":{"series":[]}},"bossCasts":{"data":[]},
		"masterData":{"abilities":[{"gameID":133,"name":"Fireball"}],"actors":[],"npcs":[]},
		"fights":[{"encounterID":3000,"phaseTransitions":[]}],"phases":null}}},
		"errors":[{"message":"no phase data for this encounter","path":["reportData","report","phases"]}]}`}
	c := api.start(t)

	tl, err := c.Timeline(context.Background(), "ExampleReport123", Fight{ID: 12, StartTime: 1000, EndTime: 301000}, 7, fire)
	if err != nil {
		t.Fatalf("Timeline() returned %v for a partial document, want the ten fields that arrived", err)
	}
	if len(tl.Casts) != 1 || tl.Casts[0].Name != "Fireball" {
		t.Errorf("Casts = %+v, want the one Fireball that was in the data", tl.Casts)
	}
	if len(tl.Incomplete) != 1 || tl.Incomplete[0] != "phases" {
		t.Errorf("Incomplete = %v, want [phases]", tl.Incomplete)
	}
	if want := (Subject{ReportCode: "ExampleReport123", FightID: 12, ActorID: 7, Spec: fire.Spec}); tl.Subject != want {
		t.Errorf("Subject = %+v, want %+v", tl.Subject, want)
	}
}

// The master data is the only source of every name and of the boss filter.
// An error on one of its parts is a gap the page can name; an error on the
// master data itself is no timeline at all, not a page of "Spell 133".
func TestAMasterDataGapIsNamedAndAMissingMasterDataIsAnError(t *testing.T) {
	// The same document answers both queries: the master data arrives with
	// its npcs missing, and the timeline arrives whole.
	api := &fakeAPI{body: `{"data":{"reportData":{"report":{
		"casts":{"data":[],"nextPageTimestamp":null},
		"lust":{"data":[]},"procs":{"data":[]},"cooldowns":{"data":[]},"raidCDs":{"data":[]},
		"damage":{"data":{"series":[]}},"taken":{"data":{"series":[]}},"bossCasts":{"data":[]},
		"masterData":{"abilities":[{"gameID":133,"name":"Fireball"}],"actors":[],"npcs":null},
		"fights":[],"phases":[]}}},
		"errors":[{"message":"npcs unavailable","path":["reportData","report","masterData","npcs"]}]}`}
	c := api.start(t)
	tl, err := c.Timeline(context.Background(), "ExampleReport123", Fight{ID: 12, StartTime: 1000, EndTime: 301000}, 7, fire)
	if err != nil {
		t.Fatalf("Timeline() returned %v for a gap in the master data, want the timeline with the gap named", err)
	}
	if len(tl.Incomplete) != 1 || tl.Incomplete[0] != "npcs" {
		t.Errorf("Incomplete = %v, want [npcs] once, though both documents reported it", tl.Incomplete)
	}

	api = &fakeAPI{body: `{"data":{"reportData":{"report":{"masterData":null}}},
		"errors":[{"message":"unavailable","path":["reportData","report","masterData"]}]}`}
	c = api.start(t)
	if _, err := c.Timeline(context.Background(), "ExampleReport123", Fight{ID: 12, StartTime: 1000, EndTime: 301000}, 7, fire); err == nil {
		t.Error("Timeline() built a timeline with no master data at all")
	}
}

// A rate limit reported inside a 200 is not a partial document to build from.
func TestTimelineDoesNotBuildFromARateLimit(t *testing.T) {
	api := &fakeAPI{body: `{"data":{"reportData":{"report":{"casts":{"data":[]}}}},"errors":[{"message":"rate limit exceeded"}]}`}
	c := api.start(t)
	if _, err := c.Timeline(context.Background(), "ExampleReport123", Fight{ID: 12, StartTime: 1000, EndTime: 301000}, 7, fire); !errors.Is(err, ErrRateLimited) {
		t.Errorf("error = %v, want ErrRateLimited", err)
	}
}

// What a real request for a code that does not exist gets: a null report
// beside a GraphQL error on it. That is not found, not an outage — and the
// API's words stay in the chain for the log.
func TestReportNotFoundAsTheAPIActuallySaysIt(t *testing.T) {
	api := &fakeAPI{body: `{"data":{"reportData":{"report":null}},"errors":[{"message":"This report does not exist.","path":["reportData","report"]}]}`}
	c := api.start(t)
	_, err := c.Report(context.Background(), "AbCdEfGh12345678")
	if !errors.Is(err, ErrReportNotFound) {
		t.Fatalf("error = %v, want ErrReportNotFound", err)
	}
	if errors.Is(err, ErrUpstream) {
		t.Error("a missing report unwraps to ErrUpstream too; it must not read as an outage")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Messages[0] != "This report does not exist." {
		t.Errorf("the API's message is not in the chain: %v", err)
	}
	if _, err := c.FightDetail(context.Background(), "AbCdEfGh12345678", 1); !errors.Is(err, ErrReportNotFound) {
		t.Errorf("FightDetail: error = %v, want ErrReportNotFound", err)
	}
	if _, err := c.Timeline(context.Background(), "AbCdEfGh12345678", Fight{ID: 1, EndTime: 1000}, 7, fire); !errors.Is(err, ErrReportNotFound) {
		t.Errorf("Timeline: error = %v, want ErrReportNotFound", err)
	}
}
