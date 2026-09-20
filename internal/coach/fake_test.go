package coach

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"wowinsight/internal/knowledge"
	"wowinsight/internal/warcraftlogs"
)

// wire is a stand-in for the Anthropic API. It keeps the request body, so a
// test can assert on what went out — which is the only way to check the thing
// that matters most here, that no player's name ever did.
type wire struct {
	t *testing.T
	// answer is the JSON the model "returned", as the structured reply.
	answer any
	// status and raw override answer when a test needs a failure.
	status int
	raw    string

	calls int
	sent  []byte
}

func (w *wire) RoundTrip(req *http.Request) (*http.Response, error) {
	w.calls++
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		w.sent = body
	}
	if w.status != 0 && w.status != http.StatusOK {
		return &http.Response{
			StatusCode: w.status,
			Status:     http.StatusText(w.status),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`))),
			Request:    req,
		}, nil
	}
	text := w.raw
	if text == "" {
		encoded, err := json.Marshal(w.answer)
		if err != nil {
			w.t.Fatalf("encode the fake answer: %v", err)
		}
		text = string(encoded)
	}
	msg := map[string]any{
		"id":            "msg_fixture",
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       []any{map[string]any{"type": "text", "text": text}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         map[string]any{"input_tokens": 1, "output_tokens": 1},
	}
	body, err := json.Marshal(msg)
	if err != nil {
		w.t.Fatalf("encode the fake response: %v", err)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}, nil
}

// refuser fails the test if it is ever asked for anything.
type refuser struct{ t *testing.T }

func (r refuser) RoundTrip(*http.Request) (*http.Response, error) {
	r.t.Error("the model was called when there was nothing for it to do")
	return nil, fmt.Errorf("must not be called")
}

// writerOver builds a Writer whose wire is rt.
func writerOver(rt http.RoundTripper) *Writer {
	return New("test-key-not-a-real-one", slog.New(slog.DiscardHandler), WithTransport(rt))
}

// The fixture pull. It is Kyzz's shape — a Fire Mage on a seven-minute
// heroic kill — with every name invented. The player is "Testmage" on
// "Testrealm" precisely so that a test can assert those strings are not in
// the request body.
const (
	fakeName   = "Testmage"
	fakeServer = "Testrealm"
	fakeCode   = "ExampleReport123"
	fakeTitle  = "Recorded raid night"
	fakeOther  = "Testpriest"
)

func fireTables() knowledge.Knowledge {
	return knowledge.Knowledge{
		Spec:      knowledge.SpecID{Class: "Mage", Spec: "Fire"},
		ProcAuras: map[int]string{48108: "Hot Streak!"},
		Cooldowns: map[int]string{190319: "Combustion", 235313: "Blazing Barrier"},
		JudgedCooldowns: map[int]knowledge.JudgedCooldown{
			190319: {Name: "Combustion", Base: 120},
		},
	}
}

func fixtureInput(t *testing.T) Input {
	t.Helper()
	kill := true
	difficulty := 4
	fight := warcraftlogs.Fight{ID: 1, Name: "Ula'tek", Kill: &kill, Difficulty: &difficulty}

	timeline := &warcraftlogs.Timeline{
		Subject:  warcraftlogs.Subject{ReportCode: fakeCode, FightID: 1, ActorID: 21},
		Duration: 7*time.Minute + 11*time.Second,
		Phases: []warcraftlogs.Phase{
			{ID: 1, Name: "Stage One: The Gathering", Start: 0, End: 3*time.Minute + 44*time.Second},
			{ID: 2, Name: "Intermission: Ritual", IsIntermission: true, Start: 3*time.Minute + 44*time.Second, End: 4*time.Minute + 30*time.Second},
		},
		Lusts: []warcraftlogs.RaidWindow{
			// Source carries a real raider's name in production. The fact
			// sheet must not.
			{AbilityID: 80353, Name: "Time Warp", Source: fakeOther, Start: 3 * time.Minute, End: 3*time.Minute + 40*time.Second},
		},
		RaidCDs: []warcraftlogs.RaidWindow{
			{AbilityID: 97463, Name: "Rallying Cry", Source: fakeOther, Start: 2 * time.Minute, End: 2*time.Minute + 10*time.Second},
		},
		Cooldowns: []warcraftlogs.CooldownWindow{
			{AbilityID: 190319, Name: "Combustion", Start: 1 * time.Second, End: 15 * time.Second},
		},
		BossCasts: []warcraftlogs.BossCast{
			{AbilityID: 1234, Name: "Soulbinding", Source: "Ula'tek", Offset: 3*time.Minute + 40*time.Second, Count: 1},
		},
		Casts: []warcraftlogs.Cast{
			{AbilityID: 190319, Name: "Combustion", Offset: 1 * time.Second},
			{AbilityID: 190319, Name: "Combustion", Offset: 1*time.Minute + 10*time.Second},
			{AbilityID: 11366, Name: "Pyroblast", Offset: 2 * time.Minute, Gap: 4 * time.Second},
		},
	}

	return Input{
		Detail: &warcraftlogs.FightDetail{
			ReportCode:  fakeCode,
			ReportTitle: fakeTitle,
			Fight:       fight,
			Players: []warcraftlogs.PlayerStats{
				{ActorID: 21, Name: fakeName, Server: fakeServer, Class: "Mage", Spec: "Fire"},
				{ActorID: 29, Name: fakeOther, Server: fakeServer, Class: "Priest", Spec: "Discipline"},
			},
		},
		Player:   warcraftlogs.PlayerStats{ActorID: 21, Name: fakeName, Server: fakeServer, Class: "Mage", Spec: "Fire", FightDuration: 7*time.Minute + 11*time.Second},
		Timeline: timeline,
		Know:     fireTables(),
		Findings: []warcraftlogs.Finding{tailFinding()},
	}
}

// tailFinding is the shape the one live rule produces, with the numbers the
// rule would have put in it.
func tailFinding() warcraftlogs.Finding {
	return warcraftlogs.Finding{
		RuleID:   "cooldown-unused-tail",
		Severity: warcraftlogs.Major,
		Title:    "Combustion came back at 5:54 and was never used again",
		Detail:   "Your last Combustion was at 4:53, so it came back around 5:54 with 77s of the pull still to run. It was never pressed again. That is 1 full use left on the table.",
		At:       5*time.Minute + 54*time.Second,
		Evidence: []warcraftlogs.Evidence{
			{Label: "last used", Value: "4:53"},
			{Label: "ready again at", Value: "5:54"},
			{Label: "pull still to run", Value: "77s"},
			{Label: "uses left unspent", Value: "1"},
		},
	}
}

// goodReply is a rewrite that stays inside the evidence.
func goodReply() any {
	return reply{Findings: []block{{
		Ref:    "cooldown-unused-tail@5:54",
		Title:  "Combustion sat ready from 5:54 to the end",
		Detail: "Your last Combustion went out at 4:53 and it was back at 5:54, with 77s of pull left. It never went out again, so that is 1 use of your best damage window unspent.",
	}}}
}
