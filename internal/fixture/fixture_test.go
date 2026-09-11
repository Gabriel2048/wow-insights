package fixture

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"wowinsight/internal/warcraftlogs"
)

// The names below are invented for this test and are the kind of thing a real
// recording carries: a character on a two-word realm, a character whose name
// is the prefix of an ability, an owner whose handle is not a character name.
const (
	realCode  = "AbCdEfGh12345678"
	realOwner = "Zorbulaxlogs"
	realRealm = "Twisting Nether"
)

// fakeAPI answers the client's queries the way the real service would, with
// real-looking names in every place they occur. The timeline's casts span two
// pages, so that the recording has to follow the cursor.
func fakeAPI(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"real","expires_in":3600}`))
	})
	mux.HandleFunc("POST /api", func(w http.ResponseWriter, r *http.Request) {
		var body graphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		k, err := key(body.Variables)
		if err != nil {
			t.Errorf("key: %v", err)
		}
		if code, _ := body.Variables["code"].(string); k != "ratelimit" && code != realCode {
			_, _ = w.Write([]byte(`{"data":{"reportData":{"report":null}}}`))
			return
		}
		responses := map[string]string{
			"ratelimit": `{"data":{"rateLimitData":{"limitPerHour":3600,"pointsSpentThisHour":7,"pointsResetIn":900}}}`,
			"report": `{"data":{"reportData":{"report":{"code":"` + realCode + `","title":"Zorbulax's Tuesday",
				"startTime":1700000000000,"endTime":1700000600000,"owner":{"name":"` + realOwner + `"},
				"zone":{"name":"The Venomous Abyss"},
				"fights":[{"id":12,"name":"The Coiled Altar","kill":true,"difficulty":4,"startTime":1000,"endTime":301000},
				          {"id":19,"name":"The Coiled Altar","kill":false,"difficulty":4,"startTime":400000,"endTime":560000}]}}}}`,
			"fight-12": `{"data":{"reportData":{"report":{"code":"` + realCode + `","title":"Zorbulax's Tuesday",
				"fights":[{"id":12,"name":"The Coiled Altar","kill":true,"difficulty":4,"startTime":1000,"endTime":301000,"bossPercentage":0,"friendlyPlayers":[7,11]}],
				"masterData":{"actors":[
					{"id":7,"name":"Zorbulax","type":"Player","subType":"Mage","server":"` + realRealm + `"},
					{"id":11,"name":"Ash","type":"Player","subType":"Priest","server":"` + realRealm + `"}]},
				"damage":{"data":{"entries":[{"id":7,"name":"Zorbulax","guid":123456789,"type":"Mage","icon":"Mage-Fire","itemLevel":678,"total":240000000,"activeTime":250000,
					"pets":[{"id":40,"name":"Echo","type":"Pet","total":1000}]}],"totalTime":300000}},
				"healing":{"data":{"entries":[{"id":11,"name":"Ash-TwistingNether","guid":987654321,"type":"Priest","icon":"Priest-Holy","itemLevel":675,"total":90000000,"overheal":30000000}],"totalTime":300000}},
				"deaths":{"data":{"entries":[{"id":11,"name":"Ash","guid":987654321,"deathWindow":"killed by Dread Bolt while Zorbulax was casting"}]}}}}}}`,
			"timeline-12-7-1000": `{"data":{"reportData":{"report":{
				"casts":{"data":[{"timestamp":2000,"type":"begincast","sourceID":7,"targetID":-1,"abilityGameID":133},
				                  {"timestamp":4000,"type":"cast","sourceID":7,"targetID":-1,"abilityGameID":133}],"nextPageTimestamp":5000},
				"lust":{"data":[],"nextPageTimestamp":null},"procs":{"data":[],"nextPageTimestamp":null},
				"cooldowns":{"data":[],"nextPageTimestamp":null},"raidCDs":{"data":[],"nextPageTimestamp":null},
				"damage":{"data":{"series":[{"name":"Fireball","pointStart":1000,"pointInterval":3000,"data":[100,200]}]}},
				"taken":{"data":{"series":[]}},
				"bossCasts":{"data":[],"nextPageTimestamp":null},
				"masterData":{"abilities":[{"gameID":133,"name":"Fireball"},{"gameID":1,"name":"Ashen Call"},{"gameID":2,"name":"Echo"}],
				              "actors":[{"id":7,"name":"Zorbulax","subType":"Mage"},{"id":11,"name":"Ash","subType":"Priest"}],
				              "npcs":[{"id":30,"name":"The Coiled One","subType":"Boss"}]},
				"fights":[{"encounterID":3000,"phaseTransitions":[{"id":1,"startTime":1000}]}],
				"phases":[{"encounterID":3000,"phases":[{"id":1,"name":"Stage One","isIntermission":false}]}]}}}}`,
			"timeline-12-7-5000": `{"data":{"reportData":{"report":{"casts":{"data":[
				{"timestamp":6000,"type":"cast","sourceID":7,"targetID":-1,"abilityGameID":133}],"nextPageTimestamp":null}}}}}`,
		}
		resp, ok := responses[k]
		if !ok {
			t.Errorf("the client asked for %s, which the fake does not serve", k)
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(resp))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// record drives the real client through a Recorder against the fake API and
// writes the result, the way cmd/record does.
func record(t *testing.T) string {
	t.Helper()
	srv := fakeAPI(t)
	rec := NewRecorder()
	wcl := warcraftlogs.New("id", "secret",
		warcraftlogs.WithBaseURL(srv.URL+"/oauth/token", srv.URL+"/api"),
		warcraftlogs.WithHTTPClient(rec.Client()))
	ctx := context.Background()
	if _, err := wcl.RateLimit(ctx); err != nil {
		t.Fatalf("RateLimit() returned error: %v", err)
	}
	if _, err := wcl.Report(ctx, realCode); err != nil {
		t.Fatalf("Report() returned error: %v", err)
	}
	detail, err := wcl.FightDetail(ctx, realCode, 12)
	if err != nil {
		t.Fatalf("FightDetail() returned error: %v", err)
	}
	if _, err := wcl.Timeline(ctx, realCode, detail.Fight, 7); err != nil {
		t.Fatalf("Timeline() returned error: %v", err)
	}
	dir := t.TempDir()
	if err := rec.Write(dir); err != nil {
		t.Fatalf("Write() returned error: %v", err)
	}
	return dir
}

func TestRecordingWritesOneFilePerExchange(t *testing.T) {
	dir := record(t)
	for _, name := range []string{"ratelimit.json", "report.json", "fight-12.json", "timeline-12-7-1000.json", "timeline-12-7-5000.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was not written: %v (the second timeline file is the cast page the cursor pointed at)", name, err)
		}
	}
}

// The whole point. Every real value must be gone from every file, and the game
// data around them must be untouched.
func TestRecordingIsRedactedEverywhere(t *testing.T) {
	dir := record(t)
	names, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(names) == 0 {
		t.Fatal("nothing was written")
	}
	var all strings.Builder
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		all.WriteString(string(data) + "\n")
	}
	for _, real := range []string{"Zorbulax", "Ash", "Twisting Nether", "TwistingNether", realOwner, realCode, "123456789", "987654321"} {
		// As a whole word: "Ash" is also the start of "Ashen Call", which
		// is game data and must stay.
		if regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(real) + `\b`).MatchString(all.String()) {
			t.Errorf("%q survived redaction in the written files", real)
		}
	}
	for _, kept := range []string{"Ashen Call", "Fireball", `"name":"Echo"`, "The Coiled One", "The Coiled Altar", "The Venomous Abyss", "Stage One", "Mage-Fire"} {
		if !strings.Contains(all.String(), kept) {
			t.Errorf("%q is game data and should have been kept", kept)
		}
	}
	for _, fake := range []string{
		`"name":"Testmage"`, `"name":"Testpriest"`, `"server":"Testrealm"`,
		`"name":"Testpriest-Testrealm"`, // the cross-realm form in a table entry
		`"code":"ExampleReport123"`, `"name":"Testowner"`, `"title":"Recorded raid night"`,
		`"guid":0`,
		`"name":"Testpet","total":1000,"type":"Pet"`,      // a pet's name is its owner's choice
		`killed by Dread Bolt while Testmage was casting`, // a name inside untyped text
	} {
		if !strings.Contains(all.String(), fake) {
			t.Errorf("the written files do not contain %s", fake)
		}
	}
}

// Numbers must come back byte for byte: a timestamp rewritten as 1.7e+12
// would still decode, but the file would no longer be what the API sent.
func TestRecordingKeepsNumbersAsTheyWere(t *testing.T) {
	dir := record(t)
	data, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"startTime":1700000000000`) {
		t.Errorf("report.json = %s, want the start time written as an integer", data)
	}
}

