// Command record captures one fight from a real Warcraft Logs report as a
// redacted fixture directory, which `go run ./cmd/dev/serve-recorded` then serves with
// no credentials at all.
//
// It is run by a human with a .env, once per fight worth keeping:
//
//	go run ./cmd/dev/record -report <URL or code> -fight 12 -players <name>,<name>
//
// It drives the real client through a recording transport, so what lands on
// disk is exactly what the client asked for — the cast pagination included —
// and nothing here knows a query by name.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"wowinsight/internal/config"
	"wowinsight/internal/fixture"
	"wowinsight/internal/knowledge"
	"wowinsight/internal/warcraftlogs"
)

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}

func run(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	fs.SetOutput(stderr)
	reportRef := fs.String("report", "", "report URL or code (required)")
	fightID := fs.Int("fight", 0, "fight id within the report (required)")
	players := fs.String("players", "", "comma-separated character names or actor ids to record timelines for")
	out := fs.String("out", "testdata", "directory to write; one directory holds one report")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *reportRef == "" || *fightID == 0 {
		fs.Usage()
		return errors.New("record: -report and -fight are required")
	}
	code, err := warcraftlogs.ParseReportCode(*reportRef)
	if err != nil {
		return err
	}

	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("record: %w (recording needs the real API)", err)
	}

	ctx := context.Background()
	recorder := fixture.NewRecorder()
	wcl := warcraftlogs.New(cfg.ClientID, cfg.ClientSecret, warcraftlogs.WithHTTPClient(recorder.Client()))

	limit, err := wcl.RateLimit(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "points spent this hour: %.0f of %d\n", limit.PointsSpentThisHour, limit.LimitPerHour)

	report, err := wcl.Report(ctx, code)
	if err != nil {
		return err
	}
	detail, err := wcl.FightDetail(ctx, code, *fightID)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "fight %d: %s (%s %s, %s)\n", *fightID, detail.Fight.Name,
		detail.Fight.DifficultyName(), detail.Fight.Outcome(), report.Title)

	if *players == "" {
		fmt.Fprintln(stderr, "\nno -players given, so no timeline was recorded and nothing was written. The roster is:")
		for _, p := range detail.Players {
			fmt.Fprintf(stderr, "  %4d  %-14s %s\n", p.ActorID, p.Name, p.Title())
		}
		return errors.New("record: re-run with -players naming who to record")
	}
	for ref := range strings.SplitSeq(*players, ",") {
		player, err := resolve(detail, strings.TrimSpace(ref))
		if err != nil {
			return err
		}
		// The same lookup the page makes, so the recording holds exactly the
		// streams the page will ask for.
		know, known := knowledge.Lookup(player.SpecID())
		if _, err := wcl.Timeline(ctx, code, detail.Fight, player.ActorID, know); err != nil {
			return fmt.Errorf("timeline for %s: %w", player.Name, err)
		}
		note := ""
		if !known {
			note = " — no knowledge for this spec, so no procs or cooldowns were asked for"
		}
		fmt.Fprintf(stderr, "recorded the timeline of actor %d (%s)%s\n", player.ActorID, player.Title(), note)
	}

	if err := recorder.Write(*out); err != nil {
		return err
	}
	names, _ := filepath.Glob(filepath.Join(*out, "*.json"))
	fmt.Fprintf(stderr, "\nwrote %s:\n", *out)
	for _, name := range names {
		info, err := os.Stat(name)
		if err != nil {
			return err
		}
		fmt.Fprintf(stderr, "  %8d  %s\n", info.Size(), filepath.Base(name))
	}
	fmt.Fprintf(stderr, "\nnow: go run ./cmd/dev/serve-recorded -dir %s\n", *out)
	return nil
}

// resolve finds one player in the fight by actor id or, case-insensitively,
// by name — the name is what a human knows.
func resolve(detail *warcraftlogs.FightDetail, ref string) (warcraftlogs.PlayerStats, error) {
	if id, err := strconv.Atoi(ref); err == nil {
		if p, ok := detail.Player(id); ok {
			return p, nil
		}
		return warcraftlogs.PlayerStats{}, fmt.Errorf("record: no actor %d in fight %d", id, detail.Fight.ID)
	}
	for _, p := range detail.Players {
		if strings.EqualFold(p.Name, ref) {
			return p, nil
		}
	}
	return warcraftlogs.PlayerStats{}, fmt.Errorf("record: no player named %q in fight %d (run without -players to list the roster)", ref, detail.Fight.ID)
}
