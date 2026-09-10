package env

import (
	"os"
	"path/filepath"
	"testing"
)

// A real export must win over the file, so a developer can override one value
// without editing .env — and so a deployment that sets real environment
// variables is never silently overridden by a stray file.
func TestLoadDoesNotOverrideTheEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("WOWINSIGHT_TEST_A=fromfile\nWOWINSIGHT_TEST_B=fromfile\n"), 0o600); err != nil {
		t.Fatalf("write temp .env: %v", err)
	}
	t.Setenv("WOWINSIGHT_TEST_A", "fromenv")

	if err := Load(path); err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if got := os.Getenv("WOWINSIGHT_TEST_A"); got != "fromenv" {
		t.Errorf("WOWINSIGHT_TEST_A = %q, want fromenv (an existing variable must win)", got)
	}
	if got := os.Getenv("WOWINSIGHT_TEST_B"); got != "fromfile" {
		t.Errorf("WOWINSIGHT_TEST_B = %q, want fromfile", got)
	}
	os.Unsetenv("WOWINSIGHT_TEST_B")
}

// A developer who has not made a .env yet should get the empty-credentials
// path, not a startup failure.
func TestLoadIgnoresAMissingFile(t *testing.T) {
	if err := Load(filepath.Join(t.TempDir(), "nope.env")); err != nil {
		t.Errorf("Load() on a missing file returned %v, want nil", err)
	}
}

// Comments, blanks and quoted values all appear in a real .env.
func TestLoadSkipsCommentsAndTrimsQuotes(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	body := "# a comment\n\n  WOWINSIGHT_TEST_C = \"quoted\"  \nnotakeyvalue\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp .env: %v", err)
	}
	if err := Load(path); err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if got := os.Getenv("WOWINSIGHT_TEST_C"); got != "quoted" {
		t.Errorf("WOWINSIGHT_TEST_C = %q, want quoted", got)
	}
	os.Unsetenv("WOWINSIGHT_TEST_C")
}

// Warcraft Logs' own client page labels the fields "Client ID" and "Client
// Secret", so both spellings are accepted and the conventional one wins.
func TestFirstPrefersTheEarlierName(t *testing.T) {
	t.Setenv("WOWINSIGHT_TEST_PRIMARY", "")
	t.Setenv("WOWINSIGHT_TEST_FALLBACK", "fallback")
	if got := First("WOWINSIGHT_TEST_PRIMARY", "WOWINSIGHT_TEST_FALLBACK"); got != "fallback" {
		t.Errorf("First() = %q, want fallback (an empty value must not count as set)", got)
	}

	t.Setenv("WOWINSIGHT_TEST_PRIMARY", "primary")
	if got := First("WOWINSIGHT_TEST_PRIMARY", "WOWINSIGHT_TEST_FALLBACK"); got != "primary" {
		t.Errorf("First() = %q, want primary", got)
	}

	if got := First("WOWINSIGHT_TEST_ABSENT"); got != "" {
		t.Errorf("First() = %q, want empty when nothing is set", got)
	}
}
