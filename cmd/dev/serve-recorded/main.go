// Command serve-recorded serves the committed recording with no credentials
// at all: the same HTTP layer as the shipped binary, over the same client,
// with the replay transport installed underneath in place of the network.
//
// It exists so that a person or an agent without a .env can open a real fight
// page — laid out by the same code that lays it out in production, since only
// the wire is swapped. Anything the recording does not hold is an error, never
// a live request.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"wowinsight/internal/config"
	"wowinsight/internal/fixture"
	"wowinsight/internal/warcraftlogs"
	"wowinsight/internal/web"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve-recorded", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "testdata", "directory written by cmd/dev/record")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	// Only PORT is wanted; there are no credentials to validate because
	// nothing here talks to Warcraft Logs.
	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}

	replay, err := fixture.Open(*dir)
	if err != nil {
		return err
	}
	tpl, err := web.ParseTemplates()
	if err != nil {
		return fmt.Errorf("parse templates: %w", err)
	}
	// The credentials are never sent anywhere: the replay transport mints its
	// own token. They only need to be non-empty for the client to make the
	// request at all.
	wcl := warcraftlogs.New("recorded", "recorded", warcraftlogs.WithHTTPClient(replay.Client()))
	announce(replay, wcl, cfg.Addr())

	return web.New(wcl, tpl, log.Default()).Run(ctx, cfg.Addr())
}

// announce logs every page the recording can serve, so that what is being
// served is visible where a developer is already looking — the output of go
// run — with no banner in the page and no branch in the templates. The fight
// names and outcomes come from the recording itself, through the real client.
func announce(replay *fixture.Replay, wcl *warcraftlogs.Client, addr string) {
	base := "http://" + addr
	if strings.HasPrefix(addr, ":") {
		base = "http://localhost" + addr
	}
	log.Printf("serving the recording in %s; Warcraft Logs is not contacted", replay.Dir())
	log.Printf("  %s/?url=%s", base, replay.Code())

	report, err := wcl.Report(context.Background(), replay.Code())
	if err != nil {
		log.Printf("  (could not read the recorded report: %v)", err)
		return
	}
	for _, f := range replay.Fights() {
		label := fmt.Sprintf("fight %d", f.ID)
		for _, fight := range report.Fights {
			if fight.ID == f.ID {
				label = fmt.Sprintf("%s, %s %s", fight.Name, fight.DifficultyName(), fight.Outcome())
			}
		}
		log.Printf("  %s/report/%s/fight/%d  (%s)", base, replay.Code(), f.ID, label)
		for _, player := range f.Players {
			log.Printf("  %s/report/%s/fight/%d?player=%d", base, replay.Code(), f.ID, player)
		}
	}
}
