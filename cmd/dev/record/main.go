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
	cohort := fs.String("cohort", "", "one of -players to also record both comparisons for: the best at their item level and the top performers")
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
	wcl := warcraftlogs.New(cfg.ClientID, cfg.ClientSecret, warcraftlogs.WithTransport(recorder))

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
	timelines := map[int]*warcraftlogs.Timeline{}
	for ref := range strings.SplitSeq(*players, ",") {
		player, err := resolve(detail, strings.TrimSpace(ref))
		if err != nil {
			return err
		}
		// The same lookup the page makes, so the recording holds exactly the
		// streams the page will ask for.
		know, known := knowledge.Lookup(player.SpecID())
		t, err := wcl.Timeline(ctx, code, detail.Fight, player.ActorID, know)
		if err != nil {
			return fmt.Errorf("timeline for %s: %w", player.Name, err)
		}
		timelines[player.ActorID] = t
		note := ""
		if !known {
			note = " — no knowledge for this spec, so no procs or cooldowns were asked for"
		}
		fmt.Fprintf(stderr, "recorded the timeline of actor %d (%s)%s\n", player.ActorID, player.Title(), note)
	}

	// The comparisons are recorded in this same run on purpose: their peers
	// and this fight's roster then get their pseudonyms from one redactor, so
	// the player the page excludes from their own cohort is recognisably them.
	// CohortQueries is what the page asks with, so the recording holds exactly
	// what the offline page will look for.
	if *cohort != "" {
		player, err := resolve(detail, strings.TrimSpace(*cohort))
		if err != nil {
			return err
		}
		t, ok := timelines[player.ActorID]
		if !ok {
			return errors.New("record: -cohort names a player -players did not record")
		}
		sameItemLevel, top := warcraftlogs.CohortQueries(code, detail.Fight, player, t)
		for _, q := range []warcraftlogs.CohortQuery{sameItemLevel, top} {
			if q.Kind == warcraftlogs.SameItemLevel && q.Bracket == 0 {
				fmt.Fprintln(stderr, "no item-level comparison: this pull has no ranking to find the bracket from")
				continue
			}
			c, err := wcl.Cohort(ctx, q)
			if err != nil {
				return fmt.Errorf("comparison: %w", err)
			}
			// Counts only. The peers' names went out in requests and came
			// back in responses the redactor is about to rewrite; they are
			// not printed here either.
			fmt.Fprintf(stderr, "recorded a comparison: %d of %d peers read, %d skipped\n", len(c.Peers), c.Asked, c.Skipped)
		}
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
