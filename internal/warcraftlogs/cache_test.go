package warcraftlogs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wowinsight/internal/knowledge"
)

// fakeSource counts what the cache let through to the API.
type fakeSource struct {
	reports, fights, timelines atomic.Int32
	reportErr                  error
	fight                      func() (*FightDetail, error)
	timeline                   func() (*Timeline, error)
	block                      chan struct{}
}

func (f *fakeSource) Report(context.Context, string) (*Report, error) {
	f.reports.Add(1)
	if f.block != nil {
		<-f.block
	}
	if f.reportErr != nil {
		return nil, f.reportErr
	}
	return &Report{Code: "ExampleReport123"}, nil
}

func (f *fakeSource) FightDetail(context.Context, string, int) (*FightDetail, error) {
	f.fights.Add(1)
	if f.fight != nil {
		return f.fight()
	}
	return &FightDetail{ReportCode: "ExampleReport123"}, nil
}

func (f *fakeSource) Timeline(context.Context, string, Fight, int, knowledge.Knowledge) (*Timeline, error) {
	f.timelines.Add(1)
	if f.timeline != nil {
		return f.timeline()
	}
	return &Timeline{Duration: time.Minute}, nil
}

func fireKnowledge(t *testing.T) knowledge.Knowledge {
	t.Helper()
	k, ok := knowledge.Lookup(knowledge.SpecID{Class: "Mage", Spec: "Fire"})
	if !ok {
		t.Fatal("the reference spec is not in the tables")
	}
	return k
}

// A second look at a page costs nothing. A fight page is about twelve points
// of an hourly budget shared by every user of the deployment, so this is the
// difference between roughly three hundred page views an hour and rather
// more of them.
func TestASecondLookCostsNothing(t *testing.T) {
	src := &fakeSource{}
	c := NewCache(src)
	ctx := context.Background()
	fight := Fight{ID: 12}

	for range 5 {
		if _, err := c.Report(ctx, "ExampleReport123"); err != nil {
			t.Fatal(err)
		}
		if _, err := c.FightDetail(ctx, "ExampleReport123", 12); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Timeline(ctx, "ExampleReport123", fight, 7, fireKnowledge(t)); err != nil {
			t.Fatal(err)
		}
	}
	if got := src.reports.Load(); got != 1 {
		t.Errorf("five page views fetched the report %d times, want 1", got)
	}
	if got := src.fights.Load(); got != 1 {
		t.Errorf("five page views fetched the fight %d times, want 1", got)
	}
	if got := src.timelines.Load(); got != 1 {
		t.Errorf("five page views fetched the timeline %d times, want 1", got)
	}
	if hits, misses := c.Stats(); hits != 12 || misses != 3 {
		t.Errorf("stats = %d hits, %d misses; want 12 and 3", hits, misses)
	}
}

// Two browsers opening the same pull at once must cost one fetch, not two —
// the shape the client already uses to coalesce token refreshes.
func TestConcurrentCallersShareOneFetch(t *testing.T) {
	src := &fakeSource{block: make(chan struct{})}
	c := NewCache(src)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := c.Report(context.Background(), "ExampleReport123"); err != nil {
				t.Error(err)
			}
		})
	}
	// Let them all arrive before the one fetch is allowed to finish.
	time.Sleep(20 * time.Millisecond)
	close(src.block)
	wg.Wait()

	if got := src.reports.Load(); got != 1 {
		t.Errorf("eight concurrent callers made %d requests, want 1", got)
	}
}

// THE RULE THE ERROR TAXONOMY WROTE FOR THIS DECORATOR BY NAME. A GraphQL
// response can arrive 200 carrying both data and errors; caching the half
// that came would serve the hole for as long as the entry lived.
func TestADocumentWithAHoleInItIsNeverCached(t *testing.T) {
	src := &fakeSource{timeline: func() (*Timeline, error) {
		return &Timeline{Duration: time.Minute, Incomplete: []string{"procs"}}, nil
	}}
	c := NewCache(src)

	for range 3 {
		if _, err := c.Timeline(context.Background(), "ExampleReport123", Fight{ID: 12}, 7, fireKnowledge(t)); err != nil {
			t.Fatal(err)
		}
	}
	if got := src.timelines.Load(); got != 3 {
		t.Errorf("a partial timeline was fetched %d times, want 3: it must never be cached", got)
	}

	// The same for a fight whose rankings would not decode.
	src2 := &fakeSource{fight: func() (*FightDetail, error) {
		return &FightDetail{Incomplete: []string{"rankings"}}, nil
	}}
	c2 := NewCache(src2)
	for range 3 {
		if _, err := c2.FightDetail(context.Background(), "ExampleReport123", 12); err != nil {
			t.Fatal(err)
		}
	}
	if got := src2.fights.Load(); got != 3 {
		t.Errorf("a partial fight was fetched %d times, want 3", got)
	}
}

