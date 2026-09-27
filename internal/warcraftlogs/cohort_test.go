package warcraftlogs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// peersOnAPage returns n ranked rows the way a rankings page lists them, with
// the subject's own pull at position subjectAt (0 for nowhere).
func peersOnAPage(n, subjectAt int) []rankedPeer {
	rows := make([]rankedPeer, 0, n)
	for i := 1; i <= n; i++ {
		ref := PullRef{Code: fmt.Sprintf("PeerReport%03d", i), Fight: 7, Name: fmt.Sprintf("Peer%d", i)}
		if i == subjectAt {
			ref = subject()
		}
		rows = append(rows, rankedPeer{rank: i, ref: ref, itemLevel: 324, dps: float64(300000 - i*1000)})
	}
	return rows
}

func subject() PullRef { return PullRef{Code: "ExampleReport123", Fight: 29, Name: "Testmage"} }

func bracketQuery() CohortQuery {
	return CohortQuery{Encounter: 3429, Difficulty: 4, Class: "Mage", Spec: "Fire",
		Kind: SameItemLevel, Bracket: 18, Size: 10, Subject: subject()}
}

// **Nothing a caller can see can carry a name.** The rankings API hands back
// names, realms, guilds and report codes; they are needed to fetch a pull and
// for nothing after it. This walks every type that leaves the package and
// fails if any of them grows a string field — which is the only kind a name, a
// realm, a guild or a report code could travel in. A new field that needs to
// be text has to justify itself here first.
func TestNoTypeACallerSeesCanCarryAPeersName(t *testing.T) {
	var walk func(reflect.Type, string)
	seen := map[reflect.Type]bool{}
	walk = func(ty reflect.Type, path string) {
		for ty.Kind() == reflect.Pointer || ty.Kind() == reflect.Slice {
			ty = ty.Elem()
		}
		if ty.Kind() != reflect.Struct || seen[ty] {
			return
		}
		seen[ty] = true
		for f := range ty.Fields() {
			if f.Type.Kind() == reflect.String {
				t.Errorf("%s.%s is a string, and a peer's name, realm, guild or report code could travel in it", path, f.Name)
			}
			walk(f.Type, path+"."+f.Name)
		}
	}
	walk(reflect.TypeFor[Cohort](), "Cohort")
}

// A player good enough to appear on the page they are compared with would
// otherwise be compared with themselves.
func TestThePlayerIsNeverInTheirOwnCohort(t *testing.T) {
	src := &fakeSource{rows: func(CohortQuery) ([]rankedPeer, error) { return peersOnAPage(20, 3), nil }}
	c, err := buildCohort(context.Background(), src, bracketQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Peers) != 10 {
		t.Fatalf("%d peers, want the 10 asked for", len(c.Peers))
	}
	for _, p := range c.Peers {
		if p.Rank == 3 {
			t.Error("the player's own pull is in their cohort")
		}
	}
	// The ten are the best ten who are not them: 1, 2, 4 … 11.
	if got := c.Peers[len(c.Peers)-1].Rank; got != 11 {
		t.Errorf("the last peer is rank %d, want 11 — the subject's place is filled by the next one down", got)
	}
}

// A private or archived report, or a pull that came back without the peer in
// it, is skipped and counted. The page says how many it compared against;
// one missing peer must not cost the player the comparison.
func TestAnUnreadablePeerIsSkippedAndCounted(t *testing.T) {
	src := &fakeSource{
		rows: func(CohortQuery) ([]rankedPeer, error) { return peersOnAPage(5, 0), nil },
		pull: func(p rankedPeer) (*Peer, error) {
			switch p.rank {
			case 2:
				return nil, fmt.Errorf("%w: a peer's report", ErrReportNotFound)
			case 4:
				return nil, errPeerMissing
			}
			return &Peer{Rank: p.rank, DPS: p.dps}, nil
		},
	}
	q := bracketQuery()
	q.Size = 5
	c, err := buildCohort(context.Background(), src, q)
	if err != nil {
		t.Fatalf("an unreadable peer failed the whole cohort: %v", err)
	}
	if c.Asked != 5 || c.Skipped != 2 || len(c.Peers) != 3 {
		t.Errorf("asked %d, skipped %d, read %d; want 5, 2, 3", c.Asked, c.Skipped, len(c.Peers))
	}
}

