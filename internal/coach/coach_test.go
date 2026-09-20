package coach

import (
	"context"
	"errors"
	"strings"
	"testing"

	"wowinsight/internal/warcraftlogs"
)

// A pull with nothing to say costs nothing and risks nothing. This is the
// common case — both committed recordings produce no findings at all — and it
// is also the case where a model with nothing to do would be most tempted to
// find something, so it is never asked.
func TestAPullWithNoFindingsNeverReachesTheModel(t *testing.T) {
	in := fixtureInput(t)
	in.Findings = nil

	got, err := writerOver(refuser{t}).Write(context.Background(), in)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d findings, want none", len(got))
	}
}

// The wording is taken and nothing else is.
func TestAGoodRewriteReplacesOnlyTheWords(t *testing.T) {
	in := fixtureInput(t)
	w := &wire{t: t, answer: goodReply()}

	got, err := writerOver(w).Write(context.Background(), in)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1", len(got))
	}
	original := tailFinding()
	if got[0].Title == original.Title {
		t.Error("the title was not reworded, so this test is proving nothing")
	}
	// Everything the player checks the claim against is the analyser's and
	// must survive untouched.
	if got[0].Severity != original.Severity {
		t.Errorf("severity %v, want %v (the model may not change it)", got[0].Severity, original.Severity)
	}
	if got[0].At != original.At {
		t.Errorf("At %v, want %v (the model may not move a finding in time)", got[0].At, original.At)
	}
	if got[0].RuleID != original.RuleID {
		t.Errorf("RuleID %q, want %q (a finding must stay traceable to its rule)", got[0].RuleID, original.RuleID)
	}
	if len(got[0].Evidence) != len(original.Evidence) {
		t.Errorf("got %d pieces of evidence, want %d (the working is not the model's to edit)", len(got[0].Evidence), len(original.Evidence))
	}
}

// Every way a reply can fail, and every one of them leaves the page showing
// the analyser's own sentences rather than half a page of prose.
func TestARejectedReplyLeavesTheAnalysersOwnWords(t *testing.T) {
	original := tailFinding()
	for _, c := range []struct {
		name  string
		reply any
		why   string
	}{
		{
			name: "a number that is not in the evidence",
			reply: reply{Findings: []block{{
				Ref:    "cooldown-unused-tail@5:54",
				Title:  "Combustion sat ready from 5:54",
				Detail: "It came back at 5:54 with 90s left, so that is 2 uses gone.",
			}}},
			why: "90s and 2 uses are both invented",
		},
		{
			name: "a spell that is not in this pull",
			reply: reply{Findings: []block{{
				Ref:    "cooldown-unused-tail@5:54",
				Title:  "Combustion sat ready from 5:54",
				Detail: "You should have used Rune of Power before pressing it at 4:53.",
			}}},
			why: "a recommendation with no numbers in it at all, about a talent nobody mentioned",
		},
		{
			name: "a reference it was never given",
			reply: reply{Findings: []block{{
				Ref:    "cooldown-drift@2:19",
				Title:  "Combustion drifted",
				Detail: "It came back at 5:54.",
			}}},
			why: "the rule that produced it does not exist",
		},
		{
			name: "more blocks than there were findings",
			reply: reply{Findings: []block{
				{Ref: "cooldown-unused-tail@5:54", Title: "One", Detail: "It came back at 5:54."},
				{Ref: "cooldown-unused-tail@5:54", Title: "Two", Detail: "It came back at 5:54."},
			}},
			why: "a second finding has been manufactured out of the first",
		},
		{
			name:  "no blocks at all",
			reply: reply{},
			why:   "a finding silently dropped is one nobody can check",
		},
		{
			name: "a number written as a word",
			reply: reply{Findings: []block{{
				Ref:    "cooldown-unused-tail@5:54",
				Title:  "Combustion sat ready",
				Detail: "It came back at 5:54 and sat there for seventy-seven seconds.",
			}}},
			why: "a spelled number is invisible to every numeric check",
		},
		{
			name: "a proportion instead of a number",
			reply: reply{Findings: []block{{
				Ref:    "cooldown-unused-tail@5:54",
				Title:  "Combustion sat ready",
				Detail: "It came back at 5:54 and you lost most of the value.",
			}}},
			why: "a numeric claim with nothing to check it against",
		},
		{
			name: "set aside with no reason",
			reply: reply{Findings: []block{{
				Ref: "cooldown-unused-tail@5:54", Title: "Combustion sat ready",
				Detail: "It came back at 5:54.", SetAside: true,
			}}},
			why: "a finding waved away without an argument is worse than one left standing",
		},
		{
			name: "a moment restated as a length",
			reply: reply{Findings: []block{{
				Ref: "cooldown-unused-tail@5:54", Title: "Combustion sat ready",
				Detail: "It came back 354 seconds in.",
			}}},
			why: "5:54 is a moment and 354s is a length; the evidence gives one, not the other",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := &wire{t: t, answer: c.reply}
			got, err := writerOver(w).Write(context.Background(), fixtureInput(t))
			if !errors.Is(err, ErrUntrustworthy) {
				t.Fatalf("err = %v, want ErrUntrustworthy (%s)", err, c.why)
			}
			if len(got) != 1 || got[0].Title != original.Title || got[0].Detail != original.Detail {
				t.Errorf("the page did not fall back to the analyser's own words (%s)", c.why)
			}
		})
	}
}

