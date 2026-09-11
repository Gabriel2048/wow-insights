package view

import (
	"fmt"
	"math"
	"strings"

	"wowinsight/internal/warcraftlogs"
)

// graph draws a damage curve against the axis. Nil in, nil out: a player
// with no damage series has no curve.
func (v *Timeline) graph(g *warcraftlogs.DPSGraph) *Graph {
	if g == nil {
		return nil
	}
	xs := make([]float64, len(g.Points))
	for i, p := range g.Points {
		xs[i] = v.percent(p.Offset)
	}
	line, area := plot(g.Points, xs, g.Peak)
	return &Graph{DPSGraph: g, Line: line, Area: area}
}

// plot renders the curve into a 100x100 viewBox, which the browser stretches to
// the timeline's width and the lane's height. Each point's x is its position
// along the drawn timeline and y is its DPS as a share of the peak, inverted
// because SVG's origin is the top-left corner.
func plot(points []warcraftlogs.DPSPoint, xs []float64, peak float64) (line, area string) {
	if len(points) == 0 || peak <= 0 {
		return "", ""
	}
	var b strings.Builder
	for i, p := range points {
		if i > 0 {
			b.WriteByte(' ')
		}
		x := math.Min(100, math.Max(0, xs[i]))
		y := 100 - 100*p.DPS/peak
		fmt.Fprintf(&b, "%.3f,%.3f", x, y)
	}
	line = b.String()
	area = fmt.Sprintf("M%.3f,100 L%s L%.3f,100 Z",
		math.Min(100, math.Max(0, xs[0])), line,
		math.Min(100, math.Max(0, xs[len(points)-1])))
	return line, area
}
