// Command wowinsight is the shipped binary: the HTTP layer over a Warcraft
// Logs client with real credentials. The development binaries under cmd/dev
// compose the same HTTP layer over other things.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"wowinsight/internal/config"
	"wowinsight/internal/warcraftlogs"
	"wowinsight/internal/web"
)

func main() {
	// SIGTERM is what Cloud Run sends before a revision is replaced, and
	// Go's default disposition for it is to die on the spot. Turning it into
	// a cancelled context is what lets in-flight requests finish.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(".env")
	logger := newLogger(os.Stdout, cfg.Project)
	if err == nil {
		err = run(ctx, cfg, logger)
	}
	if err != nil {
		logger.Error("exiting", "err", err)
		os.Exit(1)
	}
}

// run is main's body with an error return, so that every deferred function
// above it still runs. log.Fatal and a bare os.Exit run none of them, and
// main is the only place that exits.
func run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	// A revision with a mistyped secret name must fail here, once, naming
	// the variable — not start cleanly, pass the probe, and fail every request.
	if err := cfg.Validate(); err != nil {
		return err
	}
	tpl, err := web.ParseTemplates()
	if err != nil {
		return fmt.Errorf("parse templates: %w", err)
	}
	s := web.New(warcraftlogs.New(cfg.ClientID, cfg.ClientSecret), tpl, logger)
	return s.Run(ctx, cfg.Addr())
}