// Rounding a number the evidence states more precisely is honest, and is the
// one rewrite of a number that is allowed.
func TestRoundingIsAllowedAndChangingIsNot(t *testing.T) {
	in := fixtureInput(t)
	in.Findings[0].Evidence = []warcraftlogs.Evidence{
		{Label: "pull still to run", Value: "77.4s"},
		{Label: "ready again at", Value: "5:54"},
	}
	for _, c := range []struct {
		wrote string
		ok    bool
	}{
		{"It came back at 5:54 with 77s left.", true},
		{"It came back at 5:54 with 77.4s left.", true},
		{"It came back at 5:54 with 78s left.", false},
		{"It came back at 5:54 with 80s left.", false},
	} {
		w := &wire{t: t, answer: reply{Findings: []block{{
			Ref: "cooldown-unused-tail@5:54", Title: "Combustion sat ready", Detail: c.wrote,
		}}}}
		got, err := writerOver(w).Write(context.Background(), in)
		switch {
		case c.ok && err != nil:
			t.Errorf("%q was rejected: %v (it is a correct rounding of 77.4s)", c.wrote, err)
		case !c.ok && err == nil:
			t.Errorf("%q was accepted, but the evidence says 77.4s", c.wrote)
		case c.ok && err == nil && got[0].Detail != c.wrote:
			t.Errorf("detail = %q, want %q", got[0].Detail, c.wrote)
		}
	}
}

// A finding the model judged the player had a reason for is still on the
// page, below the rest, with the reason next to it. This is the mechanism
// that answers the one thing a deterministic rule cannot: was the hold
// deliberate?
func TestASetAsideFindingStaysOnThePageWithItsReason(t *testing.T) {
	w := &wire{t: t, answer: reply{Findings: []block{{
		Ref:      "cooldown-unused-tail@5:54",
		Title:    "Combustion sat ready from 5:54",
		Detail:   "It came back at 5:54 and never went out again.",
		SetAside: true,
		Why:      "The Intermission started at 3:44 and you were holding for it.",
	}}}}

	got, err := writerOver(w).Write(context.Background(), fixtureInput(t))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(got.Standing()) != 0 {
		t.Errorf("%d findings still standing, want none", len(got.Standing()))
	}
	aside := got.SetAside()
	if len(aside) != 1 {
		t.Fatalf("%d findings set aside, want 1 (a set-aside finding is shown, never dropped)", len(aside))
	}
	if aside[0].Why == "" {
		t.Error("the reason was lost, and a set-aside finding with no reason cannot be argued with")
	}
	if len(aside[0].Evidence) == 0 {
		t.Error("the evidence was lost; it is what lets a player disagree with the judgement")
	}
}

