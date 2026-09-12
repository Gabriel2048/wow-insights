// Package view turns an analysed timeline into something a page can draw.
//
// The analysis in internal/warcraftlogs carries absolute times relative to
// one pull and nothing else; this package decides where those land on a
// screen. The split exists because a position is only meaningful against
// a chosen axis: a 40% mark on a three-minute wipe and a 40% mark on a
// six-minute kill are the same number and different moments, and comparing
// pulls — which #3 is for — needs both drawn on one shared axis. Options
// carries that axis, and the same analysis value can be laid out at two
// scales without being rebuilt.
//
// Nothing here is imported by the analysis, so "the domain carries no
// geometry" is a fact the compiler checks rather than a convention.
package view

import (
	"fmt"
	"time"

	"wowinsight/internal/warcraftlogs"
)

// minLeadIn keeps a sliver of pre-pull room even when nothing was precast, so
// the pull reads as a moment on a timeline rather than a hard edge.
const minLeadIn = 1500 * time.Millisecond

// leadInMargin is the breathing room left to the left of the earliest cast.
const leadInMargin = 750 * time.Millisecond

// Options is the axis a timeline is drawn against. Zero values are derived
// from the timeline itself — enough lead-in for its earliest cast, and a
// total that ends with the pull. To draw two pulls side by side on one axis,
// give both the same LeadIn and Total.
type Options struct {
	LeadIn time.Duration // drawn room before the pull
	Total  time.Duration // the whole drawn span, lead-in included
	// WowheadDifficulty is the id Wowhead's tooltips take for this fight's
	// difficulty, so a boss marker can link to the right spell values; zero
	// links to the spell without one.
	WowheadDifficulty int
}

// Timeline is an analysed timeline with a position for everything on it.
// The analysis is embedded, so the page reads facts (names, labels, counts)
// from it and geometry from here; the fields that shadow the analysis's
// (Casts, Phases, …) carry the positioned versions.
type Timeline struct {
	*warcraftlogs.Timeline

	LeadIn      time.Duration
	Total       time.Duration
	PullPercent float64

	Casts     []Cast
	RaidCDs   Lane
	Cooldowns Lane
	Phases    Lane
	Lusts     Lane
	Boss      []Marker
	DPS       *Graph
	Taken     *Graph
}

// Cast is one cast with its place on the track.
type Cast struct {
	warcraftlogs.Cast
	Percent          float64 // where it starts
	CastWidthPercent float64 // how wide its bar is: a completed one, or an abandoned one
	GapStartPercent  float64 // where the idle before it began
	GapWidthPercent  float64
}

// Lane is a row of labelled bars — raid cooldowns, personal cooldowns,
// phases, lust windows. They are the same thing on screen: a span with a
// label, a tooltip and a row to sit on so overlapping bars stack rather
// than collide. One type, one packing, one piece of markup; a new lane
// costs a Lane, not a struct and a template block.
type Lane struct {
	Class    string // the CSS class of each bar: what the lane looks like
	Stacked  bool   // bars sit on rows; otherwise they overlay
	Labelled bool   // bars carry their name; otherwise the tooltip is all
	Rows     int
	Bars     []Bar
}

// Bar is one span on a lane.
type Bar struct {
	StartPercent float64
	WidthPercent float64
	Row          int
	Start, End   time.Duration
	Name         string // what is drawn on the bar
	Title        string // the tooltip
	Class        string // an extra CSS class: intermission, or nothing
}

// Marker is one boss cast on the boss lane: a tick with a link, and a bar
// when the cast took time.
type Marker struct {
	warcraftlogs.BossCast
	Percent      float64
	WidthPercent float64
	Href         string // the spell on Wowhead, at this fight's difficulty
}

// Graph is a damage curve with its SVG paths drawn against the axis.
type Graph struct {
	*warcraftlogs.DPSGraph
	Line string // an SVG polyline in a 0-100 by 0-100 viewBox
	Area string // the same shape closed to the baseline, for filling
}

