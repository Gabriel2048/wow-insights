package warcraftlogs

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"wowinsight/internal/knowledge"
)

// How long each kind of answer is worth keeping. They differ because the
// things they describe change at different rates, not because the numbers
// were tuned.
const (
	// reportTTL is short because a report grows. A raid night appends pulls
	// to the same report for hours, and the index page listing them is the
	// one place a stale answer is visible as a missing fight.
	reportTTL = time.Minute
	// fightTTL and timelineTTL are longer because a pull that has been
	// logged does not change: the casts happened, and no later upload edits
	// them. They stay minutes rather than hours anyway — see the note on
	// Cache about what this is and is not.
	fightTTL    = 10 * time.Minute
	timelineTTL = 10 * time.Minute
	// missTTL is how long "there is no such report" is remembered. Long
	// enough to absorb a typo being retried, short enough that a report
	// uploaded a moment later is found.
	missTTL = 30 * time.Second

	// maxEntries bounds each store. A Timeline is the large one: casts are
	// capped at 50,000 events, so a pathological pull is a few megabytes and
	// a typical one a few hundred kilobytes.
	maxEntries = 32
)

// source is the slice of the client the cache decorates, declared here by
// the consumer of it rather than beside *Client. It is the same three methods
// internal/web asks for, which is why a *Cache can stand where a *Client does
// without either package knowing about the other's interface.
type source interface {
	Report(ctx context.Context, code string) (*Report, error)
	FightDetail(ctx context.Context, code string, fightID int) (*FightDetail, error)
	Timeline(ctx context.Context, code string, fight Fight, sourceID int, know knowledge.Knowledge) (*Timeline, error)
}

// Cache remembers what the API has already been asked, so that a second look
// at a page costs nothing. Every page view re-queries an hourly budget of
// 3,600 points shared by every user of the deployment, and a fight page is
// about twelve of them — so roughly three hundred page views an hour, for
// everybody, is what the app has without this.
//
// **It caches the analysis, not the answer.** What is stored is the value
// this package built — a *Timeline of paired casts and windows, a
// *FightDetail of merged tables — and never a response body. That matters
// beyond tidiness: the API replies `cache-control: no-cache, private`, and
// the terms this app is used under forbid keeping cached copies of *their
// content* longer than that header allows. A derived analysis is this
// program's own work; a stored response body would not be. The entries are
// in memory, bounded, and die with the process, which keeps the distinction
// honest rather than convenient — and #18 is where a human settles what the
// terms actually permit.
//
// **A document with a hole in it is never cached.** The error taxonomy wrote
// that rule for this decorator by name: a GraphQL response can arrive 200
// with both data and errors, and caching the half that came would serve the
// hole for as long as the entry lived.
type Cache struct {
	src source

	reports   *store[string, *Report]
	fights    *store[fightKey, *FightDetail]
	timelines *store[timelineKey, *Timeline]

	mu   sync.Mutex
	hits int
	miss int
}

type fightKey struct {
	code    string
	fightID int
}

// timelineKey is Subject plus the digest of the tables it was analysed with.
// Subject is comparable and exists for this; Fight is not, because it holds
// *bool and *int and would compare by address.
type timelineKey struct {
	subject   Subject
	knowledge string
}

// NewCache wraps a client. Nothing else about the client changes: the cache
// satisfies the same three methods, so whatever held a *Client can hold this.
func NewCache(src source) *Cache {
	return &Cache{
		src:       src,
		reports:   newStore[string, *Report](),
		fights:    newStore[fightKey, *FightDetail](),
		timelines: newStore[timelineKey, *Timeline](),
	}
}

// Budget forwards where the hourly points budget stands. The access line
// reads it by asking the client whether it can answer — and wrapping the
// client in this would silently end that, taking the one number that says
// how close the deployment is to its ceiling out of every log line. A
// decorator has to carry what it covers.
func (c *Cache) Budget() (RateLimit, bool) {
	b, ok := c.src.(interface{ Budget() (RateLimit, bool) })
	if !ok {
		return RateLimit{}, false
	}
	return b.Budget()
}

// Stats is how the cache has been doing, for the access line. A hit rate is
// the only number that says whether any of this is working, and a log line
// is cheaper than a metrics system this app does not have.
func (c *Cache) Stats() (hits, misses int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.miss
}

func (c *Cache) note(hit bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if hit {
		c.hits++
		return
	}
	c.miss++
}

// Report lists a report's fights, for a minute.
func (c *Cache) Report(ctx context.Context, code string) (*Report, error) {
	v, hit, err := c.reports.do(ctx, code, reportTTL, func(ctx context.Context) (*Report, time.Duration, error) {
		r, err := c.src.Report(ctx, code)
		return r, reportTTL, err
	})
	c.note(hit)
	return v, err
}