// Anything else going wrong is not a peer's business: it is the budget, or
// the API being down, and the comparison says so rather than quietly showing
// three peers where there should be ten.
func TestAnyOtherFailureFailsTheCohort(t *testing.T) {
	src := &fakeSource{
		rows: func(CohortQuery) ([]rankedPeer, error) { return peersOnAPage(5, 0), nil },
		pull: func(p rankedPeer) (*Peer, error) {
			if p.rank == 3 {
				return nil, ErrRateLimited
			}
			return &Peer{Rank: p.rank}, nil
		},
	}
	if _, err := buildCohort(context.Background(), src, bracketQuery()); !errors.Is(err, ErrRateLimited) {
		t.Errorf("err = %v, want the rate limit to surface", err)
	}
}

// The top performers are drawn from every item level, which is bracket zero
// to the API — whatever bracket the player's own ranking was in.
func TestTheTopPerformersAreAskedForAcrossEveryBracket(t *testing.T) {
	var asked CohortQuery
	src := &fakeSource{rows: func(q CohortQuery) ([]rankedPeer, error) { asked = q; return peersOnAPage(5, 0), nil }}
	q := bracketQuery()
	q.Kind, q.Size = TopPerformers, 5
	c, err := buildCohort(context.Background(), src, q)
	if err != nil {
		t.Fatal(err)
	}
	if asked.Bracket != 0 {
		t.Errorf("the top performers were asked for in bracket %d, want every bracket (0)", asked.Bracket)
	}
	if c.MinItemLevel != 0 || c.MaxItemLevel != 0 {
		t.Errorf("the top performers claim item levels %d–%d; they are drawn from all of them", c.MinItemLevel, c.MaxItemLevel)
	}
}

// An item-level cohort with no bracket to ask for is refused rather than
// silently becoming the top performers — bracket zero means every bracket.
func TestAnItemLevelCohortNeedsABracket(t *testing.T) {
	q := bracketQuery()
	q.Bracket = 0
	if _, err := buildCohort(context.Background(), &fakeSource{}, q); err == nil {
		t.Error("an item-level cohort with no bracket was built, and would have compared the player with every item level")
	}
}

