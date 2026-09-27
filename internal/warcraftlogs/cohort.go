package warcraftlogs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// A cohort is other players' pulls of the same boss, on the same difficulty,
// with the same class and spec, that a player's pull is compared against.
// There are two, and they answer different questions (#60):
//
//   - the best players at the player's own item level — the top of their
//     bracket, not a sample of it, because comparing a lightly geared player
//     with other lightly geared players' average play would compare them with
//     the bottom of the barrel;
//   - the absolute top performers, whatever their gear — the reference for
//     rotation and judgement.
//
// **Nothing here that leaves this package can name a peer.** The rankings API
// hands back names, realms, guilds and report codes; they are needed to fetch
// a peer's pull and for nothing after it. They live in rankedPeer, which is
// unexported, and Peer — the only type a caller sees — has no field that could
// carry one. A peer is referred to by rank, which by the owner's rule is fine
// even though a determined reader could look the name up from it.

// CohortKind says which of the two cohorts a Cohort is.
type CohortKind int

// The two cohorts.
const (
	// SameItemLevel is the best players in the player's own item-level
	// bracket.
	SameItemLevel CohortKind = iota
	// TopPerformers is the best players at any item level.
	TopPerformers
)

// Cohort is a set of peers and how they were chosen.
type Cohort struct {
	Kind CohortKind
	// MinItemLevel and MaxItemLevel are the item levels the peers read were
	// ranked at. A bracket is a span rather than one level — the recorded
	// kill's holds 323 to 325 — so one peer's level would misname it. Both
	// are zero for the top performers, who are drawn from every bracket.
	MinItemLevel, MaxItemLevel int
	// Asked is how many peers were asked for. Peers can hold fewer: a bracket
	// with few kills in it has fewer rows, and a peer whose report is private
	// or archived is skipped rather than failing the comparison.
	Asked int
	// Skipped is how many of those could not be read.
	Skipped int
	// Peers are the pulls that were read, best first.
	Peers []Peer
}

// Peer is one other player's pull, as far as a comparison needs it. It has no
// name, realm, guild or report code, and must never gain one.
type Peer struct {
	// Rank is the peer's position on the rankings page they came from, from 1.
	Rank int
	// ItemLevel is the bracket their parse was ranked in.
	ItemLevel int
	// DPS is the rankings' own figure. It is not damage over the pull as
	// this page computes it for the player — on the recorded kill it runs
	// about 3% above Damage/Duration — so a comparison uses PullDPS, which is.
	DPS float64
	// Damage and ActiveTime come from their damage table, and are what DPS
	// while active is made of.
	Damage     float64
	ActiveTime time.Duration
	// Duration is how long their pull lasted.
	Duration time.Duration
	// Casts are their casts, as offsets into their own pull. Nothing compares
	// them yet: they are kept for #65, which reads the top performers' play to
	// judge whether a player's hold was deliberate. They cost nothing extra —
	// they come in the same document as the damage — and fetching them later
	// would change the operation and so the recording.
	Casts []PeerCast
}

// ActiveDPS is damage over the time the peer was dealing it. Zero when the
// table gave no active time, which reads as "unknown" rather than as a very
// bad pull.
func (p Peer) ActiveDPS() float64 {
	if p.ActiveTime <= 0 {
		return 0
	}
	return p.Damage / p.ActiveTime.Seconds()
}

// PullDPS is damage over the whole pull, made the way the player's own is.
func (p Peer) PullDPS() float64 {
	if p.Duration <= 0 {
		return 0
	}
	return p.Damage / p.Duration.Seconds()
}

// PeerCast is one of a peer's casts.
type PeerCast struct {
	Offset    time.Duration
	AbilityID int
}

// CohortQuery says which cohort to build.
type CohortQuery struct {
	Encounter  int
	Difficulty int
	Class      string // as Warcraft Logs spells it: "Mage", "DeathKnight"
	Spec       string // "Fire", "BeastMastery"
	Kind       CohortKind
	// Bracket is the rankings API's bracket index, for SameItemLevel. It is
	// ignored for TopPerformers, which asks for every bracket.
	Bracket int
	// Size is how many peers to compare against.
	Size int
	// Subject is the player's own pull, so they are never in their own cohort.
	// A player good enough to be on the page they are compared with would
	// otherwise be compared with themselves.
	Subject PullRef
}

// PullRef names one pull in one report, by the report's code and fight and
// the character's name. It is how the subject is kept out of their own
// cohort. It carries a name and is never shown.
type PullRef struct {
	Code  string
	Fight int
	Name  string
}

