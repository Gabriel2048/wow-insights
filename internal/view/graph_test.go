package view

import (
	"strings"
	"testing"

	"wowinsight/internal/warcraftlogs"
)

func TestPlotProducesScalableCoordinates(t *testing.T) {
	points := []warcraftlogs.DPSPoint{{DPS: 0}, {DPS: 100}, {DPS: 50}}
	line, area := plot(points, []float64{0, 50, 100}, 100)
	if line != "0.000,100.000 50.000,0.000 100.000,50.000" {
		t.Errorf("line = %q", line)
	}
	// The area must close to the baseline so it can be filled.
	if !strings.HasPrefix(area, "M0.000,100 L") || !strings.HasSuffix(area, "L100.000,100 Z") {
		t.Errorf("area = %q, want a closed path along the baseline", area)
	}
}

// A zero peak cannot be plotted without dividing by zero.
func TestPlotOfAFlatZeroGraphIsEmpty(t *testing.T) {
	line, area := plot([]warcraftlogs.DPSPoint{{DPS: 0}, {DPS: 0}}, []float64{0, 100}, 0)
	if line != "" || area != "" {
		t.Errorf("line=%q area=%q, want nothing", line, area)
	}
}