// The same peer is a different rank on different pages — third at their item
// level, forty-first overall — and their pull is cached once, by who and
// where. So the cached pull must not carry one page's rank into the other.
func TestACachedPeerTakesTheRankOfThePageItWasFoundOn(t *testing.T) {
	shared := PullRef{Code: "PeerReportShared", Fight: 7, Name: "Shared"}
	src := &fakeSource{rows: func(q CohortQuery) ([]rankedPeer, error) {
		rank := 3
		if q.Kind == TopPerformers {
			rank = 1
		}
		return []rankedPeer{{rank: rank, ref: shared, itemLevel: 324, dps: 250000}}, nil
	}}
	cache := NewCache(src)
	q := bracketQuery()
	q.Size = 1
	bracket, err := cache.Cohort(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	q.Kind = TopPerformers
	top, err := cache.Cohort(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if src.pulls.Load() != 1 {
		t.Errorf("the same pull was fetched %d times; a logged pull never changes", src.pulls.Load())
	}
	if bracket.Peers[0].Rank != 3 || top.Peers[0].Rank != 1 {
		t.Errorf("ranks %d and %d, want 3 on the item-level page and 1 on the top page", bracket.Peers[0].Rank, top.Peers[0].Rank)
	}
}

// DPS while active is damage over the time spent dealing it. With no active
// time it is unknown, never an enormous or a zero pull.
func TestDPSWhileActive(t *testing.T) {
	p := Peer{Damage: 90_000_000, ActiveTime: 300 * time.Second}
	if got := p.ActiveDPS(); got != 300_000 {
		t.Errorf("ActiveDPS = %v, want 300000", got)
	}
	if (Peer{Damage: 90_000_000}).ActiveDPS() != 0 {
		t.Error("a peer with no active time has a DPS while active")
	}
}

// The filter is the only place a name is written into an expression, and it
// must not be possible to end the quoted string early.
func TestANameCannotEscapeItsFilter(t *testing.T) {
	for name, want := range map[string]string{
		"Testmage":    `source.name = "Testmage"`,
		`Evil" or "1`: `source.name = "Evil\" or \"1"`,
		`Back\slash`:  `source.name = "Back\\slash"`,
		"Ýlvaría":     `source.name = "Ýlvaría"`,
	} {
		if got := nameFilter(name); got != want {
			t.Errorf("nameFilter(%q) = %s, want %s", name, got, want)
		}
	}
}

// The client end to end against a fake API: rows the API could not rank are
// dropped, a peer's casts become offsets into their own pull, and a pull that
// came back without them is reported as missing rather than as a peer with
// no damage.
func TestTheClientReadsARankingsPageAndAPull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
			return
		}
		var body struct {
			OperationName string         `json:"operationName"`
			Variables     map[string]any `json:"variables"`
		}
		_ = decodeJSON(r, &body)
		switch body.OperationName {
		case "Rankings":
			_, _ = w.Write([]byte(`{"data":{"worldData":{"encounter":{"characterRankings":{"rankings":[
				{"name":"A","amount":300000,"bracketData":324,"duration":371455,"startTime":1026553697,
				 "report":{"code":"PeerA","fightID":7,"startTime":1000000000}},
				{"name":"B","amount":0,"bracketData":324,"duration":300000,"startTime":1,"report":{"code":"PeerB","fightID":3,"startTime":0}},
				{"name":"C","amount":250000,"bracketData":323,"duration":300000,"startTime":5000,"report":{"code":"PeerC","fightID":2,"startTime":0}}
			]}}}}}`))
		case "Peer":
			if body.Variables["code"] == "PeerC" {
				_, _ = w.Write([]byte(`{"data":{"reportData":{"report":{"casts":{"data":[]},"damage":{"data":{"entries":[]}}}}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"reportData":{"report":{
				"casts":{"data":[{"timestamp":26554015,"type":"cast","sourceID":9,"abilityGameID":133},
				                 {"timestamp":26554100,"type":"begincast","sourceID":9,"abilityGameID":11366}]},
				"damage":{"data":{"entries":[{"total":90000000,"activeTime":300000}]}}}}}}`))
		}
	}))
	defer srv.Close()
	c := New("id", "secret", WithBaseURL(srv.URL+"/token", srv.URL+"/api"))

	rows, err := c.rankingsPage(context.Background(), bracketQuery(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2 — the unranked placeholder dropped", len(rows))
	}
	if rows[0].rank != 1 || rows[1].rank != 3 {
		t.Errorf("ranks %d and %d, want 1 and 3: a dropped row still held its place", rows[0].rank, rows[1].rank)
	}

	peer, err := c.peerPull(context.Background(), rows[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(peer.Casts) != 1 {
		t.Fatalf("%d casts, want 1 — a begincast is not a cast", len(peer.Casts))
	}
	if got := peer.Casts[0].Offset; got != 318*time.Millisecond {
		t.Errorf("the first cast is %v into the pull, want 318ms", got)
	}
	if peer.ActiveDPS() != 300000 {
		t.Errorf("ActiveDPS = %v, want 300000", peer.ActiveDPS())
	}

	if _, err := c.peerPull(context.Background(), rows[1]); !errors.Is(err, errPeerMissing) {
		t.Errorf("a pull without the peer in it: err = %v, want errPeerMissing", err)
	}
}

func decodeJSON(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }

// comparedAgainst is a player and both cohorts, with numbers chosen so that
// every figure the check states can only have come from one place: the peers'
// rankings DPS is absurd, so a check that read it instead of their damage
// would say so.
func comparedAgainst() PlayerContext {
	peer := func(damage float64, active, pull time.Duration, ilvl int) Peer {
		return Peer{ItemLevel: ilvl, DPS: 999_999, Damage: damage, ActiveTime: active, Duration: pull}
	}
	s := time.Second
	return PlayerContext{
		Damage: 72_000_000, ActiveTime: 400 * s, Duration: 450 * s,
		Cohorts: []CohortResult{
			{Kind: SameItemLevel, Cohort: &Cohort{Kind: SameItemLevel, MinItemLevel: 323, MaxItemLevel: 325, Asked: 4, Skipped: 1,
				Peers: []Peer{
					peer(90_000_000, 400*s, 450*s, 325), // 225k active, 200k over the pull
					peer(80_000_000, 400*s, 400*s, 323), // 200k, 200k
					peer(96_000_000, 400*s, 480*s, 324), // 240k, 200k
				}}},
			{Kind: TopPerformers, Unasked: "Warcraft Logs is not answering"},
		},
	}
}

func TestTheComparisonIsWhileActiveAndOverThePullAlike(t *testing.T) {
	ch, findings := cohortCheck(SameItemLevel, comparedAgainst())
	if len(findings) != 0 {
		t.Errorf("a comparison raised %d findings; findings built on it are #65's", len(findings))
	}
	if !ch.Asked {
		t.Fatalf("not asked: %s", ch.Unasked)
	}
	if want := "yours 180k while active; the best 3 at item level 323–325: median 225k"; ch.Measured != want {
		t.Errorf("Measured = %q, want %q", ch.Measured, want)
	}
	got := map[string]string{}
	for _, e := range ch.Evidence {
		got[e.Label] = e.Value
	}
	for label, want := range map[string]string{
		"yours, while active":   "180k",
		"yours, over the pull":  "160k",
		"theirs, while active":  "median 225k, 200k to 240k",
		"theirs, over the pull": "median 200k", // their damage over their pull, as yours is; not the rankings' figure
		"compared against":      "3 players",
		"could not be read":     "1",
	} {
		if got[label] != want {
			t.Errorf("%s = %q, want %q", label, got[label], want)
		}
	}
	if ch.Note == "" {
		t.Error("the comparison does not say what while active cannot tell")
	}
}

func TestAnItemLevelCohortOfOneLevelNamesOne(t *testing.T) {
	who := comparedAgainst()
	who.Cohorts[0].Cohort.MinItemLevel, who.Cohorts[0].Cohort.MaxItemLevel = 324, 324
	ch, _ := cohortCheck(SameItemLevel, who)
	if !strings.Contains(ch.Measured, "at item level 324:") {
		t.Errorf("Measured = %q, want the one level named once", ch.Measured)
	}
}

// Why a cohort was not asked for is decided where the question was, and the
// check says it in those words rather than a generic one.
func TestAnUnaskedCohortSaysWhy(t *testing.T) {
	ch, _ := cohortCheck(TopPerformers, comparedAgainst())
	if ch.Asked || ch.Unasked != "Warcraft Logs is not answering" {
		t.Errorf("Asked = %v, Unasked = %q; want the reason it was given", ch.Asked, ch.Unasked)
	}
	ch, _ = cohortCheck(TopPerformers, PlayerContext{})
	if ch.Asked || ch.Unasked == "" {
		t.Errorf("with no cohort at all: Asked = %v, Unasked = %q", ch.Asked, ch.Unasked)
	}
}