// Truncated is not Incomplete. A stream cut at its page cap is complete as
// far as it goes and would be cut identically next time, so re-fetching buys
// nothing and the result is worth keeping.
func TestATruncatedStreamIsStillWorthKeeping(t *testing.T) {
	src := &fakeSource{timeline: func() (*Timeline, error) {
		return &Timeline{Duration: time.Minute, Truncated: []string{"casts"}}, nil
	}}
	c := NewCache(src)

	for range 3 {
		if _, err := c.Timeline(context.Background(), "ExampleReport123", Fight{ID: 12}, 7, fireKnowledge(t)); err != nil {
			t.Fatal(err)
		}
	}
	if got := src.timelines.Load(); got != 1 {
		t.Errorf("a truncated timeline was fetched %d times, want 1", got)
	}
}

// A report that does not exist will not exist a moment later, so saying so
// again is free. A spent budget is the opposite: remembering it would
// outlive the condition and lock the app out of its own recovery.
func TestWhichFailuresAreWorthRemembering(t *testing.T) {
	cases := map[string]struct {
		err   error
		fetch int32
	}{
		"a report that does not exist": {fmt.Errorf("%w: nope", ErrReportNotFound), 1},
		"a spent budget":               {fmt.Errorf("%w: wait", ErrBudgetExhausted), 3},
		"a rate limit":                 {fmt.Errorf("%w: slow down", ErrRateLimited), 3},
		"an outage":                    {fmt.Errorf("%w: 502", ErrUpstream), 3},
		"bad credentials":              {fmt.Errorf("%w: 401", ErrBadCredentials), 3},
	}
	for name, c := range cases {
		src := &fakeSource{reportErr: c.err}
		cache := NewCache(src)
		for range 3 {
			if _, err := cache.Report(context.Background(), "ExampleReport123"); !errors.Is(err, c.err) {
				t.Fatalf("%s: err = %v", name, err)
			}
		}
		if got := src.reports.Load(); got != c.fetch {
			t.Errorf("%s: fetched %d times, want %d", name, got, c.fetch)
		}
	}
}

// Two runs over one actor under different tables are different answers, and
// the digest on the key is what stops one being served for the other.
func TestADifferentKnowledgeIsADifferentAnswer(t *testing.T) {
	src := &fakeSource{}
	c := NewCache(src)
	ctx := context.Background()
	fight := Fight{ID: 12}

	fire := fireKnowledge(t)
	edited := fire
	edited.ProcAuras = map[int]string{48108: "Hot Streak!", 999: "Something New"}

	if _, err := c.Timeline(ctx, "ExampleReport123", fight, 7, fire); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Timeline(ctx, "ExampleReport123", fight, 7, edited); err != nil {
		t.Fatal(err)
	}
	if got := src.timelines.Load(); got != 2 {
		t.Errorf("edited tables reused the old answer (%d fetches, want 2)", got)
	}
}

// The store is bounded, and eviction must release what it drops: a reslice
// alone leaves the value reachable from the backing array, so a cache
// bounded by count would not be bounded by bytes.
func TestTheStoreIsBoundedAndReleasesWhatItDrops(t *testing.T) {
	src := &fakeSource{}
	c := NewCache(src)
	for i := range maxEntries * 2 {
		if _, err := c.FightDetail(context.Background(), "ExampleReport123", i); err != nil {
			t.Fatal(err)
		}
	}
	s := c.fights
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) > maxEntries {
		t.Errorf("holding %d entries, want at most %d", len(s.entries), maxEntries)
	}
	for i, key := range s.order[len(s.order):cap(s.order)] {
		if key != (fightKey{}) {
			t.Errorf("evicted slot %d still names %v; the backing array holds the value alive", i, key)
		}
	}
}
