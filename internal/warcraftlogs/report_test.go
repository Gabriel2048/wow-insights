package warcraftlogs

import "testing"

func TestParseReportCode(t *testing.T) {
	const code = "ExampleReport123"

	valid := []struct{ name, input string }{
		{"canonical URL", "https://www.warcraftlogs.com/reports/ExampleReport123"},
		{"no www", "https://warcraftlogs.com/reports/ExampleReport123"},
		{"no scheme", "www.warcraftlogs.com/reports/ExampleReport123"},
		{"trailing slash", "https://www.warcraftlogs.com/reports/ExampleReport123/"},
		{"fragment", "https://www.warcraftlogs.com/reports/ExampleReport123#fight=last"},
		{"query", "https://www.warcraftlogs.com/reports/ExampleReport123?fight=3"},
		{"locale prefix", "https://www.warcraftlogs.com/de/reports/ExampleReport123"},
		{"bare code", "ExampleReport123"},
		{"surrounding space", "  https://www.warcraftlogs.com/reports/ExampleReport123  "},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseReportCode(tc.input)
			if err != nil {
				t.Fatalf("ParseReportCode(%q) returned error: %v", tc.input, err)
			}
			if got != code {
				t.Errorf("ParseReportCode(%q) = %q, want %q", tc.input, got, code)
			}
		})
	}

	invalid := []struct{ name, input string }{
		{"empty", ""},
		{"other host", "https://example.com/reports/ExampleReport123"},
		{"lookalike host", "https://notwarcraftlogs.com/reports/ExampleReport123"},
		{"character page", "https://www.warcraftlogs.com/character/eu/silvermoon/someone"},
		{"no code", "https://www.warcraftlogs.com/reports/"},
		{"short code", "https://www.warcraftlogs.com/reports/tooshort"},
		{"not a URL at all", "hello world"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := ParseReportCode(tc.input); err == nil {
				t.Errorf("ParseReportCode(%q) = %q, want an error", tc.input, got)
			}
		})
	}
}

func TestReportCounts(t *testing.T) {
	kill, wipe := true, false
	mythic := 5
	r := Report{
		StartTime: 1_700_000_000_000,
		EndTime:   1_700_000_060_000,
		Fights: []Fight{
			{Name: "Trash"},
			{Name: "Boss", Kill: &kill, Difficulty: &mythic},
			{Name: "Boss", Kill: &wipe, Difficulty: &mythic},
			{Name: "Boss", Kill: &wipe, Difficulty: &mythic},
		},
	}
	if got := r.Kills(); got != 1 {
		t.Errorf("Kills() = %d, want 1", got)
	}
	if got := r.Wipes(); got != 2 {
		t.Errorf("Wipes() = %d, want 2", got)
	}
	if got := len(r.BossFights()); got != 3 {
		t.Errorf("len(BossFights()) = %d, want 3", got)
	}
	if got := r.Duration().Minutes(); got != 1 {
		t.Errorf("Duration() = %v, want 1m", r.Duration())
	}
}
