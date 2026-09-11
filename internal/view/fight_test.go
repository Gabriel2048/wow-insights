package view

import (
	"testing"

	"wowinsight/internal/warcraftlogs"
)

func TestWowheadDifficulty(t *testing.T) {
	id := func(v int) *int { return &v }
	cases := []struct {
		name       string
		difficulty *int
		want       int
	}{
		{"LFR", id(1), 17},
		{"Normal", id(3), 14},
		{"Heroic", id(4), 15},
		{"Mythic", id(5), 16},
		{"trash has no difficulty", nil, 0},
		{"unknown difficulty", id(99), 0},
	}
	for _, c := range cases {
		got := Fight{warcraftlogs.Fight{Difficulty: c.difficulty}}.WowheadDifficulty()
		if got != c.want {
			t.Errorf("%s: WowheadDifficulty() = %d, want %d", c.name, got, c.want)
		}
	}
}
