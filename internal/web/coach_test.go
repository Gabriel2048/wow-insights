package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"wowinsight/internal/coach"
	"wowinsight/internal/knowledge"
	"wowinsight/internal/warcraftlogs"
)

// fakeCoach stands in for the model. It is the consumer-declared writer
// interface and nothing more, which is the point of declaring it here.
type fakeCoach struct {
	called int
	out    coach.Findings
	err    error
}

func (f *fakeCoach) Write(_ context.Context, in coach.Input) (coach.Findings, error) {
	f.called++
	if f.err != nil {
		// Write's contract: the deterministic wording comes back alongside
		// every error, so a caller never has to choose between the findings
		// and the failure.
		return coach.Deterministic(in.Findings), f.err
	}
	return f.out, nil
}

// A model that fails must not cost the page its findings. They were computed
// before it was asked and are true whatever it did.
func TestTheFindingsSurviveAModelThatFails(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		says string
	}{
		{"an outage", coach.ErrModelUnavailable, "was not available"},
		{"a rate limit", coach.ErrModelBusy, "was not available"},
		{"a bad key", coach.ErrBadKey, "missing or rejected"},
		{"an empty account", coach.ErrNoCredit, "out of credit"},
		{"prose that did not check out", coach.ErrUntrustworthy, "did not match the evidence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &fakeCoach{err: tc.err}
			out := analyseWith(t, model)

			if model.called != 1 {
				t.Fatalf("the model was asked %d times, want 1", model.called)
			}
			if len(out.findings) == 0 {
				t.Fatal("the findings were lost; they do not depend on the model")
			}
			if len(out.findings.Standing()) == 0 {
				t.Error("nothing is standing, so the page would say the pull was clean")
			}
			notice := strings.Join(out.notices, " ")
			if !strings.Contains(notice, tc.says) {
				t.Errorf("notices = %q, want one saying %q", out.notices, tc.says)
			}
			// The rule every failure on this page is held to: never blame
			// Warcraft Logs for something Warcraft Logs did not do.
			if strings.Contains(notice, "Warcraft Logs") {
				t.Errorf("a failure of the writing model is reported as Warcraft Logs failing: %q", notice)
			}
		})
	}
}

// Every coach sentinel has its own row. Without one they fall through to the
// bottom of classify, which names the wrong vendor.
func TestEveryModelFailureHasItsOwnSentence(t *testing.T) {
	for _, err := range []error{
		coach.ErrModelUnavailable, coach.ErrModelBusy, coach.ErrBadKey, coach.ErrNoCredit,
		coach.ErrDeclined, coach.ErrUntrustworthy, coach.ErrWouldLeak,
	} {
		p := classify(err)
		if strings.Contains(p.message, "Warcraft Logs") {
			t.Errorf("%v is reported as a Warcraft Logs failure: %q", err, p.message)
		}
		if p.status != 200 {
			t.Errorf("%v: status = %d, want 200 — the page worked, only its prose did not", err, p.status)
		}
	}
	// And the fallback still exists for everything else.
	if p := classify(errors.New("something nobody has classified")); !strings.Contains(p.message, "Warcraft Logs") {
		t.Errorf("the fallback row changed: %q", p.message)
	}
}

// With no model configured nothing is asked and the page is the analyser's.
func TestWithNoModelTheAnalyserWordsItsOwnFindings(t *testing.T) {
	out := analyseWith(t, nil)
	if len(out.findings) == 0 {
		t.Fatal("no findings")
	}
	if out.findings[0].Title == "" {
		t.Error("the finding lost its title on the way to the page")
	}
	if len(out.notices) != 0 {
		t.Errorf("notices = %q, want none: a server with no key is not a failure", out.notices)
	}
}

// analyseWith runs the background job over a pull built to produce exactly one
// finding, with model installed as the writer.
func analyseWith(t *testing.T, model writer) result {
	t.Helper()
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var opts []Option
	if model != nil {
		opts = append(opts, WithWriter(model))
	}
	s := New(coachableWCL(), tpl, slog.New(slog.NewTextHandler(io.Discard, nil)), opts...)

	out, err := s.analyse(context.Background(), s.log, jobKey{
		subject: warcraftlogs.Subject{ReportCode: "ExampleReport123", FightID: 12, ActorID: 21},
	})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	return out
}

// coachableWCL answers with a Fire Mage who used Combustion twice early and
// then never again, which is the one rule that exists today firing.
func coachableWCL() fakeWCL {
	player := warcraftlogs.PlayerStats{
		ActorID: 21, Name: "Testmage", Server: "Testrealm",
		Class: "Mage", Spec: "Fire", FightDuration: 7 * time.Minute,
	}
	return fakeWCL{
		fightDetail: func(context.Context, string, int) (*warcraftlogs.FightDetail, error) {
			kill := true
			return &warcraftlogs.FightDetail{
				ReportCode: "ExampleReport123", ReportTitle: "Recorded raid night",
				Fight:   warcraftlogs.Fight{ID: 12, Name: "The Coiled Altar", Kill: &kill},
				Players: []warcraftlogs.PlayerStats{player},
			}, nil
		},
		timeline: func(_ context.Context, _ string, _ warcraftlogs.Fight, _ int, _ knowledge.Knowledge) (*warcraftlogs.Timeline, error) {
			return &warcraftlogs.Timeline{
				Subject:  warcraftlogs.Subject{ReportCode: "ExampleReport123", FightID: 12, ActorID: 21},
				Duration: 7 * time.Minute,
				Casts: []warcraftlogs.Cast{
					{AbilityID: 190319, Name: "Combustion", Offset: 10 * time.Second},
					{AbilityID: 190319, Name: "Combustion", Offset: 2*time.Minute + 13*time.Second},
				},
			}, nil
		},
	}
}
