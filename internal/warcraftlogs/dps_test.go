package warcraftlogs

import (
	"math"
	"strings"
	"testing"
	"time"
)

// series builds one ability's run of damage values.
func series(start, interval float64, values ...float64) struct {
	PointStart    float64   `json:"pointStart"`
	PointInterval float64   `json:"pointInterval"`
	Data          []float64 `json:"data"`
} {
	return struct {
		PointStart    float64   `json:"pointStart"`
		PointInterval float64   `json:"pointInterval"`
		Data          []float64 `json:"data"`
	}{start, interval, values}
}

func TestBuildDPSSumsSeriesAcrossAbilities(t *testing.T) {
	fight := Fight{ID: 1, StartTime: 1000, EndTime: 11000} // 10s
	var r dpsGraphResponse
	// Two abilities on the same 2s grid, as the API returns them.
	r.Data.Series = append(r.Data.Series,
		series(1000, 2000, 100, 200, 300),
		series(1000, 2000, 50, 50, 50),
	)
	g := buildDPS(r, fight)
	if g == nil {
		t.Fatal("expected a graph")
	}
	if len(g.Points) != 3 {
		t.Fatalf("got %d points, want 3", len(g.Points))
	}
	// Damage per bucket is 150, 250, 350 over 2s buckets.
	want := []float64{75, 125, 175}
	for i, w := range want {
		if math.Abs(g.Points[i].DPS-w) > 0.001 {
			t.Errorf("point %d DPS = %v, want %v", i, g.Points[i].DPS, w)
		}
	}
	if g.Peak != 175 {
		t.Errorf("Peak = %v, want 175", g.Peak)
	}
	if math.Abs(g.Mean-125) > 0.001 {
		t.Errorf("Mean = %v, want 125", g.Mean)
	}
	// The first bucket sits at the pull, the last two follow the interval.
	if g.Points[0].Offset != 0 || g.Points[1].Offset != 2*time.Second {
		t.Errorf("offsets = %v, %v; want 0 and 2s", g.Points[0].Offset, g.Points[1].Offset)
	}
	// Positions are assigned by Timeline.layout, not here.
}

func TestBuildDPSAlignsSeriesStartingLate(t *testing.T) {
	fight := Fight{ID: 1, StartTime: 1000, EndTime: 11000}
	var r dpsGraphResponse
	// The second ability is first used two buckets in.
	r.Data.Series = append(r.Data.Series,
		series(1000, 2000, 100, 100, 100, 100),
		series(5000, 2000, 900, 900),
	)
	g := buildDPS(r, fight)
	if len(g.Points) != 4 {
		t.Fatalf("got %d points, want 4", len(g.Points))
	}
	want := []float64{50, 50, 500, 500}
	for i, w := range want {
		if math.Abs(g.Points[i].DPS-w) > 0.001 {
			t.Errorf("point %d DPS = %v, want %v (late series must land in the right bucket)", i, g.Points[i].DPS, w)
		}
	}
}

func TestPlotProducesScalableCoordinates(t *testing.T) {
	points := []DPSPoint{
		{Percent: 0, DPS: 0},
		{Percent: 50, DPS: 100},
		{Percent: 100, DPS: 50},
	}
	line, area := plot(points, 100)
	if line != "0.000,100.000 50.000,0.000 100.000,50.000" {
		t.Errorf("line = %q", line)
	}
	// The area must close to the baseline so it can be filled.
	if !strings.HasPrefix(area, "M0.000,100 L") || !strings.HasSuffix(area, "L100.000,100 Z") {
		t.Errorf("area = %q, want a closed path along the baseline", area)
	}
}

func TestBuildDPSHandlesEmptyAndFlatInput(t *testing.T) {
	fight := Fight{ID: 1, StartTime: 1000, EndTime: 11000}
	if g := buildDPS(dpsGraphResponse{}, fight); g != nil {
		t.Errorf("no series should yield no graph, got %+v", g)
	}
	var zero dpsGraphResponse
	zero.Data.Series = append(zero.Data.Series, series(1000, 2000, 0, 0))
	g := buildDPS(zero, fight)
	if g == nil || g.Peak != 0 {
		t.Fatalf("a flat zero graph should still build, got %+v", g)
	}
	if g.Line != "" || g.Area != "" {
		t.Errorf("a zero peak cannot be plotted without dividing by zero: %q", g.Line)
	}
}