// The cohort sizes, and how many peers are fetched at once.
const (
	// DefaultBracketSize is the best at the player's item level. Ten gives a
	// median and a spread without costing more than an ordinary page view.
	DefaultBracketSize = 10
	// DefaultTopSize is the top performers: #1 to #5.
	DefaultTopSize = 5

	// peerConcurrency caps how many peers are fetched at once. The API has
	// an undocumented burst limit, and a cohort is fifteen requests at the
	// start of a background job that nobody is waiting on by the millisecond.
	peerConcurrency = 3
)

// rankedPeer is one row of a rankings page. It carries identity, and it is
// unexported so that it cannot leave the package.
type rankedPeer struct {
	rank      int
	ref       PullRef
	itemLevel int
	dps       float64
	duration  time.Duration
	// start is where their pull begins, counted from the start of their
	// report — which is what their cast timestamps count from.
	start time.Duration
}

// cohortSource is what building a cohort needs: a rankings page and a peer's
// pull. The Client answers both from the API and the Cache in front of it,
// which caches them for different lengths of time.
type cohortSource interface {
	rankingsPage(ctx context.Context, q CohortQuery, page int) ([]rankedPeer, error)
	peerPull(ctx context.Context, p rankedPeer) (*Peer, error)
}

// buildCohort asks for a rankings page and then each peer's pull.
func buildCohort(ctx context.Context, src cohortSource, q CohortQuery) (*Cohort, error) {
	if q.Encounter == 0 || q.Class == "" || q.Spec == "" || q.Size <= 0 {
		return nil, errors.New("warcraftlogs: a cohort needs an encounter, a class, a spec and a size")
	}
	c := &Cohort{Kind: q.Kind}
	if q.Kind == SameItemLevel {
		if q.Bracket <= 0 {
			return nil, errors.New("warcraftlogs: an item-level cohort needs the player's bracket")
		}
	} else {
		q.Bracket = 0
	}

	rows, err := src.rankingsPage(ctx, q, 1)
	if err != nil {
		return nil, err
	}
	var chosen []rankedPeer
	for _, r := range rows {
		if r.ref == q.Subject {
			continue
		}
		chosen = append(chosen, r)
		if len(chosen) == q.Size {
			break
		}
	}
	c.Asked = len(chosen)
	if q.Kind == SameItemLevel {
		for _, r := range chosen {
			if c.MinItemLevel == 0 || r.itemLevel < c.MinItemLevel {
				c.MinItemLevel = r.itemLevel
			}
			c.MaxItemLevel = max(c.MaxItemLevel, r.itemLevel)
		}
	}

	peers := make([]*Peer, len(chosen))
	errs := make([]error, len(chosen))
	sem := make(chan struct{}, peerConcurrency)
	var wg sync.WaitGroup
	for i, r := range chosen {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			peers[i], errs[i] = src.peerPull(ctx, r)
		})
	}
	wg.Wait()

	for i, p := range peers {
		switch err := errs[i]; {
		case err == nil && p != nil:
			c.Peers = append(c.Peers, *p)
		case errors.Is(err, ErrReportNotFound), errors.Is(err, errPeerMissing):
			// A private or archived report, or a pull whose table did not
			// name them: skipped and counted, never a failure. The page says
			// how many it compared against.
			c.Skipped++
		default:
			return nil, err
		}
	}
	slices.SortFunc(c.Peers, func(a, b Peer) int { return a.Rank - b.Rank })
	return c, nil
}

// errPeerMissing is a peer's pull that came back without them in it.
var errPeerMissing = errors.New("warcraftlogs: the peer's pull did not include them")

// Cohort fetches a cohort from the API.
func (c *Client) Cohort(ctx context.Context, q CohortQuery) (*Cohort, error) {
	return buildCohort(ctx, c, q)
}

type rankingsResponse struct {
	WorldData struct {
		Encounter *struct {
			CharacterRankings json.RawMessage `json:"characterRankings"`
		} `json:"encounter"`
	} `json:"worldData"`
}

type rankingsWire struct {
	Rankings []struct {
		Name        string  `json:"name"`
		Amount      float64 `json:"amount"`
		BracketData int     `json:"bracketData"`
		Duration    float64 `json:"duration"`
		StartTime   float64 `json:"startTime"`
		Report      struct {
			Code      string  `json:"code"`
			FightID   int     `json:"fightID"`
			StartTime float64 `json:"startTime"`
		} `json:"report"`
	} `json:"rankings"`
}