// The recording replays through the real client, so what the application
// sees offline is what buildFightDetail and buildTimeline produce from the
// redacted bytes — including the cast pagination.
func TestReplayServesTheRecordingThroughTheRealClient(t *testing.T) {
	dir := record(t)
	replay, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	if replay.Code() != FakeCode {
		t.Errorf("Code() = %q, want %q", replay.Code(), FakeCode)
	}
	if got := replay.Fights(); len(got) != 1 || got[0].ID != 12 || len(got[0].Players) != 1 || got[0].Players[0] != 7 {
		t.Errorf("Fights() = %+v, want fight 12 with player 7", got)
	}

	wcl := warcraftlogs.New("fixture", "fixture", warcraftlogs.WithHTTPClient(replay.Client()))
	ctx := context.Background()
	report, err := wcl.Report(ctx, FakeCode)
	if err != nil {
		t.Fatalf("Report() returned error: %v", err)
	}
	if report.Title != FakeTitle || report.Owner.Name != FakeOwner {
		t.Errorf("report title=%q owner=%q, want the fake ones", report.Title, report.Owner.Name)
	}
	detail, err := wcl.FightDetail(ctx, FakeCode, 12)
	if err != nil {
		t.Fatalf("FightDetail() returned error: %v", err)
	}
	if p, ok := detail.Player(7); !ok || p.Name != "Testmage" || p.Server != FakeRealm || p.Spec != "Fire" {
		t.Errorf("player 7 = %+v, want Testmage of Testrealm, Fire", p)
	}
	timeline, err := wcl.Timeline(ctx, FakeCode, detail.Fight, 7)
	if err != nil {
		t.Fatalf("Timeline() returned error: %v", err)
	}
	// Two casts on the first page (a begincast/cast pair, so one Cast) and one
	// on the second page.
	if len(timeline.Casts) != 2 {
		t.Errorf("len(Casts) = %d, want 2 (the second page of casts was not followed)", len(timeline.Casts))
	}
	if timeline.Total == 0 || timeline.Casts[1].Percent == 0 {
		t.Errorf("Total=%v Casts[1].Percent=%v: the timeline was not laid out, so the replay is not going through the real client", timeline.Total, timeline.Casts[1].Percent)
	}
	if _, err := wcl.RateLimit(ctx); err != nil {
		t.Errorf("RateLimit() returned error: %v (the health endpoint should work offline)", err)
	}
}