// A model that cannot be reached is a plainer page, never a failed one.
func TestAnOutageIsAPlainerPageAndNotAFailure(t *testing.T) {
	w := &wire{t: t, status: 529}
	got, err := writerOver(w).Write(context.Background(), fixtureInput(t))
	if !errors.Is(err, ErrModelBusy) {
		t.Fatalf("err = %v, want ErrModelBusy", err)
	}
	if len(got) != 1 || got[0].Title != tailFinding().Title {
		t.Error("the findings did not survive the outage, and they were computed before it")
	}
}

// A key the API rejects is a deployment fault and says so, so that it is not
// retried forever as though it were weather.
func TestARejectedKeyIsItsOwnFailure(t *testing.T) {
	w := &wire{t: t, status: 401}
	if _, err := writerOver(w).Write(context.Background(), fixtureInput(t)); !errors.Is(err, ErrBadKey) {
		t.Fatalf("err = %v, want ErrBadKey", err)
	}
}

// The request body is the fifth destination AGENTS.md's rule now names, and
// the only one that leaves the process. Nothing in it belongs to a person.
func TestNothingAboutAPersonLeavesTheProcess(t *testing.T) {
	w := &wire{t: t, answer: goodReply()}
	if _, err := writerOver(w).Write(context.Background(), fixtureInput(t)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if w.calls != 1 {
		t.Fatalf("the model was called %d times, want 1", w.calls)
	}
	body := string(w.sent)
	for _, secret := range []struct{ kind, value string }{
		{"the player's name", fakeName},
		{"another raider's name", fakeOther},
		{"a server", fakeServer},
		{"the report code", fakeCode},
		{"the report title", fakeTitle},
	} {
		if strings.Contains(body, secret.value) {
			t.Errorf("the request body carries %s", secret.kind)
		}
	}
	// And prove the body was worth checking: the pull's own shape is in it.
	for _, want := range []string{"Ula'tek", "Combustion", "Time Warp", "Intermission"} {
		if !strings.Contains(body, want) {
			t.Errorf("the request body does not mention %q, so this test is checking an empty body", want)
		}
	}
}

// Nothing is sent at all when the sheet would carry somebody. The direction
// matters: a page that loses its prose is cheap, and a name in a third
// party's request log cannot be taken back.
func TestARosterNameInTheBodyStopsTheRequest(t *testing.T) {
	in := fixtureInput(t)
	// A raider whose name is a word the fact sheet legitimately contains.
	// Real names like this exist, and the check must fire on them.
	in.Detail.Players = append(in.Detail.Players, warcraftlogs.PlayerStats{ActorID: 33, Name: "Combustion", Server: fakeServer})

	w := &wire{t: t, answer: goodReply()}
	got, err := writerOver(w).Write(context.Background(), in)
	if !errors.Is(err, ErrWouldLeak) {
		t.Fatalf("err = %v, want ErrWouldLeak", err)
	}
	if w.calls != 0 {
		t.Errorf("the model was called %d times; nothing may be sent once the check has failed", w.calls)
	}
	if len(got) != 1 {
		t.Errorf("got %d findings, want the analyser's 1", len(got))
	}
}

// An answer that is not the shape asked for is a failure like any other.
func TestAMalformedAnswerIsNotTrusted(t *testing.T) {
	w := &wire{t: t, raw: "I'd be happy to help! Here are your findings:"}
	if _, err := writerOver(w).Write(context.Background(), fixtureInput(t)); !errors.Is(err, ErrUntrustworthy) {
		t.Fatalf("err = %v, want ErrUntrustworthy", err)
	}
}
