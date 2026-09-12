package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"testing"
)

// A bad directory must fail at startup, not on the first request.
func TestRunRejectsAMissingRecording(t *testing.T) {
	err := run(context.Background(), []string{"-dir", filepath.Join(t.TempDir(), "nope")}, io.Discard, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want the missing directory", err)
	}
}
