package web

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

// The recording in testdata/ at the repository root is made by a human with
// credentials, once, and committed. It lives at the root rather than beside
// this package because cmd/dev/serve-recorded reads it at runtime too. Until
// it exists these tests have nothing to run against, and the skip message is
// the instruction for making it exist.
const (
	recordingDir  = "../../testdata"
	recordCommand = "go run ./cmd/dev/record -report <URL> -fight <id> -players <name,...>"
)

// openRecording returns the committed recording, or skips.
func openRecording(t *testing.T) *fixture.Replay {
	t.Helper()
	if _, err := os.Stat(filepath.Join(recordingDir, "report.json")); err != nil {
		t.Skipf("no recording in testdata/; record one with: %s", recordCommand)
	}
	replay, err := fixture.Open(recordingDir)
	if err != nil {
		t.Fatalf("Open(%s) returned error: %v", recordingDir, err)
	}
	return replay
}

// recordedServer is the server exactly as cmd/dev/serve-recorded builds it:
// the real client over the replay transport, so every page comes from
// production code with only the wire swapped.
func recordedServer(t *testing.T, replay *fixture.Replay) *Server {
	t.Helper()
	return newTestServer(t, warcraftlogs.New("fixture", "fixture", warcraftlogs.WithTransport(replay)))
}

// The acceptance criterion of #10: a fight page renders on a machine with no
// credentials, and it is a real one — decoded, paged, built and laid out by
// production code, not everything stacked at left: 0.000%.
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

// The positioned golden render #10 deferred: the recorded kill, laid out
// and rendered, with the positions of specific things pinned to three
// decimals. Every number was checked by hand against the axis: the fight is
// 431,472 ms, the precast bar starts 1,650 ms before the pull so the lead-in
// is 2,400 ms with its margin, and the pull therefore sits at 2400/433872 =
// 0.553%. A change to the axis — the lead-in rule, the total, the percent
// formula — moves all of these at once, which is what the test is for; a
// change to the analysis moves only what it changed, and the message says
// which.
func TestGoldenRenderOfTheRecordedKill(t *testing.T) {
	replay := openRecording(t)
	rec := get(t, recordedServer(t, replay).wcl, "/report/"+replay.Code()+"/fight/1?player=21")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	page := rec.Body.String()

	// The axis.
	for _, want := range []string{`data-total-ms="433872"`, `data-lead-ms="2400"`, `data-duration-ms="431472"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the axis moved: want %s, page has %s", want, regexp.MustCompile(`data-(total|lead|duration)-ms="[0-9]+"`).FindAllString(page, -1))
		}
	}
	// Positions on it. Each pattern names the element and its numbers and
	// nothing else — not its other classes, attribute order or the rest of
	// its style — so a markup change that moves nothing stays green.
	for what, pattern := range map[string]string{
		"the pull": `class="prepull[^>]*width: 0\.553%`,
		"the precast Pyroblast bar (1.818s, reconstructed)": `class="castbar estimated[^>]*left: 0\.173%; width: 0\.419%`,
		"the precast's tick":                       `class="tick[^>]*left: 0\.173%`,
		"the first phase (Stage One, 2m00s)":       `class="phase[^>]*left: 0\.553%; width: 27\.764%`,
		"the first boss marker":                    `class="bcast[^>]*left: 0\.558%`,
		"the DPS curve's first point, on the pull": `points="0\.553,87\.776`,
	} {
		if !regexp.MustCompile(pattern).MatchString(page) {
			t.Errorf("%s is not where it was: /%s/ not found", what, pattern)
		}
	}
}

// The recording holds a Holy Paladin (actor 11) next to the Fire Mage, so the
// unauthored-spec path is exercised end to end over production code: the
// replay answers the query with no proc or cooldown filter, the page renders
// the class-agnostic lanes, and it says why the rest is missing.
func TestFixtureRendersAnUnauthoredSpecWithANotice(t *testing.T) {
	replay := openRecording(t)
	page := get(t, recordedServer(t, replay).wcl, "/report/"+replay.Code()+"/fight/1?player=11").Body.String()
	for _, want := range []string{
		"No rotation knowledge for Holy Paladin yet",
		"Cast timeline",
		`class="phase`,    // the phases still render
		`class="rcdblock`, // and the raid cooldowns
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the Holy Paladin's page lacks %q", want)
		}
	}
	if strings.Contains(page, `class="cdblock`) {
		t.Error("the Holy Paladin's page draws personal cooldown blocks")
	}
}