func TestReplayAnswersAnUnknownCodeWithNotFound(t *testing.T) {
	replay, err := Open(record(t))
	if err != nil {
		t.Fatal(err)
	}
	wcl := warcraftlogs.New("fixture", "fixture", warcraftlogs.WithHTTPClient(replay.Client()))
	_, err = wcl.Report(context.Background(), "SomeOtherCode123")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want the client's own not-found message", err)
	}
}

// Nothing unrecorded may reach the network, and the error must say which file
// is missing so the fix is obvious.
func TestReplayRefusesWhatWasNotRecorded(t *testing.T) {
	replay, err := Open(record(t))
	if err != nil {
		t.Fatal(err)
	}
	wcl := warcraftlogs.New("fixture", "fixture", warcraftlogs.WithHTTPClient(replay.Client()))
	_, err = wcl.FightDetail(context.Background(), FakeCode, 19)
	if err == nil || !strings.Contains(err.Error(), "fight-19.json") {
		t.Errorf("error = %v, want it to name the missing fight-19.json", err)
	}
}

func TestOpenRejectsADirectoryWithNoReport(t *testing.T) {
	if _, err := Open(t.TempDir()); err == nil || !strings.Contains(err.Error(), "cmd/record") {
		t.Errorf("error = %v, want one pointing at the recorder", err)
	}
}

