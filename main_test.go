package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"wowinsight/internal/config"
)

// The acceptance criterion: no credentials is a startup failure that names
// what is missing, never a server that starts and fails every request.
func TestRunRefusesToStartWithoutCredentials(t *testing.T) {
	var missing *config.MissingError
	err := run(context.Background(), config.Config{Port: "0"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.As(err, &missing) {
		t.Fatalf("run() with no credentials returned %v, want a *config.MissingError", err)
	}
	if len(missing.Names) != 2 {
		t.Errorf("Names = %v, want both credentials named", missing.Names)
	}
}
