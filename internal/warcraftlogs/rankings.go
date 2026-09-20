package warcraftlogs

import (
	"encoding/json"
	"fmt"
)

// Ranking is how Warcraft Logs rated one player's pull against every other
// parse of that specialisation on that encounter and difficulty. It is the
// only judgement in this package the app does not make itself: the numbers
// arrive already computed, and are worth showing because a player cannot tell
// from raw damage whether it was good.
//
// Two percentiles arrive, and they answer different questions. RankPercent
// compares the parse with every other one; BracketPercent compares it only
// with parses at the same item level, which is the fairer of the two and the
// one the page leads with — gear is not play.
type Ranking struct {
	// RankPercent is the percentile against every parse of this spec.
	RankPercent float64
	// BracketPercent is the percentile against parses at this item level.
	BracketPercent float64
	// TotalParses is how many parses the percentile is measured against.
	TotalParses int
	// ItemLevel is the bracket this parse was ranked in, which is not
	// necessarily the player's own item level — the brackets are coarse.
	ItemLevel float64
}

// Percentile renders the bracket percentile as an ordinal, e.g. "61st". It
// rounds down: a parse is not in the 62nd percentile until it reaches it.
func (r Ranking) Percentile() string { return ordinal(int(r.BracketPercent)) }

// Overall renders the all-parses percentile as an ordinal.
func (r Ranking) Overall() string { return ordinal(int(r.RankPercent)) }

// ordinal writes a number with its English suffix. The teens are the
// exception every naive implementation gets wrong: 11, 12 and 13 take "th"
// even though 1, 2 and 3 take "st", "nd" and "rd".
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}

// rankingsData is the rankings payload, which the API returns as an untyped
// JSON scalar like the tables, so the shape is asserted here rather than by
// GraphQL. One element per fight asked for.
type rankingsData struct {
	Data []rankedFight `json:"data"`
}

// rankedFight is one fight's rankings, grouped by role. The role names are
// the API's ("tanks", "healers", "dps") and nothing here cares which is
// which: every player in the pull is ranked against their own spec.
type rankedFight struct {
	FightID int                   `json:"fightID"`
	Roles   map[string]rankedRole `json:"roles"`
}

// rankedRole is one role's ranked players.
type rankedRole struct {
	Characters []rankedCharacter `json:"characters"`
}

// rankedCharacter is one player's row. The id here is the character's global
// Warcraft Logs id and is *not* the actor id this report uses, so it cannot
// be joined on; the name is what ties a row to the roster.
//
// The leaderboard position is deliberately absent. The API sends it as
// "~573" — a string, approximate, on 26 of 30 rows in the pull this was
// checked against — and decoding that into an int both fails and, if it did
// not, would present a guess as exact.
type rankedCharacter struct {
	Name           string  `json:"name"`
	RankPercent    float64 `json:"rankPercent"`
	BracketPercent float64 `json:"bracketPercent"`
	TotalParses    int     `json:"totalParses"`
	BracketData    float64 `json:"bracketData"`
}

// rankingsByName indexes one fight's rankings by character name, which is the
// only key the payload shares with the roster. A report carrying two
// characters of one name would collide; that cannot happen, because Warcraft
// Logs disambiguates by server and a raid cannot hold the same character
// twice.
//
// The payload is absent for a pull nobody ranked — a wipe, an unranked
// difficulty, or a recording made before this field was asked for — and an
// empty map is the right answer for all three: the page shows no tile rather
// than a confident zero. ok is false only for the fourth case, a payload that
// arrived and would not decode, which the page says out loud rather than
// leaving to look like an unranked pull.
func rankingsByName(raw json.RawMessage, fightID int) (map[string]Ranking, bool) {
	byName := map[string]Ranking{}
	if len(raw) == 0 || string(raw) == "null" {
		return byName, true
	}
	var rankings rankingsData
	if err := json.Unmarshal(raw, &rankings); err != nil {
		// The payload is an untyped scalar, so its shape is a guess this
		// package makes and the API can change under it. It must not take
		// the page down with it: a tile is worth less than the fight.
		return byName, false
	}
	for _, fight := range rankings.Data {
		// fightIDs asks for one fight, but the field is a list and a
		// response carrying another fight's rankings must not be attributed
		// to this one.
		if fight.FightID != 0 && fight.FightID != fightID {
			continue
		}
		for _, role := range fight.Roles {
			for _, c := range role.Characters {
				if c.Name == "" {
					continue
				}
				byName[c.Name] = Ranking{
					RankPercent:    c.RankPercent,
					BracketPercent: c.BracketPercent,
					TotalParses:    c.TotalParses,
					ItemLevel:      c.BracketData,
				}
			}
		}
	}
	return byName, true
}
