package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parse is pure, which is the point of returning a map rather than calling
// os.Setenv: these run in parallel with nothing else and touch no process
// state.
func TestParseReadsWhatARealDotEnvContains(t *testing.T) {
	t.Parallel()
	body := `# a comment

  KEY_A = "quoted"  
KEY_B='single'
KEY_C=plain
notakeyvalue
KEY_D=has=equals
`
	values, err := parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse() returned error: %v", err)
	}
	want := map[string]string{"KEY_A": "quoted", "KEY_B": "single", "KEY_C": "plain", "KEY_D": "has=equals"}
	for k, v := range want {
		if values[k] != v {
			t.Errorf("%s = %q, want %q", k, values[k], v)
		}
	}
	if _, ok := values["notakeyvalue"]; ok {
		t.Error("a line with no = was taken as a key")
	}
}

// The old strings.Trim(value, `"'`) removed every leading and trailing quote
// character, so a secret ending in one was silently shortened. Only a matched
// wrapping pair comes off.
func TestUnquoteRemovesOneMatchedPairOnly(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		`"abc"`:   "abc",
		`'abc'`:   "abc",
		`abc'`:    "abc'",  // a trailing quote is part of the value
		`"abc'`:   `"abc'`, // mismatched: not a pair
		`""abc""`: `"abc"`, // one pair, not every quote
		`"`:       `"`,     // too short to be a pair
		``:        ``,
	} {
		if got := unquote(in); got != want {
			t.Errorf("unquote(%q) = %q, want %q", in, got, want)
		}
	}
}

// A real export must win over the file, so a developer can override one value
// without editing .env — and so a deployment that sets real environment
// variables is never silently overridden by a stray file.
func TestLoadLetsTheEnvironmentWin(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(ClientIDVar+"=fromfile\n"+ClientSecretVar+"=fromfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ClientIDVar, "fromenv")
	t.Setenv(ClientSecretVar, "")
	t.Setenv(PortVar, "")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.ClientID != "fromenv" {
		t.Errorf("ClientID = %q, want fromenv (an exported variable must win)", cfg.ClientID)
	}
	if cfg.ClientSecret != "fromfile" {
		t.Errorf("ClientSecret = %q, want fromfile (an empty export does not count as set)", cfg.ClientSecret)
	}
	if cfg.Port != defaultPort {
		t.Errorf("Port = %q, want the default %s", cfg.Port, defaultPort)
	}
}

// Loading must not put the secret into the process environment.
func TestLoadDoesNotTouchTheEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(ClientSecretVar+"=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ClientSecretVar, "")
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(ClientSecretVar); got != "" {
		t.Errorf("%s = %q in the environment after Load; it must stay in the returned Config only", ClientSecretVar, got)
	}
}

func TestLoadIgnoresAMissingFile(t *testing.T) {
	t.Setenv(PortVar, "9999")
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.env"))
	if err != nil {
		t.Fatalf("Load() on a missing file returned %v, want nil", err)
	}
	if cfg.Port != "9999" || cfg.Addr() != ":9999" {
		t.Errorf("Port = %q Addr = %q, want PORT from the environment", cfg.Port, cfg.Addr())
	}
}

// A deployment with a typo in a secret's name must fail at startup, naming
// what is missing — all of it at once.
func TestValidateNamesEveryMissingVariable(t *testing.T) {
	t.Parallel()
	var missing *MissingError
	err := Config{}.Validate()
	if !errors.As(err, &missing) {
		t.Fatalf("Validate() on an empty Config returned %v, want a *MissingError", err)
	}
	if len(missing.Names) != 2 || missing.Names[0] != ClientIDVar || missing.Names[1] != ClientSecretVar {
		t.Errorf("Names = %v, want both credentials", missing.Names)
	}
	if !strings.Contains(err.Error(), ClientIDVar) || !strings.Contains(err.Error(), ClientSecretVar) {
		t.Errorf("error = %q, want it to name both variables", err)
	}

	if err := (Config{ClientID: "id"}).Validate(); err == nil || strings.Contains(err.Error(), ClientIDVar) {
		t.Errorf("with only the secret missing, error = %v, want only %s named", err, ClientSecretVar)
	}
	if err := (Config{ClientID: "id", ClientSecret: "s"}).Validate(); err != nil {
		t.Errorf("Validate() with both set returned %v", err)
	}
}
