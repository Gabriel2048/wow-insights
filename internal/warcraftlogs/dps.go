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
			PointStart    float64   `json:"pointStart"`
			PointInterval float64   `json:"pointInterval"`
			Data          []float64 `json:"data"`
		} `json:"series"`
	} `json:"data"`
}

// buildDPS sums the per-ability series into a single damage-per-second curve.
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
		if s.PointInterval != interval {
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

	seconds := interval / 1000
	graph := &DPSGraph{Interval: time.Duration(interval) * time.Millisecond}

	var sum float64
	for i := 0; i <= last; i++ {
		dps := totals[i] / seconds
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
