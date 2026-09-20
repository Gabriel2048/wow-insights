package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"wowinsight/internal/warcraftlogs"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testKey(actor int) jobKey {
	return jobKey{subject: warcraftlogs.Subject{ReportCode: "ExampleReport123", FightID: 12, ActorID: actor}, knowledge: "v1"}
}

// THE REGRESSION THIS WHOLE FILE EXISTS FOR. net/http cancels an incoming
// request's context "when the ServeHTTP method returns", so a job handed
// r.Context() dies while the response is still on the wire. The job's context
// must come from a root that outlives every request.
func TestAJobOutlivesTheRequestThatStartedIt(t *testing.T) {
	var jobCtx context.Context
	running, release := make(chan struct{}), make(chan struct{})

	srv := newTestServer(t, fakeWCL{
		fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) {
			return fightDetail(), nil
		},
	})
	srv.jobs.analyse = func(ctx context.Context, _ *slog.Logger, _ jobKey) (result, error) {
		jobCtx = ctx
		close(running)
		<-release
		return result{}, nil
	}

	rec := serve(srv, "POST", "/report/ExampleReport123/fight/12/analysis?player=7")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST status = %d, want 303", rec.Code)
	}

	// The request has been answered and its context cancelled. The job's
	// must not have been.
	<-running
	if err := jobCtx.Err(); err != nil {
		t.Errorf("the job's context is cancelled (%v) after the request that started it returned", err)
	}
	close(release)
	srv.jobs.wg.Wait()
}

// Two browsers asking for the same pull at the same moment must cost one
// fetch, not two. This is the shape internal/warcraftlogs/client.go already
// uses to coalesce token refreshes.
func TestTwoRequestsForOnePullRunOneJob(t *testing.T) {
	var runs atomic.Int32
	release := make(chan struct{})
	rg := newRegistry(func(context.Context, *slog.Logger, jobKey) (result, error) {
		runs.Add(1)
		<-release
		return result{}, nil
	})

	var wg sync.WaitGroup
	attached := make([]bool, 8)
	for i := range attached {
		wg.Go(func() {
			_, started, err := rg.start(testKey(7), "req", quietLog())
			if err != nil {
				t.Error(err)
			}
			attached[i] = !started
		})
	}
	wg.Wait()

	starts := 0
	for _, a := range attached {
		if !a {
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("%d of 8 concurrent requests started work, want exactly 1", starts)
	}
	close(release)
	rg.wg.Wait()
	if got := runs.Load(); got != 1 {
		t.Errorf("the analysis ran %d times, want once", got)
	}
}

// A finished result is served, not recomputed. This is the issue's first
// acceptance criterion: a refresh must not do the work again.
func TestAFinishedJobIsNotRecomputed(t *testing.T) {
	var runs atomic.Int32
	rg := newRegistry(func(context.Context, *slog.Logger, jobKey) (result, error) {
		runs.Add(1)
		return result{}, nil
	})
	j, _, _ := rg.start(testKey(7), "req", quietLog())
	<-j.done

	for range 5 {
		if _, started, _ := rg.start(testKey(7), "req", quietLog()); started {
			t.Fatal("a finished job was started again")
		}
	}
	if got := runs.Load(); got != 1 {
		t.Errorf("ran %d times across six requests, want once", got)
	}
}

// sync's own WaitGroup.Go re-panics rather than calling Done, noting that a
// panic in a spawned goroutine "will be fatal" — so the job runner carries
// its own recover. A panic must become one error line and a failed job, not
// a dead process, and above all it must still close done: a job that never
// finishes leaves every browser polling a key that never resolves.
func TestAPanicInAJobFinishesTheJobRatherThanTheProcess(t *testing.T) {
	rg := newRegistry(func(context.Context, *slog.Logger, jobKey) (result, error) {
		panic("the analysis exploded")
	})
	j, _, err := rg.start(testKey(7), "req", quietLog())
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-j.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the job never finished; a panic left done unclosed and every poller stuck")
	}
	if j.err == nil {
		t.Error("the job succeeded despite panicking")
	}
	// And the slot came back, so a panic does not shrink the pool.
	if _, _, err := rg.start(testKey(8), "req", quietLog()); err != nil {
		t.Errorf("the slot was not released after the panic: %v", err)
	}
	rg.wg.Wait()
}

