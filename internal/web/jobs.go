package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"wowinsight/internal/coach"
	"wowinsight/internal/warcraftlogs"
)

// The registry's dials. None of them is tuned to today's work, which finishes
// in under a second: this machinery exists so that a future analysis may take
// as long as it needs to think, and these are the bounds it will think inside.
const (
	// jobSlots is how many analyses run at once. The work is one Warcraft
	// Logs fetch and some arithmetic, so the limit is politeness to an
	// upstream on a shared hourly budget rather than local CPU.
	jobSlots = 4
	// maxKept is how many finished results are held for the browsers still
	// polling for them. The bound that matters is bytes, not count — see
	// resultBytes below.
	maxKept = 16
	// resultTTL is how long a finished result stays. A logged pull never
	// changes, so this is a memory bound and not a correctness one.
	resultTTL = 30 * time.Minute
	// jobDeadline is the longest a single analysis may run. It is the
	// ceiling this whole file exists to raise: the request deadline is one
	// minute, and a model that has to read a guide and call a tool or two
	// will want more than that.
	jobDeadline = 10 * time.Minute
)

// errJobsBusy is returned when every slot is taken. It is a sentinel because
// classify turns it into a status and a sentence, and because "the server is
// busy" must not be reported as Warcraft Logs failing.
var errJobsBusy = errors.New("web: too many analyses are already running")

// jobKey identifies the work, not the request that asked for it. Subject is
// already comparable and carries {ReportCode, FightID, ActorID, Spec}; its
// own doc comment says it exists because "there was nothing to key a cache
// on". The knowledge digest joins it so that editing a spec's tables cannot
// serve an answer computed from the old ones.
//
// There is deliberately no opaque job id here. An id would have to live in a
// JavaScript variable, and a refresh destroys it — while the issue's own
// acceptance criterion is that a refresh must not recompute. The URL the
// browser is already on *is* the key.
type jobKey struct {
	subject   warcraftlogs.Subject
	knowledge string
}

// actorRef is a job key with the parts a URL already carries, and nothing
// that has to be fetched to know. It exists so that polling costs nothing:
// the full key needs the player's specialisation and the digest of that
// spec's tables, and finding those means fetching the fight — seven points
// of a shared hourly budget, every couple of seconds, for an answer the
// registry already has. One actor in one pull has one spec, so this
// identifies the same work.
type actorRef struct {
	code    string
	fightID int
	actorID int
}

func (k jobKey) ref() actorRef {
	return actorRef{code: k.subject.ReportCode, fightID: k.subject.FightID, actorID: k.subject.ActorID}
}

// result is what an analysis produces. It is stored rather than rendered so
// that a second browser, or the same one after a refresh, gets the answer
// without the work being done again.
type result struct {
	findings coach.Findings
	// checks are the questions the analysis asked, whether or not any of
	// them produced a finding. A page that shows only findings cannot be
	// read when there are none.
	checks  []warcraftlogs.Check
	notices []string
}

// job is one analysis, running or finished. Everything but done is written
// once, before done is closed, and read only after — which is what makes it
// safe to hand the same *job to every browser waiting on it without a lock
// around the fields.
type job struct {
	// id correlates the job's own log lines with the request that started
	// it. It is not a handle: nothing looks a job up by it.
	id      string
	started time.Time
	done    chan struct{}

	result result
	err    error
	ended  time.Time
}

// finished reports whether the work is over, without blocking.
func (j *job) finished() bool {
	select {
	case <-j.done:
		return true
	default:
		return false
	}
}

// registry owns every running analysis and the results they leave behind.
type registry struct {
	// analyse is the work itself, injected so a test can make it block,
	// fail or panic on demand. The consumer declares what it needs.
	analyse func(ctx context.Context, log *slog.Logger, key jobKey) (result, error)

	// root outlives every request. A job given r.Context() would be
	// cancelled the instant the handler returned — net/http cancels an
	// incoming request's context "when the ServeHTTP method returns" — and
	// context.WithoutCancel is not the answer either: it keeps the values
	// and drops the deadline, which would leave stop with no lever and a
	// runaway analysis with no bound. So the root is made here, and stop
	// cancels it.
	root   context.Context
	cancel context.CancelCauseFunc

	slots chan struct{}
	wg    sync.WaitGroup

	mu    sync.Mutex
	jobs  map[jobKey]*job
	byRef map[actorRef]*job // the same jobs, addressable from a URL alone
	order []jobKey          // finished keys, oldest first, for eviction

	closed bool
}

func newRegistry(analyse func(context.Context, *slog.Logger, jobKey) (result, error)) *registry {
	root, cancel := context.WithCancelCause(context.Background())
	return &registry{
		analyse: analyse,
		root:    root,
		cancel:  cancel,
		slots:   make(chan struct{}, jobSlots),
		jobs:    map[jobKey]*job{},
		byRef:   map[actorRef]*job{},
	}
}

