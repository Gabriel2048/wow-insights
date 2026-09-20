package warcraftlogs

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// DPSPoint is one bucket of the damage graph.
type DPSPoint struct {
	Offset time.Duration
	DPS    float64
}

// DPSGraph is a player's damage over the course of a fight. Drawing it is
// internal/view's job.
type DPSGraph struct {
	Points   []DPSPoint
	Peak     float64
	Mean     float64
	Interval time.Duration
}

// dpsGraphResponse mirrors the untyped JSON the graph field returns. Warcraft
// Logs sends one series per ability, each a flat run of values rather than
// timestamped pairs.
type dpsGraphResponse struct {
	Data struct {
		Series []struct {
			// Name is decoded because one of the series is the sum of the
			// others. Without it every value was counted twice.
			Name          string    `json:"name"`
			PointStart    float64   `json:"pointStart"`
			PointInterval float64   `json:"pointInterval"`
			Data          []float64 `json:"data"`
		} `json:"series"`
	} `json:"data"`
}

// totalSeries is the name Warcraft Logs gives the series that is already the
// sum of all the others. On the committed recording it is the eighteenth of
// eighteen, and per bucket it equals the other seventeen added up to the
// decimal — 104,119.6 against 104,119.7.
const totalSeries = "Total"

// buildDPS sums the per-ability series into a single damage-per-second curve.
//
// Two things here were wrong together and nearly cancelled, which is why
// neither could be fixed alone. The series were all summed including the one
// called Total, doubling every value; and the values, which are already
// damage per second, were then divided by the bucket width as though they
// were damage. The page read about 10% high, and dropping the Total series
// on its own would have left it 45% low.
func buildDPS(response dpsGraphResponse, fight Fight) *DPSGraph {
	series := response.Data.Series
	if len(series) == 0 {
		return nil
	}

	// Every series shares a bucket width; the first one defines the grid,
	// and a series on a different grid cannot be summed into it and is
	// dropped. The API has never sent one — every series of a graph shares
	// pointStart and pointInterval on every real report seen — so this is a
	// guard against the assumption, not a path with traffic.
	interval := series[0].PointInterval
	base := series[0].PointStart
	for _, s := range series {
		if s.PointStart < base {
			base = s.PointStart
		}
	}
	if interval <= 0 {
		return nil
	}

	totals := map[int]float64{}
	last := -1
	for _, s := range series {
		if s.PointInterval != interval || s.Name == totalSeries {
			continue
		}
		offset := int(math.Round((s.PointStart - base) / interval))
		for i, v := range s.Data {
			index := offset + i
			totals[index] += v
			if index > last {
				last = index
			}
		}
	}
	if last < 0 {
		return nil
	}

	graph := &DPSGraph{Interval: time.Duration(interval) * time.Millisecond}

	var sum float64
	for i := 0; i <= last; i++ {
		// No division: the API sends damage per second, not damage per
		// bucket. The table's own figure for a player agrees with the mean
		// of these values to within a percent, and a test holds the two to
		// each other rather than to a number somebody wrote down.
		dps := totals[i]
		// A time, not a position: internal/view draws the curve on the same
		// axis as the casts.
		point := DPSPoint{
			Offset: time.Duration(base-fight.StartTime)*time.Millisecond +
				time.Duration(float64(i)*interval)*time.Millisecond,
			DPS: dps,
		}
		if dps > graph.Peak {
			graph.Peak = dps
		}
		sum += dps
		graph.Points = append(graph.Points, point)
	}
	graph.Mean = sum / float64(len(graph.Points))
	return graph
}

// HalfPeak is the midpoint of the vertical scale, matching the dashed gridline.
func (g *DPSGraph) HalfPeak() float64 { return g.Peak / 2 }

// StartMS is the offset of the first bucket, relative to the pull.
func (g *DPSGraph) StartMS() int64 {
	if len(g.Points) == 0 {
		return 0
	}
	return g.Points[0].Offset.Milliseconds()
}

// IntervalMS is the bucket width in milliseconds.
func (g *DPSGraph) IntervalMS() int64 { return g.Interval.Milliseconds() }

// Values is the curve as comma-separated whole numbers, for the hover readout
// to index into without needing the points re-encoded as JSON.
func (g *DPSGraph) Values() string {
	parts := make([]string, len(g.Points))
	for i, p := range g.Points {
		parts[i] = strconv.FormatInt(int64(math.Round(p.DPS)), 10)
	}
	return strings.Join(parts, ",")
}
