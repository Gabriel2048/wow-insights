package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"wowinsight/internal/env"
	"wowinsight/internal/fixture"
	"wowinsight/internal/warcraftlogs"
)

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// run is main's body with an error return, so that every deferred function
// above it still runs. log.Fatal and a bare os.Exit run none of them.
func run(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("wowinsight", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", ":8080", "address to listen on")
	fixtureDir := fs.String("fixture", "", "serve a recorded report from this directory instead of Warcraft Logs; needs no credentials")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	tpl, err := parseTemplates()
	if err != nil {
		return fmt.Errorf("parse templates: %w", err)
	}

	var wcl *warcraftlogs.Client
	if *fixtureDir != "" {
		replay, err := fixture.Open(*fixtureDir)
		if err != nil {
			return err
		}
		// The credentials are never sent anywhere: the replay transport mints
		// its own token. They only need to be non-empty for the client to
		// make the request at all.
		wcl = warcraftlogs.New("fixture", "fixture", warcraftlogs.WithHTTPClient(replay.Client()))
		announceFixture(replay, wcl, *addr)
	} else {
		if err := env.Load(".env"); err != nil {
			log.Printf("load .env: %v", err)
		}
		wcl = warcraftlogs.New(
			env.First("WARCRAFTLOGS_CLIENT_ID", "ClientId"),
			env.First("WARCRAFTLOGS_CLIENT_SECRET", "ClientSecret"),
		)
	}

	s := newServer(wcl, tpl, log.Default())
	log.Printf("listening on %s", *addr)
	return http.ListenAndServe(*addr, s.routes())
}

// announceFixture logs every page the recording can serve, so that the mode
// is visible where an agent is already looking — the output of go run — with
// no banner in the page and no branch in the templates. The fight names and
// outcomes come from the recording itself, through the real client.
func announceFixture(replay *fixture.Replay, wcl *warcraftlogs.Client, addr string) {
	base := "http://" + addr
	if strings.HasPrefix(addr, ":") {
		base = "http://localhost" + addr
	}
	log.Printf("fixture mode: serving the recording in %s; Warcraft Logs is not contacted", replay.Dir())
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
