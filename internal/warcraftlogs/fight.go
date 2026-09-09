package warcraftlogs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Actor is a participant in a report, as listed in the report's master data.
type Actor struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`    // "Player"
	SubType string `json:"subType"` // class, e.g. "Priest"
	Server  string `json:"server"`
}

// PlayerStats is a single player's contribution to one fight.
type PlayerStats struct {
	ActorID   int
	Name      string
	Server    string
	Class     string
	Spec      string
	ItemLevel float64

	Damage        float64
	Healing       float64
	Overheal      float64
	Deaths        int
	ActiveTime    time.Duration // time spent casting
	FightDuration time.Duration
}

// Spec and class read as "Blood Death Knight" rather than "Blood DeathKnight".
func (p PlayerStats) ClassName() string { return spaceCamel(p.Class) }

// Title is the player's spec and class, e.g. "Blood Death Knight".
func (p PlayerStats) Title() string {
	if p.Spec == "" {
		return p.ClassName()
	}
	return spaceCamel(p.Spec) + " " + p.ClassName()
}

// DPS is damage per second over the length of the fight.
func (p PlayerStats) DPS() float64 { return perSecond(p.Damage, p.FightDuration) }

// HPS is effective healing per second over the length of the fight.
func (p PlayerStats) HPS() float64 { return perSecond(p.Healing, p.FightDuration) }

// ActivePercent is the share of the fight the player spent casting. It is the
// crudest possible rotation metric, but it already exposes downtime.
func (p PlayerStats) ActivePercent() float64 {
	if p.FightDuration <= 0 {
		return 0
	}
	return 100 * float64(p.ActiveTime) / float64(p.FightDuration)
}

// OverhealPercent is the share of raw healing that landed on full health bars.
func (p PlayerStats) OverhealPercent() float64 {
	raw := p.Healing + p.Overheal
	if raw <= 0 {
		return 0
	}
	return 100 * p.Overheal / raw
}

func perSecond(total float64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return total / d.Seconds()
}

// spaceCamel turns "DeathKnight" into "Death Knight".
func spaceCamel(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// FightDetail is one fight plus the per-player breakdown for it.
type FightDetail struct {
	ReportCode  string
	ReportTitle string
	Fight       Fight
	Players     []PlayerStats
}

// Player returns the stats for one actor ID.
func (d *FightDetail) Player(actorID int) (PlayerStats, bool) {
	for _, p := range d.Players {
		if p.ActorID == actorID {
			return p, true
		}
	}
	return PlayerStats{}, false
}

// tableEntry is one row of a Warcraft Logs table. The API returns tables as an
// untyped JSON scalar, so the shape is asserted here rather than by GraphQL.
type tableEntry struct {
	ID         int     `json:"id"`
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Icon       string  `json:"icon"`
	ItemLevel  float64 `json:"itemLevel"`
	Total      float64 `json:"total"`
	ActiveTime float64 `json:"activeTime"`
	Overheal   float64 `json:"overheal"`
}

type tableData struct {
	Data struct {
		Entries   []tableEntry `json:"entries"`
		TotalTime float64      `json:"totalTime"`
	} `json:"data"`
}

const fightQuery = `query ($code: String!, $id: Int!) {
  reportData {
    report(code: $code) {
      code
      title
      fights(fightIDs: [$id]) {
        id name kill difficulty startTime endTime bossPercentage friendlyPlayers
      }
      masterData { actors(type: "Player") { id name type subType server } }
      damage: table(dataType: DamageDone, fightIDs: [$id])
      healing: table(dataType: Healing, fightIDs: [$id])
      deaths: table(dataType: Deaths, fightIDs: [$id])
    }
  }
}`

// FightDetail fetches one fight and the per-player damage, healing and deaths
// for it.
func (c *Client) FightDetail(ctx context.Context, code string, fightID int) (*FightDetail, error) {
	var data struct {
		ReportData struct {
			Report *struct {
				Code       string  `json:"code"`
				Title      string  `json:"title"`
				Fights     []Fight `json:"fights"`
				MasterData struct {
					Actors []Actor `json:"actors"`
				} `json:"masterData"`
				Damage  tableData `json:"damage"`
				Healing tableData `json:"healing"`
				Deaths  struct {
					Data struct {
						Entries []tableEntry `json:"entries"`
					} `json:"data"`
				} `json:"deaths"`
			} `json:"report"`
		} `json:"reportData"`
	}
	if err := c.Query(ctx, fightQuery, map[string]any{"code": code, "id": fightID}, &data); err != nil {
		return nil, err
	}
	report := data.ReportData.Report
	if report == nil {
		return nil, fmt.Errorf("warcraftlogs: report %q not found (it may be private)", code)
	}
	if len(report.Fights) == 0 {
		return nil, fmt.Errorf("warcraftlogs: report %q has no fight %d", code, fightID)
	}
	fight := report.Fights[0]

	detail := &FightDetail{
		ReportCode:  report.Code,
		ReportTitle: report.Title,
		Fight:       fight,
	}

	// Seed the roster from master data so that players who neither damaged nor
	// healed still appear in the list.
	byID := map[int]*PlayerStats{}
	inFight := map[int]bool{}
	for _, id := range fight.FriendlyPlayers {
		inFight[id] = true
	}
	for _, actor := range report.MasterData.Actors {
		if !inFight[actor.ID] {
			continue
		}
		byID[actor.ID] = &PlayerStats{
			ActorID:       actor.ID,
			Name:          actor.Name,
			Server:        actor.Server,
			Class:         actor.SubType,
			FightDuration: fight.Duration(),
		}
	}

	// The tables carry the spec (in the icon) and item level; master data does not.
	enrich := func(e tableEntry) *PlayerStats {
		p, ok := byID[e.ID]
		if !ok {
			return nil
		}
		if p.Class == "" {
			p.Class = e.Type
		}
		if p.Spec == "" {
			if _, spec, found := strings.Cut(e.Icon, "-"); found {
				p.Spec = spec
			}
		}
		if p.ItemLevel == 0 {
			p.ItemLevel = e.ItemLevel
		}
		return p
	}
	for _, e := range report.Damage.Data.Entries {
		if p := enrich(e); p != nil {
			p.Damage = e.Total
			p.ActiveTime = time.Duration(e.ActiveTime) * time.Millisecond
		}
	}
	for _, e := range report.Healing.Data.Entries {
		if p := enrich(e); p != nil {
			p.Healing = e.Total
			p.Overheal = e.Overheal
		}
	}
	for _, e := range report.Deaths.Data.Entries {
		if p, ok := byID[e.ID]; ok {
			p.Deaths++
		}
	}

	detail.Players = make([]PlayerStats, 0, len(byID))
	for _, p := range byID {
		detail.Players = append(detail.Players, *p)
	}
	sort.Slice(detail.Players, func(i, j int) bool {
		return strings.ToLower(detail.Players[i].Name) < strings.ToLower(detail.Players[j].Name)
	})
	return detail, nil
}
