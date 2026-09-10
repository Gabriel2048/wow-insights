package main

import (
	"fmt"
	"html/template"
	"time"
)

// templateFuncs returns the function map the templates are parsed with. It is a
// function rather than a package-level var so that each parse gets its own map:
// a shared template.FuncMap is mutable state anything in the package could
// write to.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"duration": formatDuration,
		"compact":  formatCompact,
		"short":    formatShort,
		"datetime": func(t time.Time) string { return t.Format("2 Jan 2006, 15:04 MST") },
	}
}

// formatDuration renders a duration as "2h 14m" or "3m 42s", dropping the
// precision nobody reads.
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if h := int(d.Hours()); h > 0 {
		return fmt.Sprintf("%dh %02dm", h, int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// formatShort renders the small durations of a cast timeline: "2.4s" for
// anything under a minute, "1m 04s" above it.
func formatShort(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	d = d.Round(time.Second)
	return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// formatCompact abbreviates large numbers the way damage meters do: 12.3m
// rather than 12345678.
func formatCompact(v float64) string {
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%.2fb", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%.2fm", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.1fk", v/1e3)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}