// poll answers from what a URL alone can name, touching no upstream. It is
// what makes a browser able to ask every second without spending anything.
func (r *registry) poll(ref actorRef) (*job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
	j, ok := r.byRef[ref]
	return j, ok
}

// lookup returns the job for a key if there is one, running or finished.
func (r *registry) lookup(key jobKey) (*job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
	j, ok := r.jobs[key]
	return j, ok
}

// start begins an analysis, or attaches to the one already running or
// finished for the same key. started is false when an existing job was
// returned, which is what makes two browsers asking at once cost one fetch.
//
// log is the logger of the request that started the work, passed explicitly
// rather than carried on a context: the request's context is about to be
// cancelled, and a *slog.Logger is immutable and safe to share while an
// *http.Request is not. The consequence, worth knowing when reading the log,
// is that a job's lines carry the id of the request that started it and not
// of whichever poll happened to observe the result.
func (r *registry) start(key jobKey, id string, log *slog.Logger) (*job, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()

	if r.closed {
		return nil, false, errJobsBusy
	}
	if j, ok := r.jobs[key]; ok {
		return j, false, nil
	}

	select {
	case r.slots <- struct{}{}:
	default:
		return nil, false, errJobsBusy
	}

	j := &job{id: id, started: time.Now(), done: make(chan struct{})}
	r.jobs[key] = j
	r.byRef[key.ref()] = j
	r.wg.Go(func() { r.run(key, j, log) })
	return j, true, nil
}

// run does the work and finishes the job exactly once.
func (r *registry) run(key jobKey, j *job, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(r.root, jobDeadline)
	defer cancel()

	// The recover and the close must be in one deferred function. sync's own
	// WaitGroup.Go re-panics rather than calling Done, noting that a panic in
	// a spawned goroutine "will be fatal" — so the net is here or nowhere.
	// And a panic that unwound without closing done would leave every browser
	// polling a key that never resolves.
	defer func() {
		if v := recover(); v != nil {
			j.err = fmt.Errorf("web: the analysis panicked: %v", v)
			log.Error("analysis panicked", "job", j.id, "panic", v)
		}
		j.ended = time.Now()
		close(j.done)
		<-r.slots
		r.mu.Lock()
		r.order = append(r.order, key)
		r.evictLocked()
		r.mu.Unlock()
	}()

	j.result, j.err = r.analyse(ctx, log, key)
	if j.err != nil {
		log.Warn("analysis failed", "job", j.id, "err", j.err)
	}
}

// evictLocked drops the oldest finished results once too many are held.
func (r *registry) evictLocked() {
	for len(r.order) > maxKept {
		r.dropLocked(r.order[0])
		// Clearing the slot before reslicing is what actually releases the
		// key: the backing array keeps whatever the header no longer spans.
		r.order[0] = jobKey{}
		r.order = r.order[1:]
	}
}

// sweepLocked drops results nobody is going to ask for again.
func (r *registry) sweepLocked() {
	if len(r.order) == 0 {
		return
	}
	cut := time.Now().Add(-resultTTL)
	kept := r.order[:0]
	for _, key := range r.order {
		j, ok := r.jobs[key]
		if ok && j.finished() && j.ended.Before(cut) {
			r.dropLocked(key)
			continue
		}
		kept = append(kept, key)
	}
	// Everything past the new length is a stale copy holding a result alive.
	clear(r.order[len(kept):])
	r.order = kept
}

func (r *registry) dropLocked(key jobKey) {
	j, ok := r.jobs[key]
	if !ok || !j.finished() {
		return
	}
	delete(r.jobs, key)
	// Only if it is still the same job: a later run for the same actor under
	// different tables has its own key and must keep the reference.
	if r.byRef[key.ref()] == j {
		delete(r.byRef, key.ref())
	}
}

// stop refuses new work and waits for what is running, until ctx runs out.
// It returns nothing: a job with a ten-minute ceiling cannot finish in the
// seconds a shutdown has, so being cut short is the expected outcome and must
// not become a non-zero exit.
func (r *registry) stop(ctx context.Context, log *slog.Logger) {
	r.mu.Lock()
	r.closed = true
	running := 0
	for _, j := range r.jobs {
		if !j.finished() {
			running++
		}
	}
	r.mu.Unlock()

	if running == 0 {
		r.cancel(errors.New("web: shutting down"))
		return
	}
	log.Info("waiting for analyses", "running", running)

	waited := make(chan struct{})
	go func() { r.wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-ctx.Done():
		log.Warn("abandoning analyses still running", "running", running)
	}
	r.cancel(errors.New("web: shutting down"))
}
