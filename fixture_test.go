package main

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"wowinsight/internal/fixture"
	"wowinsight/internal/warcraftlogs"
)

// The recording in testdata/ is made by a human with credentials, once, and
// committed. Until it exists these tests have nothing to run against, and the
// skip message is the instruction for making it exist.
const recordCommand = "go run ./cmd/record -report <URL> -fight <id> -players <name,...>"

// openRecording returns the committed recording, or skips.
func openRecording(t *testing.T) *fixture.Replay {
	t.Helper()
	if _, err := os.Stat(filepath.Join("testdata", "report.json")); err != nil {
		t.Skipf("no recording in testdata/; record one with: %s", recordCommand)
	}
	replay, err := fixture.Open("testdata")
	if err != nil {
		t.Fatalf("Open(testdata) returned error: %v", err)
	}
	return replay
}

// recordedServer is the fixture-mode server exactly as main builds it: the
// real client over the replay transport, so every page comes from production
// code with only the wire swapped.
func recordedServer(t *testing.T, replay *fixture.Replay) *server {
	t.Helper()
	return newTestServer(t, warcraftlogs.New("fixture", "fixture", warcraftlogs.WithHTTPClient(replay.Client())))
}

// The acceptance criterion of #10: a fight page renders on a machine with no
// credentials, and it is a real one — laid out, not everything stacked at
// left: 0.000%. That last part is what a fake client cannot provide, since
// layout() runs only inside the real one.
//
// This is also the staleness detector: when a query grows a field the
// recording lacks, the page still renders, so the assertions here are on the
// things that would go missing.
func TestFixtureRendersAFightPage(t *testing.T) {
	replay := openRecording(t)
	s := recordedServer(t, replay)
	if len(replay.Fights()) == 0 {
		t.Fatal("the recording holds a report but no fight")
	}
	for _, f := range replay.Fights() {
		if len(f.Players) == 0 {
			t.Errorf("fight %d was recorded with no player timeline; re-record it with -players", f.ID)
		}
		for _, player := range f.Players {
			target := "/report/" + replay.Code() + "/fight/" + strconv.Itoa(f.ID) + "?player=" + strconv.Itoa(player)
			rec := get(t, s.wcl, target)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status = %d, want %d: %s", target, rec.Code, http.StatusOK, rec.Body.String())
			}
			page := rec.Body.String()
			if strings.Contains(page, "ZgotmplZ") || strings.Contains(page, "<no value>") {
				t.Errorf("%s: the page carries an escaper refusal or a missing field", target)
			}
			if !strings.Contains(page, "Cast timeline") {
				t.Errorf("%s: no timeline rendered", target)
			}
			m := regexp.MustCompile(`data-total-ms="(\d+)"`).FindStringSubmatch(page)
			if m == nil || m[1] == "0" {
				t.Errorf("%s: data-total-ms = %v, want non-zero (the timeline was not laid out)", target, m)
			}
			positioned := false
			for _, tick := range regexp.MustCompile(`class="tick[^"]*"\s+style="left: ([0-9.]+)%"`).FindAllStringSubmatch(page, -1) {
				if tick[1] != "0.000" {
					positioned = true
				}
			}
			if !positioned {
				t.Errorf("%s: no cast is positioned anywhere but 0.000%%", target)
			}
		}
	}
}

func TestFixtureReportPageListsTheRecordedFights(t *testing.T) {
	replay := openRecording(t)
	s := recordedServer(t, replay)
	rec := get(t, s.wcl, "/?url=https://www.warcraftlogs.com/reports/"+replay.Code())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	for _, f := range replay.Fights() {
		href := `href="/report/` + replay.Code() + `/fight/` + strconv.Itoa(f.ID) + `"`
		if !strings.Contains(rec.Body.String(), href) {
			t.Errorf("the report page does not link to recorded fight %d (%s)", f.ID, href)
		}
	}
}

// The recorder refuses to write a real name, and this is the committed half
// of that guard: every player in the recording carries a pseudonym, so a
// hand-edited or foreign file cannot slip in.
func TestFixtureRosterIsRedacted(t *testing.T) {
	replay := openRecording(t)
	if replay.Code() != fixture.FakeCode {
		t.Errorf("recorded code = %q, want %q", replay.Code(), fixture.FakeCode)
	}
	pseudonym := regexp.MustCompile(`^Test[a-z]+[0-9]*$`)
	s := recordedServer(t, replay)
	for _, f := range replay.Fights() {
		detail, err := s.wcl.FightDetail(t.Context(), replay.Code(), f.ID)
		if err != nil {
			t.Fatalf("FightDetail(%d) returned error: %v", f.ID, err)
		}
		if detail.ReportTitle != fixture.FakeTitle {
			t.Errorf("fight %d: report title = %q, want %q", f.ID, detail.ReportTitle, fixture.FakeTitle)
		}
		for _, p := range detail.Players {
			if !pseudonym.MatchString(p.Name) {
				t.Errorf("fight %d: player %d is named %q, which is not a pseudonym", f.ID, p.ActorID, p.Name)
			}
			if !strings.HasPrefix(p.Server, fixture.FakeRealm) {
				t.Errorf("fight %d: player %d is on %q, which is not a pseudonym", f.ID, p.ActorID, p.Server)
			}
		}
	}
	report, err := s.wcl.Report(t.Context(), replay.Code())
	if err != nil {
		t.Fatalf("Report() returned error: %v", err)
	}
	if report.Owner.Name != fixture.FakeOwner {
		t.Errorf("owner = %q, want %q", report.Owner.Name, fixture.FakeOwner)
	}
}

// A bad -fixture path must fail at startup, not on the first request.
func TestRunRejectsAMissingFixtureDirectory(t *testing.T) {
	err := run([]string{"-fixture", filepath.Join(t.TempDir(), "nope")}, discard{})
	if err == nil || !strings.Contains(err.Error(), "cmd/record") {
		t.Errorf("error = %v, want one pointing at the recorder", err)
	}
}

func TestRunRejectsAnUnknownFlag(t *testing.T) {
	if err := run([]string{"-nope"}, discard{}); err == nil {
		t.Error("run() accepted a flag it does not define")
	}
}
