package main

import (
	"bufio"
	"os"
	"strings"
)

// loadEnvFile reads simple KEY=VALUE lines from path into the process
// environment. Existing environment variables win, so a real export always
// overrides the file. A missing file is not an error.
func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
	return scanner.Err()
}

// firstEnv returns the value of the first name that is set to a non-empty
// value. Warcraft Logs' own client page labels the fields "Client ID" and
// "Client Secret", so those spellings are accepted alongside the conventional
// WARCRAFTLOGS_* names.
func firstEnv(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}
