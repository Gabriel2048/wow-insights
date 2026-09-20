// Command measure-insight times the model that words the findings, against a
// real pull, so the numbers the job machinery is sized on are measured rather
// than guessed.
//
// Issue #61 asks for this before anything else is built, and it is the one
// part of that issue nobody without a key can do: how long a run may think,
// what the page shows while it does, and where a spend ceiling goes are all
// downstream of a p50 and a p95 that only a real call produces.
//
//	go run ./cmd/dev/measure-insight -report <url or code> -fight 1 -player 21 -n 5
//
// It needs the Warcraft Logs credentials and ANTHROPIC_API_KEY in .env, and
// **it spends both budgets**: one Warcraft Logs fight fetch plus one timeline,
// and n calls to the model. That is why it is a command you run on purpose
// and not a test.
//
// It deliberately does not *record* the exchange, which is what #61 sketched
// as cmd/dev/record-insight. A recording would exist to be replayed by
// cmd/dev/serve-recorded, and both committed recordings produce zero findings
// — so there would be nothing for the replayed model to have worded, and the
// fixture would be unreachable. The transport seam that a recorder would hang
// on is built and tested (coach.WithTransport); the recorder is worth writing
// the day there is a recorded pull with something wrong in it.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"time"

	"wowinsight/internal/coach"
	"wowinsight/internal/config"
	"wowinsight/internal/knowledge"
	"wowinsight/internal/warcraftlogs"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "measure-insight:", err)
		os.Exit(1)
	}
}

func run() error {
	report := flag.String("report", "", "a Warcraft Logs report URL or code")
	fightID := flag.Int("fight", 0, "the fight id")
	actorID := flag.Int("player", 0, "the actor id of the player to analyse")
	runs := flag.Int("n", 5, "how many times to call the model; 0 lists the findings and spends nothing on it")
	sheet := flag.Bool("sheet", false, "print everything the model would be told about this pull, and send nothing")
	flag.Parse()

	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if cfg.AnthropicKey == "" {
		return fmt.Errorf("no %s in .env; there is nothing to measure without one", config.AnthropicKeyVar)
	}
	code, err := warcraftlogs.ParseReportCode(*report)
	if err != nil {
		return fmt.Errorf("-report: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client := warcraftlogs.New(cfg.ClientID, cfg.ClientSecret)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	in, err := pull(ctx, client, code, *fightID, *actorID)
	if err != nil {
		return err
	}

	fmt.Printf("\n%s — %s, %s, %s\n", in.Detail.Fight.Name, in.Detail.Fight.Outcome(),
		in.Timeline.Duration.Round(time.Second), in.Know.Spec.Spec+" "+in.Know.Spec.Class)
	fmt.Printf("findings: %d\n", len(in.Findings))
	if *sheet {
		// Printed before the findings gate below, because "what would the
		// model have been told" is most worth asking about a pull where it
		// was never asked anything.
		body, err := coach.Facts(in)
		if err != nil {
			return err
		}
		fmt.Printf("\nthe fact sheet, %d bytes on the wire:\n%s\n", len(body), body)
	}
	if len(in.Findings) == 0 {
		// Not a failure, and worth saying plainly: the model is never called
		// for a pull with nothing to say about it, so there is nothing here
		// to time. Pick a pull where something went wrong.
		fmt.Println("\nNothing was found on this pull, so the model is never asked and there is")
		fmt.Println("nothing to measure. Try a pull where a cooldown went unused at the end.")
		return nil
	}
	for _, f := range in.Findings {
		fmt.Printf("  [%s] %s %s\n", f.Severity, f.Timestamp(), f.Title)
	}

	writer := coach.New(cfg.AnthropicKey, logger)
	var took []time.Duration
	for i := range *runs {
		started := time.Now()
		out, err := writer.Write(ctx, in)
		elapsed := time.Since(started)
		took = append(took, elapsed)
		if err != nil {
			fmt.Printf("\nrun %d in %s — FAILED: %v\n", i+1, elapsed.Round(time.Millisecond), err)
			continue
		}
		fmt.Printf("\nrun %d in %s\n", i+1, elapsed.Round(time.Millisecond))
		for _, w := range out {
			mark := " "
			if w.SetAside {
				mark = "~"
			}
			fmt.Printf("  %s %s %s\n      %s\n", mark, w.Timestamp(), w.Title, w.Detail)
			if w.Why != "" {
				fmt.Printf("      set aside: %s\n", w.Why)
			}
		}
	}

	if len(took) == 0 {
		// -n 0 asks what a pull contains without spending anything on the
		// model, which is how you find a pull worth measuring in the first
		// place. Without this the summary below indexes an empty slice.
		return nil
	}
	slices.Sort(took)
	fmt.Printf("\n%d runs: min %s  p50 %s  p95 %s  max %s\n", len(took),
		took[0].Round(time.Millisecond), percentile(took, 0.50).Round(time.Millisecond),
		percentile(took, 0.95).Round(time.Millisecond), took[len(took)-1].Round(time.Millisecond))
	fmt.Printf("jobDeadline in internal/web is %s; the page polls while it thinks.\n", 10*time.Minute)
	return nil
}

// pull fetches everything one analysis needs, the same way the background job
// does.
func pull(ctx context.Context, client *warcraftlogs.Client, code string, fightID, actorID int) (coach.Input, error) {
	detail, err := client.FightDetail(ctx, code, fightID)
	if err != nil {
		return coach.Input{}, err
	}
	player, ok := detail.Player(actorID)
	if !ok {
		var ids []int
		for _, p := range detail.Players {
			ids = append(ids, p.ActorID)
		}
		return coach.Input{}, fmt.Errorf("actor %d is not in fight %d; the fight has %v", actorID, fightID, ids)
	}
	know, _ := knowledge.Lookup(player.SpecID())
	timeline, err := client.Timeline(ctx, code, detail.Fight, actorID, know)
	if err != nil {
		return coach.Input{}, err
	}
	return coach.Input{
		Detail:   detail,
		Player:   player,
		Timeline: timeline,
		Know:     know,
		Findings: warcraftlogs.Findings(timeline, know, player.ActedUntil()),
	}, nil
}

// percentile reads the pth value out of a sorted slice, nearest-rank. With
// five samples this is a coarse instrument and says so: it is here to size a
// timeout, not to publish a service level.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)))
	return sorted[min(i, len(sorted)-1)]
}
