package warcraftlogs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAPI serves the two endpoints a Client talks to and counts what it was
// asked for. The token endpoint and the API endpoint are unrelated paths on the
// real service, which is why WithBaseURL takes both and why one server has to
// answer both here.
type fakeAPI struct {
	tokens  int    // how many times the OAuth endpoint was hit
	queries int    // how many times the GraphQL endpoint was hit
	expires int    // seconds to report for the token; 3600 when zero
	status  int    // status for the GraphQL endpoint; 200 when zero
	body    string // body for the GraphQL endpoint
}

func (f *fakeAPI) start(t *testing.T) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		f.tokens++
		expires := f.expires
		if expires == 0 {
			expires = 3600
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"access_token":"tok","expires_in":` + itoa(expires) + `}`)); err != nil {
			t.Errorf("write token response: %v", err)
		}
	})
	mux.HandleFunc("POST /api", func(w http.ResponseWriter, r *http.Request) {
		f.queries++
		if f.status != 0 && f.status != http.StatusOK {
			w.WriteHeader(f.status)
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
	return New("id", "secret", WithBaseURL(srv.URL+"/oauth/token", srv.URL+"/api"))
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
// failed query as a successful one and decode an empty result.
func TestQueryReportsErrorsInsideA200(t *testing.T) {
	api := &fakeAPI{body: `{"errors":[{"message":"You do not have permission"},{"message":"and another thing"}]}`}
	c := api.start(t)

	_, err := c.RateLimit(context.Background())
	if err == nil {
		t.Fatal("RateLimit() returned no error for a response carrying a GraphQL errors array")
	}
	for _, want := range []string{"You do not have permission", "and another thing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q — every message is worth surfacing", err, want)
		}
	}
}

func TestQueryReportsANon200(t *testing.T) {
	api := &fakeAPI{status: http.StatusServiceUnavailable, body: "upstream is down"}
	c := api.start(t)

	_, err := c.RateLimit(context.Background())
	if err == nil {
		t.Fatal("RateLimit() returned no error for a 503")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error = %q, want it to carry the status", err)
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
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want it to say the report was not found", err)
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

// WithHTTPClient is the seam the offline fixture mode installs a replay
// transport through, so a nil must not be able to strip the default client.
func TestWithHTTPClientIgnoresNil(t *testing.T) {
	c := New("id", "secret", WithHTTPClient(nil))
	if c.http == nil {
		t.Error("a nil http.Client was installed, leaving the client with no transport and no timeout")
	}
}

func TestNewDefaultsToTheRealEndpoints(t *testing.T) {
	c := New("id", "secret")
	if c.tokenURL != defaultTokenURL || c.apiURL != defaultAPIURL {
		t.Errorf("New() gave tokenURL=%q apiURL=%q, want the package defaults", c.tokenURL, c.apiURL)
	}
}