// Layout positions everything on the timeline against the axis the options
// describe. Positions are computed in one place so the casts, phases, lust
// windows and DPS curve cannot drift out of alignment.
func Layout(t *warcraftlogs.Timeline, o Options) *Timeline {
	v := &Timeline{
		Timeline: t, LeadIn: o.LeadIn, Total: o.Total,
		RaidCDs:   Lane{Class: "rcdblock", Stacked: true, Labelled: true},
		Cooldowns: Lane{Class: "cdblock", Stacked: true, Labelled: true},
		Phases:    Lane{Class: "phase", Labelled: true},
		Lusts:     Lane{Class: "lust"},
	}
	if v.LeadIn == 0 {
		v.LeadIn = minLeadIn
		for _, c := range t.Casts {
			if c.Offset < 0 && -c.Offset+leadInMargin > v.LeadIn {
				v.LeadIn = -c.Offset + leadInMargin
			}
		}
	}
	if v.Total == 0 {
		v.Total = v.LeadIn + t.Duration
	}
	if v.Total <= 0 {
		return v
	}
	v.PullPercent = v.percent(0)

	v.Casts = make([]Cast, len(t.Casts))
	for i, c := range t.Casts {
		vc := Cast{Cast: c, Percent: v.percent(c.Offset), CastWidthPercent: v.span(c.End - c.Offset), GapWidthPercent: v.span(c.Gap)}
		vc.GapStartPercent = vc.Percent - vc.GapWidthPercent
		v.Casts[i] = vc
	}

	for _, w := range t.RaidCDs {
		v.RaidCDs.Bars = append(v.RaidCDs.Bars, Bar{
			Start: w.Start, End: w.End, Name: w.Name,
			Title: fmt.Sprintf("%s — %s for %s on %d players", w.Label(), w.Timestamp(), short(w.Duration()), w.Targets),
		})
	}
	for _, w := range t.Cooldowns {
		v.Cooldowns.Bars = append(v.Cooldowns.Bars, Bar{
			Start: w.Start, End: w.End, Name: w.Name,
			Title: fmt.Sprintf("%s — %s for %s", w.Name, w.Timestamp(), short(w.Duration())),
		})
	}
	for _, p := range t.Phases {
		class := ""
		if p.IsIntermission {
			class = "intermission"
		}
		v.Phases.Bars = append(v.Phases.Bars, Bar{
			Start: p.Start, End: p.End, Name: p.ShortName(), Class: class,
			Title: fmt.Sprintf("%s — %s for %s", p.Name, p.Timestamp(), short(p.Duration())),
		})
	}
	for _, l := range t.Lusts {
		v.Lusts.Bars = append(v.Lusts.Bars, Bar{
			Start: l.Start, End: l.End, Name: l.Name,
			Title: fmt.Sprintf("%s — %s for %s", l.Label(), l.Timestamp(), short(l.Duration())),
		})
	}
	for _, lane := range []*Lane{&v.RaidCDs, &v.Cooldowns, &v.Phases, &v.Lusts} {
		lane.Rows = packRows(lane.Bars)
		for i := range lane.Bars {
			b := &lane.Bars[i]
			b.StartPercent = v.percent(b.Start)
			b.WidthPercent = v.span(b.End - b.Start)
		}
	}

	for _, b := range t.BossCasts {
		href := fmt.Sprintf("https://www.wowhead.com/spell=%d", b.AbilityID)
		if o.WowheadDifficulty != 0 {
			href += fmt.Sprintf("?dd=%d", o.WowheadDifficulty)
		}
		v.Boss = append(v.Boss, Marker{BossCast: b, Percent: v.percent(b.Offset), WidthPercent: v.span(b.End - b.Offset), Href: href})
	}
	v.DPS = v.graph(t.DPS)
	v.Taken = v.graph(t.Taken)
	return v
}

// percent places a fight-relative offset along the drawn timeline.
func (v *Timeline) percent(offset time.Duration) float64 {
	return 100 * float64(offset+v.LeadIn) / float64(v.Total)
}

// span converts a length of time into a width along the drawn timeline.
func (v *Timeline) span(d time.Duration) float64 {
	return 100 * float64(d) / float64(v.Total)
}

// TotalMS is the drawn span in milliseconds, for the timeline ruler.
func (v *Timeline) TotalMS() int64 { return v.Total.Milliseconds() }

// LeadInMS is the pre-pull span in milliseconds, for the timeline ruler.
func (v *Timeline) LeadInMS() int64 { return v.LeadIn.Milliseconds() }

// packRows assigns each bar the first row already free at the moment it
// starts, so overlapping bars stack instead of colliding, and returns how
// many rows that took. Bars must already be sorted by start. Plain and
// non-generic on purpose: every lane is a []Bar, so there is one of these.
func packRows(bars []Bar) int {
	var rowEnds []time.Duration
	for i := range bars {
		placed := false
		for row, end := range rowEnds {
			if bars[i].Start >= end {
				bars[i].Row = row
				rowEnds[row] = bars[i].End
				placed = true
				break
			}
		}
		if !placed {
			bars[i].Row = len(rowEnds)
			rowEnds = append(rowEnds, bars[i].End)
		}
	}
	return len(rowEnds)
}

// short renders a duration the way the page's `short` template func does,
// for tooltips built here.
func short(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// HasPhases reports whether the encounter had phase data.
func (v *Timeline) HasPhases() bool { return len(v.Phases.Bars) > 0 }

// HasLusts reports whether any raid haste buff ran during the pull.
func (v *Timeline) HasLusts() bool { return len(v.Lusts.Bars) > 0 }
