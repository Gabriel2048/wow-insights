package web

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"

	"wowinsight/internal/warcraftlogs"
)

// startServer runs serve on a port-0 listener and returns its base URL, a
// cancel for the context it runs under, and the channel serve's result
// arrives on.
func startServer(t *testing.T, wcl logsClient) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- newTestServer(t, wcl).serve(ctx, ln) }()
	return "http://" + ln.Addr().String(), cancel, done
}

// The acceptance criterion behind graceful shutdown: a request in flight when
// the process is told to stop still completes, and only then does the server
// return. The fake's Report blocks until the test releases it, which is what
// keeps the request in flight across the cancellation.
func TestServeLetsAnInFlightRequestFinishAfterCancel(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	wcl := fakeWCL{report: func(context.Context, string) (*warcraftlogs.Report, error) {
		entered <- struct{}{}
		<-release
		return reportWithTwoPulls(), nil
	}}
	base, cancel, done := startServer(t, wcl)

	type result struct {
		resp *http.Response
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.Get(base + "/?url=https://www.warcraftlogs.com/reports/ExampleReport123")
		got <- result{resp, err}
	}()
	<-entered // the handler is now inside the upstream call

	cancel() // the process has been told to stop
	select {
	case err := <-done:
		t.Fatalf("serve returned %v with a request still in flight", err)
	case <-time.After(200 * time.Millisecond):
	}

	// A new connection is refused while draining: the listener is closed.
	if _, err := net.DialTimeout("tcp", base[len("http://"):], 200*time.Millisecond); err == nil {
		t.Error("a new connection was accepted after shutdown began")
	}

	close(release)
	r := <-got
	if r.err != nil {
		t.Fatalf("the in-flight request failed: %v", r.err)
	}
	defer r.resp.Body.Close()
	if r.resp.StatusCode != http.StatusOK {
		t.Errorf("in-flight request status = %d, want %d", r.resp.StatusCode, http.StatusOK)
	}
	if body, _ := io.ReadAll(r.resp.Body); len(body) == 0 {
		t.Error("the in-flight request got an empty body: it was cut off")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve returned %v after a clean drain, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("serve did not return after the last request finished")
	}
}

// A port that cannot be bound is a startup failure, not a hang.
func TestRunFailsWhenThePortIsTaken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	err = newTestServer(t, fakeWCL{}).Run(context.Background(), ln.Addr().String())
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Errorf("Run() on a taken port returned %v, want an address-in-use error", err)
	}
}
