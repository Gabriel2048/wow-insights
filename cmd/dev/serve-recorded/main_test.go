package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// A bad directory must fail at startup, not on the first request.
func TestRunRejectsAMissingRecording(t *testing.T) {
	err := run(context.Background(), []string{"-dir", filepath.Join(t.TempDir(), "nope")}, io.Discard, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "cmd/dev/record") {
		t.Errorf("error = %v, want one pointing at the recorder", err)
	}
}