// FightDetail is one pull's roster and tables.
func (c *Cache) FightDetail(ctx context.Context, code string, fightID int) (*FightDetail, error) {
	key := fightKey{code: code, fightID: fightID}
	v, hit, err := c.fights.do(ctx, key, fightTTL, func(ctx context.Context) (*FightDetail, time.Duration, error) {
		d, err := c.src.FightDetail(ctx, code, fightID)
		if err == nil && len(d.Incomplete) > 0 {
			// Part of the document did not arrive. Keep the value for this
			// caller — the page still renders and says so — but do not keep
			// it for the next one.
			return d, 0, nil
		}
		return d, fightTTL, err
	})
	c.note(hit)
	return v, err
}

// Timeline is one player's analysed pull, keyed on the tables it was analysed
// with: two runs over one actor under different knowledge are different
// answers, and the digest is what stops one being served for the other.
func (c *Cache) Timeline(ctx context.Context, code string, fight Fight, sourceID int, know knowledge.Knowledge) (*Timeline, error) {
	key := timelineKey{
		subject:   Subject{ReportCode: code, FightID: fight.ID, ActorID: sourceID, Spec: know.Spec},
		knowledge: know.Version(),
	}
	v, hit, err := c.timelines.do(ctx, key, timelineTTL, func(ctx context.Context) (*Timeline, time.Duration, error) {
		t, err := c.src.Timeline(ctx, code, fight, sourceID, know)
		if err == nil && len(t.Incomplete) > 0 {
			// The same rule, and the reason it is written twice: Incomplete
			// means the API reported errors on those fields. Truncated does
			// not — a stream cut at its page cap is complete as far as it
			// goes and would be cut identically next time, so it is kept.
			return t, 0, nil
		}
		return t, timelineTTL, err
	})
	c.note(hit)
	return v, err
}

// cacheableFailure reports whether a failure is worth remembering. A report
// that does not exist will not exist a second later, so the answer is cheap
// to keep. A spent budget or a rate limit is the opposite: remembering it
// would outlive the condition and lock the app out of its own recovery, and
// an outage is transient by definition.
func cacheableFailure(err error) bool {
	return errors.Is(err, ErrReportNotFound) || errors.Is(err, ErrFightNotFound)
}

// entry is one answer, or one fetch in progress. done is closed exactly once,
// when val and err are final; nothing reads them before.
type entry[V any] struct {
	done    chan struct{}
	val     V
	err     error
	expires time.Time
}

// store is a small single-flight cache. Two callers asking for one uncached
// thing make one request and both get its answer — the shape this repository
// already uses for the coalesced token refresh and for the analysis registry.
type store[K comparable, V any] struct {
	mu      sync.Mutex
	entries map[K]*entry[V]
	order   []K
}

func newStore[K comparable, V any]() *store[K, V] {
	return &store[K, V]{entries: map[K]*entry[V]{}}
}

// do returns the cached value, or waits for the fetch already running for
// this key, or starts one. hit reports whether the work was avoided.
func (s *store[K, V]) do(ctx context.Context, key K, _ time.Duration, fetch func(context.Context) (V, time.Duration, error)) (V, bool, error) {
	s.mu.Lock()
	if e, ok := s.entries[key]; ok {
		s.mu.Unlock()
		select {
		case <-e.done:
		case <-ctx.Done():
			var zero V
			return zero, false, ctx.Err()
		}
		// An entry that expired while it was being waited on is still the
		// freshest thing anyone has; the next caller will replace it.
		return e.val, true, e.err
	}
	e := &entry[V]{done: make(chan struct{})}
	s.entries[key] = e
	s.mu.Unlock()

	val, ttl, err := fetch(ctx)
	e.val, e.err = val, err
	e.expires = time.Now().Add(ttl)
	close(e.done)

	// A failure worth remembering gets its own short life; anything else is
	// dropped so the next caller tries again.
	keep := err == nil && ttl > 0
	if err != nil && cacheableFailure(err) {
		keep, e.expires = true, time.Now().Add(missTTL)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !keep {
		delete(s.entries, key)
		return val, false, err
	}
	s.order = append(s.order, key)
	s.evictLocked()
	return val, false, err
}

// evictLocked drops the oldest entries, and expired ones wherever they are.
func (s *store[K, V]) evictLocked() {
	now := time.Now()
	kept := s.order[:0]
	for _, key := range s.order {
		if e, ok := s.entries[key]; ok && now.After(e.expires) {
			delete(s.entries, key)
			continue
		}
		kept = append(kept, key)
	}
	// Past the new length the array still names evicted keys, which would
	// hold their values alive.
	clear(s.order[len(kept):])
	s.order = kept

	for len(s.order) > maxEntries {
		delete(s.entries, s.order[0])
		s.order[0] = *new(K)
		s.order = s.order[1:]
	}
	s.order = slices.Clip(s.order)
}