// A second report recorded into the same directory would leave report.json
// listing fights the directory cannot serve.
func TestWriteRefusesADifferentReportInTheSameDirectory(t *testing.T) {
	dir := record(t)
	rec := NewRecorder()
	rec.exchanges["report"] = []byte(`{"data":{"reportData":{"report":{"code":"` + realCode + `","startTime":1600000000000,"owner":{"name":"x"},"masterData":{"actors":[]}}}}}`)
	err := rec.Write(dir)
	if err == nil || !strings.Contains(err.Error(), "different report") {
		t.Errorf("error = %v, want a refusal", err)
	}
}

// The scan after redaction is the guard against a future rule missing a
// place. It has to trip on a name that is still there.
func TestApplyRefusesWhenARealValueSurvives(t *testing.T) {
	r := &redactor{rules: []rule{{real: "Zorbulax", fake: "Testmage", kind: "a character name"}}}
	// The walk only reaches strings; a name smuggled into a key is exactly the
	// kind of position the rules do not cover and the scan must.
	_, err := r.apply([]byte(`{"Zorbulax":1}`))
	if err == nil || !strings.Contains(err.Error(), "a character name") {
		t.Errorf("error = %v, want a refusal naming the kind of value, not the value", err)
	}
	if err != nil && strings.Contains(err.Error(), "Zorbulax") {
		t.Errorf("error = %v: the refusal must not say what survived", err)
	}
}

func TestReplaceWordMatchesWholeWordsCaseInsensitively(t *testing.T) {
	for _, tc := range []struct {
		in, real, want string
		n              int
	}{
		{"Ash", "Ash", "Testpriest", 1},
		{"ash", "Ash", "Testpriest", 1},
		{"Ashen Call", "Ash", "Ashen Call", 0},
		{"Ash-TwistingNether", "Ash", "Testpriest-TwistingNether", 1},
		{"Zorbulax's Tuesday", "Zorbulax", "Testmage's Tuesday", 1},
		{"Zorbulaxé", "Zorbulax", "Zorbulaxé", 0}, // an accented letter is still a letter
		{"Zorbulaxé", "Zorbulaxé", "Testmage", 1}, // and a name can carry one
		{"Zorbulax and Zorbulax", "Zorbulax", "Testmage and Testmage", 2},
	} {
		fake := "Testmage"
		if tc.real == "Ash" {
			fake = "Testpriest"
		}
		got, n := replaceWord(tc.in, tc.real, fake)
		if got != tc.want || n != tc.n {
			t.Errorf("replaceWord(%q, %q) = %q, %d; want %q, %d", tc.in, tc.real, got, n, tc.want, tc.n)
		}
	}
}

func TestKeyNamesEachQueryByItsVariables(t *testing.T) {
	for _, tc := range []struct {
		vars map[string]any
		want string
	}{
		{nil, "ratelimit"},
		{map[string]any{"code": "x"}, "report"},
		{map[string]any{"code": "x", "id": 12.0}, "fight-12"},
		{map[string]any{"code": "x", "id": 12.0, "source": 7.0, "start": 1000.0, "end": 301000.0}, "timeline-12-7-1000"},
		{map[string]any{"code": "x", "id": 12.0, "source": 7.0, "start": 123456.5, "end": 301000.0}, "timeline-12-7-123456.5"},
	} {
		got, err := key(tc.vars)
		if err != nil || got != tc.want {
			t.Errorf("key(%v) = %q, %v; want %q", tc.vars, got, err, tc.want)
		}
	}
	if _, err := key(map[string]any{"unknown": 1}); err == nil {
		t.Error("key() accepted variables it has no name for")
	}
}
