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
	// DiedAt is when this player died, or zero if they did not. It arrives
	// here rather than with the analysis because the deaths table is fetched
	// by the fight query and not the timeline one, and because a Timeline is
	// one pull's while a death is one player's — the page is where the two
	// meet. It relabels the pauses a death explains; it moves nothing.
	DiedAt time.Duration
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

	// pauses are the analysis's, with anything the player's death explains
	// relabelled. They shadow the embedded Timeline's for the same reason
	// Casts does: the page must read the attributed ones or it will tell a
	// dead player they were idle.
	pauses []warcraftlogs.Pause
	// diedAt is when this player died, kept so a reason can name the moment.
	diedAt time.Duration

	Casts     []Cast
	RaidCDs   Lane
	Cooldowns Lane
	Phases    Lane
	Lusts     Lane
	// Pauses is time the player was not occupied, once the global cooldown is
	// accounted for. It is empty when the cooldown could not be modelled;
	// HasPauses is what a template must ask, because an empty lane and an
	// unmodelled one mean different things and the page says different things
	// about them.
	Pauses Lane
	Boss   []Marker
	DPS    *Graph
	Taken  *Graph
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
		Timeline: t, LeadIn: o.LeadIn, Total: o.Total, diedAt: o.DiedAt,
		RaidCDs:   Lane{Class: "rcdblock", Stacked: true, Labelled: true},
		Cooldowns: Lane{Class: "cdblock", Stacked: true, Labelled: true},
		Phases:    Lane{Class: "phase", Labelled: true},
		Lusts:     Lane{Class: "lust"},
		Pauses:    Lane{Class: "pause"},
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
	v.pauses = t.Attributed(o.DiedAt)
	for _, p := range v.pauses {
		// The tooltip carries the global cooldown this pause was measured
		// against, because that is the model's own number and printing it is
		// what lets a sceptical reader check the whole thing. It replaces the
		// threshold slider, which was the previous way to probe the model.
		title := fmt.Sprintf("%s idle from %s — global cooldown %s", short(p.Duration), p.Timestamp(), short(p.GCD))
		if reason := v.reasonFor(p, o.DiedAt); reason != "" {
			title += ", " + reason
		}
		v.Pauses.Bars = append(v.Pauses.Bars, Bar{Start: p.Start, End: p.End, Title: title})
	}
	for _, lane := range []*Lane{&v.RaidCDs, &v.Cooldowns, &v.Phases, &v.Lusts, &v.Pauses} {
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

// HasPauses reports whether the pauses lane should be drawn at all.
//
// It is not `len(Bars) > 0`. An empty lane on a modelled pull means the player
// was never idle for long enough to report, which is a real and good answer; an
// empty lane on an unmodelled pull means nothing could be measured, which is a
// different thing the page has to say out loud. A template that asked only
// about the bars would render the second as the first.
func (v *Timeline) HasPauses() bool {
	return v.Timeline != nil && v.GCD.Modelled
}

// Pauses the page should read, with anything a death explains relabelled.
// The lane above is the same list positioned; this is for the cast table,
// which reads facts rather than geometry.
func (v *Timeline) PauseList() []warcraftlogs.Pause { return v.pauses }

// PauseBefore shadows the analysis's, so the cast table shows the attributed
// reason rather than the bare one. Without it a row would say a dead player's
// pause ran "to the end of the pull", which is true and reads as a reproach.
func (v *Timeline) PauseBefore(c warcraftlogs.Cast) *warcraftlogs.Pause {
	for _, p := range v.pauses {
		if p.End == c.Offset {
			return &p
		}
	}
	return nil
}

// PauseReason is what to print next to a pause. A death carries the moment it
// happened, because "dead" alone is not checkable and "dead from 8:32" is —
// and because a pause that began before the death keeps its whole span, so the
// reader needs both numbers to see which part of it they were alive for.
func (v *Timeline) PauseReason(p warcraftlogs.Pause) string {
	return v.reasonFor(p, v.diedAt)
}

func (v *Timeline) reasonFor(p warcraftlogs.Pause, diedAt time.Duration) string {
	if p.Reason == warcraftlogs.PauseDead && diedAt > 0 {
		return "dead from " + clock(diedAt)
	}
	return string(p.Reason)
}

// clock renders a moment as m:ss, the way every timestamp on the page reads.
// internal/warcraftlogs spells its own the same way; this is here rather than
// exported from there because a moment on a page is this package's business,
// and there is nothing to share but four lines of Sprintf.
func clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}
