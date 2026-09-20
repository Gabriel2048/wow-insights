package warcraftlogs

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

// series builds one ability's run of values. They are damage per second, not
// damage: the committed recording settles it — see
// TestTheCurveAgreesWithTheDamageTable.
func series(name string, start, interval float64, values ...float64) struct {
	Name          string    `json:"name"`
	PointStart    float64   `json:"pointStart"`
	PointInterval float64   `json:"pointInterval"`
	Data          []float64 `json:"data"`
} {
	return struct {
		Name          string    `json:"name"`
		PointStart    float64   `json:"pointStart"`
		PointInterval float64   `json:"pointInterval"`
		Data          []float64 `json:"data"`
	}{name, start, interval, values}
}

func TestBuildDPSSumsSeriesAcrossAbilities(t *testing.T) {
	fight := Fight{ID: 1, StartTime: 1000, EndTime: 11000} // 10s
	var r dpsGraphResponse
	// Two abilities on the same 2s grid, as the API returns them.
	r.Data.Series = append(r.Data.Series,
		series("Fireball", 1000, 2000, 100, 200, 300),
		series("Ignite", 1000, 2000, 50, 50, 50),
	)
	g := buildDPS(r, fight)
	if g == nil {
		t.Fatal("expected a graph")
	}
	if len(g.Points) != 3 {
		t.Fatalf("got %d points, want 3", len(g.Points))
	}
	// The values are already per second, so the abilities simply add up.
	// This assertion used to divide by the bucket width and expect 75, 125,
	// 175 — see #71 for why that was wrong about the API rather than about
	// the code.
	want := []float64{150, 250, 350}
	for i, w := range want {
		if math.Abs(g.Points[i].DPS-w) > 0.001 {
			t.Errorf("point %d DPS = %v, want %v", i, g.Points[i].DPS, w)
		}
	}
	if g.Peak != 350 {
		t.Errorf("Peak = %v, want 350", g.Peak)
	}
	if math.Abs(g.Mean-250) > 0.001 {
		t.Errorf("Mean = %v, want 250", g.Mean)
	}
	// The first bucket sits at the pull, the last two follow the interval.
	if g.Points[0].Offset != 0 || g.Points[1].Offset != 2*time.Second {
		t.Errorf("offsets = %v, %v; want 0 and 2s", g.Points[0].Offset, g.Points[1].Offset)
	}
}

func TestBuildDPSAlignsSeriesStartingLate(t *testing.T) {
	fight := Fight{ID: 1, StartTime: 1000, EndTime: 11000}
	var r dpsGraphResponse
	// The second ability is first used two buckets in.
	r.Data.Series = append(r.Data.Series,
		series("Fireball", 1000, 2000, 100, 100, 100, 100),
		series("Fireball", 5000, 2000, 900, 900),
	)
	g := buildDPS(r, fight)
	if len(g.Points) != 4 {
		t.Fatalf("got %d points, want 4", len(g.Points))
	}
	want := []float64{100, 100, 1000, 1000}
	for i, w := range want {
		if math.Abs(g.Points[i].DPS-w) > 0.001 {
			t.Errorf("point %d DPS = %v, want %v (late series must land in the right bucket)", i, g.Points[i].DPS, w)
		}
	}
}

func TestBuildDPSHandlesEmptyAndFlatInput(t *testing.T) {
	fight := Fight{ID: 1, StartTime: 1000, EndTime: 11000}
	if g := buildDPS(dpsGraphResponse{}, fight); g != nil {
		t.Errorf("no series should yield no graph, got %+v", g)
	}
	var zero dpsGraphResponse
	zero.Data.Series = append(zero.Data.Series, series("Fireball", 1000, 2000, 0, 0))
	g := buildDPS(zero, fight)
	if g == nil || g.Peak != 0 {
		t.Fatalf("a flat zero graph should still build, got %+v", g)
	}
	// Whether a zero peak can be drawn is internal/view's problem; see its
	// plot test.
}

// THE ASSERTION THAT SETTLES IT. The damage graph and the damage table
// describe one player's one pull, so their figures have to agree. Holding
// them to each other is worth far more than holding either to a number
// somebody wrote down: it is what says the series are damage per second and
// not damage per bucket, and it is what would have caught #71 the day it
// shipped.
func TestTheCurveAgreesWithTheDamageTable(t *testing.T) {
	report, fight := recordedKill(t)
	timeline := buildTimeline(report, nil, fight, fire)
	if timeline.DPS == nil {
		t.Fatal("the recorded kill has no damage curve")
	}

	// The same player's row in the same pull's damage table.
	raw, err := os.ReadFile("../../testdata/fight-1.json")
	if err != nil {
		t.Fatalf("the recording has no fight-1.json: %v", err)
	}
	var env struct{ Data fightDetailResponse }
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var total float64
	for _, e := range env.Data.ReportData.Report.Damage.Data.Entries {
		if e.ID == recordedFireMage {
			total = e.Total
		}
	}
	if total == 0 {
		t.Fatal("the damage table has no row for the recorded mage")
	}

	table := total / fight.Duration().Seconds()
	got := timeline.DPS.Mean
	if diff := math.Abs(got-table) / table; diff > 0.02 {
		t.Errorf("the curve means %.0f dps and the table says %.0f — %.1f%% apart.\n"+
			"They are the same player's same pull, so they agree or one of them is wrong",
			got, table, 100*diff)
	}
}

// The graph carries a series that is already the sum of the others. Counting
// it doubles every value, and nothing in the payload distinguishes it except
// its name.
func TestTheTotalSeriesIsNotCountedTwice(t *testing.T) {
	var r dpsGraphResponse
	r.Data.Series = append(r.Data.Series,
		series("Fireball", 1000, 2000, 100, 200),
		series("Ignite", 1000, 2000, 50, 50),
		series(totalSeries, 1000, 2000, 150, 250),
	)
	g := buildDPS(r, Fight{ID: 1, StartTime: 1000, EndTime: 5000})
	if g == nil {
		t.Fatal("expected a graph")
	}
	want := []float64{150, 250}
	for i, w := range want {
		if math.Abs(g.Points[i].DPS-w) > 0.001 {
			t.Errorf("point %d = %v, want %v: the Total series was added to the abilities it totals", i, g.Points[i].DPS, w)
		}
	}
}

// A graph with no Total series still sums correctly, in case the API ever
// stops sending one.
func TestAGraphWithoutATotalSeriesStillSums(t *testing.T) {
	var r dpsGraphResponse
	r.Data.Series = append(r.Data.Series,
		series("Fireball", 1000, 2000, 100, 200),
		series("Ignite", 1000, 2000, 50, 50),
	)
	g := buildDPS(r, Fight{ID: 1, StartTime: 1000, EndTime: 5000})
	if g == nil || len(g.Points) != 2 {
		t.Fatal("expected two points")
	}
	if g.Points[0].DPS != 150 || g.Points[1].DPS != 250 {
		t.Errorf("got %v and %v, want 150 and 250", g.Points[0].DPS, g.Points[1].DPS)
	}
}
