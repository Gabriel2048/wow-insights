package warcraftlogs

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// DPSPoint is one bucket of the damage graph.
type DPSPoint struct {
	Offset  time.Duration
	DPS     float64
	Percent float64 // position along the fight, 0-100
}

// DPSGraph is a player's damage over the course of a fight, ready to draw.
type DPSGraph struct {
	Points   []DPSPoint
	Peak     float64
	Mean     float64
	Interval time.Duration

	// Line is an SVG polyline in a 0-100 by 0-100 viewBox, and Area closes the
	// same shape to the baseline for filling underneath it.
	Line string
	Area string
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

	// Every series shares a bucket width; the first one defines the grid.
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
		// Positions are assigned later, by Timeline.layout, so the curve shares
		// one x domain with the casts.
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

	graph.Line, graph.Area = plot(graph.Points, graph.Peak)
	return graph
}

// plot renders the curve into a 100x100 viewBox, which the browser stretches to
// whatever width the zoom level gives it.
func plot(points []DPSPoint, peak float64) (line, area string) {
	if len(points) == 0 || peak <= 0 {
		return "", ""
	}
	coords := make([]string, len(points))
	for i, p := range points {
		x := math.Min(100, math.Max(0, p.Percent))
		y := 100 - 100*p.DPS/peak
		coords[i] = fmt.Sprintf("%.3f,%.3f", x, y)
	}
	line = strings.Join(coords, " ")
	area = fmt.Sprintf("M%.3f,100 L%s L%.3f,100 Z",
		math.Min(100, math.Max(0, points[0].Percent)), line,
		math.Min(100, math.Max(0, points[len(points)-1].Percent)))
	return line, area
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
