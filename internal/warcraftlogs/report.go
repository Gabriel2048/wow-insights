package warcraftlogs

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ErrNotAReportURL is returned when a URL does not point at a Warcraft Logs report.
var ErrNotAReportURL = errors.New("warcraftlogs: not a Warcraft Logs report URL")

// ParseReportCode extracts the report code from a Warcraft Logs report URL,
// e.g. https://www.warcraftlogs.com/reports/ExampleReport123 -> ExampleReport123.
// A bare report code is accepted as-is. Trailing query strings and fragments
// (the "?fight=3" and "#fight=last" that the site adds) are ignored.
func ParseReportCode(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrNotAReportURL
	}

	// A bare code, with no scheme or path separators.
	if !strings.Contains(raw, "/") && isReportCode(raw) {
		return raw, nil
	}

	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ErrNotAReportURL
	}
	if host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."); host != "warcraftlogs.com" {
		return "", ErrNotAReportURL
	}

	// The code is the segment following "reports"; locales prefix the path, as
	// in /de/reports/CODE.
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, segment := range segments {
		if segment == "reports" && i+1 < len(segments) {
			if code := segments[i+1]; isReportCode(code) {
				return code, nil
			}
			return "", ErrNotAReportURL
		}
	}
	return "", ErrNotAReportURL
}

// isReportCode reports whether s looks like a report code: 16 alphanumeric
// characters.
func isReportCode(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// Fight is a single pull within a report. Its times are offsets in
// milliseconds from the start of the report, not absolute timestamps.
type Fight struct {
	ID         int     `json:"id"`
	Name       string  `json:"name"`
	Kill       *bool   `json:"kill"`
	Difficulty *int    `json:"difficulty"`
	StartTime  float64 `json:"startTime"`
	EndTime    float64 `json:"endTime"`

	// Requested only by the single-fight query.
	BossPercentage  float64 `json:"bossPercentage"`
	FriendlyPlayers []int   `json:"friendlyPlayers"`
}

// difficultyNames maps Warcraft Logs difficulty IDs to their raid names.
var difficultyNames = map[int]string{1: "LFR", 3: "Normal", 4: "Heroic", 5: "Mythic"}

// wowheadDifficulties maps Warcraft Logs difficulty IDs to the game's own
// difficulty IDs, which Wowhead takes as its "dd" tooltip parameter. Without
// it Wowhead shows Mythic values for every spell.
var wowheadDifficulties = map[int]int{
	1: 17, // Looking For Raid
	3: 14, // Normal
	4: 15, // Heroic
	5: 16, // Mythic
}

// WowheadDifficulty is the difficulty id to ask Wowhead for, or 0 when the
// fight has no difficulty we can map.
func (f Fight) WowheadDifficulty() int {
	if f.Difficulty == nil {
		return 0
	}
	return wowheadDifficulties[*f.Difficulty]
}

// DifficultyName is the human name of the fight difficulty, or "" for trash.
func (f Fight) DifficultyName() string {
	if f.Difficulty == nil {
		return ""
	}
	if name, ok := difficultyNames[*f.Difficulty]; ok {
		return name
	}
	return fmt.Sprintf("Difficulty %d", *f.Difficulty)
}

// IsBoss reports whether the fight is an encounter rather than trash. Only
// encounters carry a difficulty.
func (f Fight) IsBoss() bool { return f.Difficulty != nil }

// Outcome is "Kill", "Wipe", or "" for trash.
func (f Fight) Outcome() string {
	if f.Kill == nil {
		return ""
	}
	if *f.Kill {
		return "Kill"
	}
	return "Wipe"
}

// Duration is how long the pull lasted.
func (f Fight) Duration() time.Duration {
	return time.Duration(f.EndTime-f.StartTime) * time.Millisecond
}

// Report is the basic metadata of a logged raid or dungeon session.
type Report struct {
	Code      string  `json:"code"`
	Title     string  `json:"title"`
	StartTime float64 `json:"startTime"`
	EndTime   float64 `json:"endTime"`
	Owner     struct {
		Name string `json:"name"`
	} `json:"owner"`
	Zone struct {
		Name string `json:"name"`
	} `json:"zone"`
	Fights []Fight `json:"fights"`
}

// StartedAt is the report's start as an absolute time.
func (r Report) StartedAt() time.Time {
	return time.UnixMilli(int64(r.StartTime)).UTC()
}

// Duration is the wall-clock length of the session.
func (r Report) Duration() time.Duration {
	return time.Duration(r.EndTime-r.StartTime) * time.Millisecond
}

// Kills counts the boss fights that ended in a kill.
func (r Report) Kills() int {
	n := 0
	for _, f := range r.Fights {
		if f.Kill != nil && *f.Kill {
			n++
		}
	}
	return n
}

// Wipes counts the boss fights that ended in a wipe.
func (r Report) Wipes() int {
	n := 0
	for _, f := range r.Fights {
		if f.Kill != nil && !*f.Kill {
			n++
		}
	}
	return n
}

// BossFights returns only the encounter pulls, dropping trash.
func (r Report) BossFights() []Fight {
	fights := make([]Fight, 0, len(r.Fights))
	for _, f := range r.Fights {
		if f.IsBoss() {
			fights = append(fights, f)
		}
	}
	return fights
}

const reportQuery = `query ($code: String!) {
  reportData {
    report(code: $code) {
      code
      title
      startTime
      endTime
      owner { name }
      zone { name }
      fights { id name kill difficulty startTime endTime }
    }
  }
}`

// Report fetches the basic metadata for a report by its code.
func (c *Client) Report(ctx context.Context, code string) (*Report, error) {
	var data struct {
		ReportData struct {
			Report *Report `json:"report"`
		} `json:"reportData"`
	}
	if err := c.Query(ctx, reportQuery, map[string]any{"code": code}, &data); err != nil {
		return nil, err
	}
	if data.ReportData.Report == nil {
		return nil, fmt.Errorf("warcraftlogs: report %q not found (it may be private)", code)
	}
	return data.ReportData.Report, nil
}
