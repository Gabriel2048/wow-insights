package web

import (
	"testing"
	"time"
)

// The three formatters each switch at a boundary, and the boundaries are what a
// change would move: a fight is minutes, a cast is seconds, and damage is in
// the millions.
func TestFormatDurationSwitchesAtAnHour(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0m 00s"},
		{42 * time.Second, "0m 42s"},
		{3*time.Minute + 42*time.Second, "3m 42s"},
		{59*time.Minute + 59*time.Second, "59m 59s"},
		{time.Hour, "1h 00m"},
		{2*time.Hour + 14*time.Minute, "2h 14m"},
	}
	for _, c := range cases {
		if got := formatDuration(c.in); got != c.want {
			t.Errorf("formatDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatShortSwitchesAtAMinute(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0.0s"},
		{1500 * time.Millisecond, "1.5s"},
		{59900 * time.Millisecond, "59.9s"},
		{time.Minute, "1m 00s"},
		{64 * time.Second, "1m 04s"},
	}
	for _, c := range cases {
		if got := formatShort(c.in); got != c.want {
			t.Errorf("formatShort(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatCompactSwitchesAtEachThousand(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0k"},
		{12345, "12.3k"},
		{999999, "1000.0k"},
		{1e6, "1.00m"},
		{12345678, "12.35m"},
		{1e9, "1.00b"},
	}
	for _, c := range cases {
		if got := formatCompact(c.in); got != c.want {
			t.Errorf("formatCompact(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