// The pool is bounded, and running out is this server's problem to name.
// Falling through to the upstream's error would blame Warcraft Logs for our
// own queue.
func TestRunningOutOfSlotsIsOurOwnError(t *testing.T) {
	release := make(chan struct{})
	rg := newRegistry(func(context.Context, *slog.Logger, jobKey) (result, error) {
		<-release
		return result{}, nil
	})
	for i := range jobSlots {
		if _, _, err := rg.start(testKey(i), "req", quietLog()); err != nil {
			t.Fatalf("slot %d: %v", i, err)
		}
	}
	_, _, err := rg.start(testKey(99), "req", quietLog())
	if !errors.Is(err, errJobsBusy) {
		t.Fatalf("err = %v, want errJobsBusy", err)
	}
	if p := classify(err); p.status != 503 {
		t.Errorf("a full queue classifies as %d, want 503 — and it must not read as the upstream failing", p.status)
	}
	close(release)
	rg.wg.Wait()
}

// Shutdown waits for what is running, inside the budget it is given, and
// never turns being cut short into a non-zero exit: a job with a ten-minute
// ceiling cannot finish in the seconds a shutdown has.
func TestShutdownWaitsForARunningJobAndGivesUpOnTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		finished := make(chan struct{})
		rg := newRegistry(func(ctx context.Context, _ *slog.Logger, _ jobKey) (result, error) {
			select {
			case <-time.After(time.Hour): // longer than any drain
			case <-ctx.Done():
			}
			close(finished)
			return result{}, nil
		})
		if _, _, err := rg.start(testKey(7), "req", quietLog()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()

		drain, cancel := context.WithTimeout(context.Background(), shutdownBudget)
		defer cancel()
		start := time.Now()
		rg.stop(drain, quietLog())

		if waited := time.Since(start); waited < shutdownBudget {
			t.Errorf("stop returned after %v, want it to wait the whole %v for a running job", waited, shutdownBudget)
		}
		// Cancelling the root is what lets the job notice and unwind.
		select {
		case <-finished:
		case <-time.After(time.Minute):
			t.Error("the job was never told to stop; the root context was not cancelled")
		}
	})
}

// A shutdown with nothing running does not sit through the budget.
func TestShutdownWithNothingRunningIsImmediate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rg := newRegistry(func(context.Context, *slog.Logger, jobKey) (result, error) {
			return result{}, nil
		})
		drain, cancel := context.WithTimeout(context.Background(), shutdownBudget)
		defer cancel()
		start := time.Now()
		rg.stop(drain, quietLog())
		if waited := time.Since(start); waited != 0 {
			t.Errorf("stop took %v with nothing running, want none of the budget", waited)
		}
	})
}

// Finished results are bounded, and eviction must release what it drops:
// a slice reslice alone leaves the result reachable from the backing array,
// so a store bounded by count would not be bounded by bytes.
func TestFinishedResultsAreBoundedAndReleased(t *testing.T) {
	rg := newRegistry(func(context.Context, *slog.Logger, jobKey) (result, error) {
		return result{}, nil
	})
	for i := range maxKept * 2 {
		j, _, err := rg.start(testKey(i), "req", quietLog())
		if err != nil {
			t.Fatal(err)
		}
		<-j.done
	}
	rg.wg.Wait()

	rg.mu.Lock()
	defer rg.mu.Unlock()
	if len(rg.jobs) > maxKept {
		t.Errorf("holding %d finished jobs, want at most %d", len(rg.jobs), maxKept)
	}
	if len(rg.order) > maxKept {
		t.Errorf("the eviction list holds %d keys, want at most %d", len(rg.order), maxKept)
	}
	for i, key := range rg.order[len(rg.order):cap(rg.order)] {
		if key != (jobKey{}) {
			t.Errorf("evicted slot %d still names %v; the backing array is holding the result alive", i, key)
		}
	}
}

// Polling must cost nothing. The full job key needs the player's spec and
// the digest of that spec's tables, and finding those means fetching the
// fight — seven points of a shared hourly budget. A browser asking every
// second for a minute would spend a sixth of the hour on questions the
// registry could already answer.
func TestPollingSpendsNothing(t *testing.T) {
	fights := 0
	srv := newTestServer(t, fakeWCL{
		fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) {
			fights++
			return fightDetail(), nil
		},
	})
	const target = "/report/ExampleReport123/fight/12/analysis?player=7"

	serve(srv, "POST", target)
	srv.jobs.wg.Wait()
	fights = 0

	for range 30 {
		req := httptest.NewRequest("GET", target, nil)
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("poll status = %d, want 200", rec.Code)
		}
	}
	if fights != 0 {
		t.Errorf("30 polls cost %d fight fetches, want none: a poll must answer from the registry alone", fights)
	}
}

// A poll for a pull nobody has asked about is idle, not an error, so a page
// opened fresh can poll safely.
func TestPollingSomethingUnstartedIsIdle(t *testing.T) {
	srv := newTestServer(t, fakeWCL{})
	req := httptest.NewRequest("GET", "/report/ExampleReport123/fight/12/analysis?player=7", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"state"`) || strings.Contains(body, "running") {
		t.Errorf("body = %s, want an idle state", body)
	}
}