func (c *Client) rankingsPage(ctx context.Context, q CohortQuery, page int) ([]rankedPeer, error) {
	var data rankingsResponse
	vars := map[string]any{
		"encounter": q.Encounter, "difficulty": q.Difficulty,
		"className": q.Class, "specName": q.Spec,
		"bracket": q.Bracket, "page": page,
	}
	if err := c.query(ctx, rankingsOp, vars, &data); err != nil {
		return nil, err
	}
	if data.WorldData.Encounter == nil {
		return nil, fmt.Errorf("%w: no encounter %d", ErrUpstream, q.Encounter)
	}
	var wire rankingsWire
	if raw := data.WorldData.Encounter.CharacterRankings; len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &wire); err != nil {
			return nil, fmt.Errorf("%w: decode rankings: %w", ErrUpstream, err)
		}
	}
	rows := make([]rankedPeer, 0, len(wire.Rankings))
	for i, r := range wire.Rankings {
		// A row with no damage is a placeholder the API returns for a pull it
		// could not rank, and there is nothing to compare against in it.
		if r.Amount <= 0 || r.Report.Code == "" {
			continue
		}
		rows = append(rows, rankedPeer{
			rank:      (page-1)*100 + i + 1,
			ref:       PullRef{Code: r.Report.Code, Fight: r.Report.FightID, Name: r.Name},
			itemLevel: r.BracketData,
			dps:       r.Amount,
			duration:  time.Duration(r.Duration) * time.Millisecond,
			start:     time.Duration(r.StartTime-r.Report.StartTime) * time.Millisecond,
		})
	}
	return rows, nil
}

type peerResponse struct {
	ReportData struct {
		Report *struct {
			Casts struct {
				Data []event `json:"data"`
			} `json:"casts"`
			Damage struct {
				Data struct {
					Entries []struct {
						Total      float64 `json:"total"`
						ActiveTime float64 `json:"activeTime"`
					} `json:"entries"`
				} `json:"data"`
			} `json:"damage"`
		} `json:"report"`
	} `json:"reportData"`
}

func (c *Client) peerPull(ctx context.Context, p rankedPeer) (*Peer, error) {
	var data peerResponse
	vars := map[string]any{"code": p.ref.Code, "id": p.ref.Fight, "filter": nameFilter(p.ref.Name)}
	if err := c.query(ctx, peerOp, vars, &data); err != nil {
		return nil, notFound(err, "a peer's report")
	}
	report := data.ReportData.Report
	if report == nil {
		return nil, fmt.Errorf("%w: a peer's report", ErrReportNotFound)
	}
	entries := report.Damage.Data.Entries
	if len(entries) == 0 {
		return nil, errPeerMissing
	}
	peer := &Peer{
		Rank:       p.rank,
		ItemLevel:  p.itemLevel,
		DPS:        p.dps,
		Damage:     entries[0].Total,
		ActiveTime: time.Duration(entries[0].ActiveTime) * time.Millisecond,
		Duration:   p.duration,
	}
	for _, e := range report.Casts.Data {
		if e.Type != "cast" {
			continue
		}
		peer.Casts = append(peer.Casts, PeerCast{
			Offset:    time.Duration(e.Timestamp)*time.Millisecond - p.start,
			AbilityID: e.AbilityGameID,
		})
	}
	return peer, nil
}

// CohortQueries are the two comparisons for one player's pull: the best at
// their item level, then the top performers. It is the one place they are
// built, because two callers must ask for exactly the same thing — the page,
// and the recorder that captures what the page will ask for. A second copy
// that drifted would make the offline page ask for peers nobody recorded.
//
// The item-level query's Bracket is zero when the player was not ranked — a
// wipe never is — and buildCohort refuses it rather than asking every bracket.
func CohortQueries(code string, fight Fight, player PlayerStats, t *Timeline) (sameItemLevel, top CohortQuery) {
	base := CohortQuery{
		Class:   player.Class,
		Spec:    player.Spec,
		Subject: PullRef{Code: code, Fight: fight.ID, Name: player.Name},
	}
	if t != nil {
		base.Encounter = t.EncounterID
	}
	if fight.Difficulty != nil {
		base.Difficulty = *fight.Difficulty
	}
	sameItemLevel, top = base, base
	sameItemLevel.Kind, sameItemLevel.Size = SameItemLevel, DefaultBracketSize
	if player.Ranked() {
		sameItemLevel.Bracket = player.Ranking.Bracket
	}
	top.Kind, top.Size = TopPerformers, DefaultTopSize
	return sameItemLevel, top
}
