// Package config is what the process is told from outside: the port to listen
// on and the Warcraft Logs credentials. It reads a .env file for local
// development and the real environment for everything else, and it never
// writes to the environment — a secret loaded here stays out of
// /proc/self/environ and out of every child process this one starts.
package config

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// The names, one per value. Warcraft Logs' own client page labels the fields
// "Client ID" and "Client Secret"; those spellings were once accepted as
// aliases and are not any more, so that a deployment has exactly one name to
// get right.
const (
	PortVar         = "PORT"
	ClientIDVar     = "WARCRAFTLOGS_CLIENT_ID"
	ClientSecretVar = "WARCRAFTLOGS_CLIENT_SECRET"
	ProjectVar      = "GOOGLE_CLOUD_PROJECT"

	defaultPort = "8080"
)

// Config is everything the binaries take from outside.
type Config struct {
	// Port is what to listen on. Cloud Run sets PORT; 8080 otherwise.
	Port string
	// ClientID and ClientSecret are the Warcraft Logs OAuth credentials.
	ClientID     string
	ClientSecret string
	// Project is the Google Cloud project, used only to spell trace ids the
	// way Cloud Logging groups them. Optional; nothing fails without it.
	Project string
}

// Addr is the listen address for Port.
func (c Config) Addr() string { return ":" + c.Port }

// MissingError names every required variable that was not set, so that a
// misconfigured deployment fails once with the whole list rather than once
// per variable.
type MissingError struct {
	Names []string
}

func (e *MissingError) Error() string {
	return "config: missing " + strings.Join(e.Names, ", ")
}

// Validate reports every credential that is missing. Port always has a value.
func (c Config) Validate() error {
	var missing []string
	if c.ClientID == "" {
		missing = append(missing, ClientIDVar)
	}
	if c.ClientSecret == "" {
		missing = append(missing, ClientSecretVar)
	}
	if len(missing) > 0 {
		return &MissingError{Names: missing}
	}
	return nil
}

// Load reads the .env file at path, if there is one, and the process
// environment, and builds a Config with the environment winning: a real export
// overrides the file, so a deployment that sets variables is never silently
// overridden by a stray file, and a developer can override one value without
// editing .env. A missing file is not an error; a developer who has not made
// one yet should reach Validate's message, not an open() failure.
func Load(path string) (Config, error) {
	file := map[string]string{}
	f, err := os.Open(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, err
	}
	if err == nil {
		defer f.Close()
		if file, err = parse(f); err != nil {
			return Config{}, fmt.Errorf("config: %s: %w", path, err)
		}
	}
	get := func(name string) string {
		return cmp.Or(strings.TrimSpace(os.Getenv(name)), file[name])
	}
	return Config{
		Port:         cmp.Or(get(PortVar), defaultPort),
		ClientID:     get(ClientIDVar),
		ClientSecret: get(ClientSecretVar),
		Project:      get(ProjectVar),
	}, nil
}

// parse reads KEY=VALUE lines. Blank lines and # comments are skipped, keys
// and values are trimmed, and a value wrapped in one matching pair of quotes
// loses that pair — only that pair, so a secret that happens to end in a
// quote character survives intact.
func parse(r io.Reader) (map[string]string, error) {
	values := map[string]string{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = unquote(strings.TrimSpace(value))
	}
	return values, scanner.Err()
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
