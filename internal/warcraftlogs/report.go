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
		if !isAlnum(r) {
			return false
		}
	}
	return true
}

// Fight is a single pull within a report. Its times are offsets in
// milliseconds from the start of the report, not absolute timestamps.
//
// This is the domain value. What the API sends is fightWire, mapped here by
// hand, so a change to the wire format is a change to one function rather
// than to every builder that takes a Fight; and how a fight is described on
// a page — its difficulty's name, the id Wowhead wants — is view.Fight's.
type Fight struct {
	ID         int
	Name       string
	Kill       *bool // nil for trash, which has no outcome
	Difficulty *int  // nil for trash; encounters carry one
	StartTime  float64
	EndTime    float64

	// Requested only by the single-fight query.
	BossPercentage  float64
	FriendlyPlayers []int
}

// fightWire is a fight as the API sends it.
type fightWire struct {
	ID              int     `json:"id"`
	Name            string  `json:"name"`
	Kill            *bool   `json:"kill"`
	Difficulty      *int    `json:"difficulty"`
	StartTime       float64 `json:"startTime"`
	EndTime         float64 `json:"endTime"`
	BossPercentage  float64 `json:"bossPercentage"`
	FriendlyPlayers []int   `json:"friendlyPlayers"`
}

// fight maps the wire value onto the domain one. Go converts between two
// struct types whose fields agree ignoring tags, so this is a conversion
// today — and the moment the wire grows a field the domain does not want,
// it stops compiling here, in the one place that is supposed to notice.
func (w fightWire) fight() Fight { return Fight(w) }

// difficultyNames maps Warcraft Logs difficulty IDs to the names Warcraft
// Logs itself gives them.
var difficultyNames = map[int]string{1: "LFR", 3: "Normal", 4: "Heroic", 5: "Mythic"}

// DifficultyName is what the difficulty is called, or "" for trash. A fact
// about the fight rather than a way of drawing it, which is why it is here
// and the Wowhead tooltip parameter is view.Fight's.
func (f Fight) DifficultyName() string {
	if f.Difficulty == nil {
		return ""
	}
	if name, ok := difficultyNames[*f.Difficulty]; ok {
		return name
	}
	return fmt.Sprintf("Difficulty %d", *f.Difficulty)
}

func fightsFromWire(wire []fightWire) []Fight {
	fights := make([]Fight, len(wire))
	for i, w := range wire {
		fights[i] = w.fight()
	}
	return fights
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
	Code      string
	Title     string
	StartTime float64
	EndTime   float64
	Owner     struct{ Name string }
	Zone      struct{ Name string }
	Fights    []Fight
}

// reportWire is a report as the API sends it.
type reportWire struct {
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
	Fights []fightWire `json:"fights"`
}

func (w *reportWire) report() *Report {
	r := &Report{Code: w.Code, Title: w.Title, StartTime: w.StartTime, EndTime: w.EndTime, Fights: fightsFromWire(w.Fights)}
	r.Owner.Name = w.Owner.Name
	r.Zone.Name = w.Zone.Name
	return r
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

// reportResponse is the envelope the report query returns.
type reportResponse struct {
	ReportData struct {
		Report *reportWire `json:"report"`
	} `json:"reportData"`
}

// Report fetches the basic metadata for a report by its code.
//
// There is deliberately no fetch/build split here, unlike FightDetail and
// Timeline: everything after the query is a nil check whose only input is the
// code parameter it needs for the message. A free function for that would be
// one branch behind a second name.
func (c *Client) Report(ctx context.Context, code string) (*Report, error) {
	var data reportResponse
	err := c.query(ctx, reportOp, map[string]any{"code": code}, &data)
	if data.ReportData.Report == nil {
		return nil, notFound(err, code)
	}
	if err != nil {
		return nil, err
	}
	return data.ReportData.Report.report(), nil
}

func isAlnum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}
