package warcraftlogs

import (
	"encoding/json"
	"os"
	"testing"
)

// recordedKill loads the committed recording of the Fire Mage's kill: the
// timeline document and the report's master data, which lives in its own
// file since the query was hoisted. Skips when there is no recording.
func recordedKill(t *testing.T) (*timelineReport, Fight) {
	t.Helper()
	raw, err := os.ReadFile(recordedKillFile)
	if err != nil {
		skipWithoutRecording(t, err)
	}
	var env struct{ Data timelineResponse }
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	report := env.Data.ReportData.Report
	if report == nil {
		t.Fatal("the recorded timeline holds no report")
	}
	raw, err = os.ReadFile("../../testdata/masterdata.json")
	if err != nil {
		t.Fatalf("the recording has no masterdata.json: %v", err)
	}
	var master struct{ Data masterDataResponse }
	if err := json.Unmarshal(raw, &master); err != nil {
		t.Fatal(err)
	}
	report.MasterData = master.Data.ReportData.Report.MasterData
	return report, recordedFight(t, recordedKillID)
}

// skipWithoutRecording skips a recording-backed test on a machine with no
// recording — and fails it in CI, where the recording is committed, so a
// change that loses testdata/ cannot pass by skipping everything that
// reads it.
func skipWithoutRecording(t *testing.T, err error) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Fatalf("no recording in CI: %v", err)
	}
	t.Skipf("no recording: %v (record one with go run ./cmd/dev/record)", err)
}

// recordedFight reads one fight's bounds from the recorded report, so the
// goldens are pinned against the pull as the page draws it.
func recordedFight(t *testing.T, id int) Fight {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/report.json")
	if err != nil {
		t.Fatalf("the recording has no report.json: %v", err)
	}
	var env struct{ Data reportResponse }
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	for _, f := range env.Data.ReportData.Report.Fights {
		if f.ID == id {
			return f.fight()
		}
	}
	t.Fatalf("the recorded report has no fight %d", id)
	return Fight{}
}

// The recorded subject: report ExampleReport123 — the owner's raid night of
// 2026-09-23, re-recorded under #81 — with fight 29 a heroic The Coiled Altar
// kill carrying the Fire Mage as actor 5, a Holy Paladin as 11 and a Shadow
// Priest as 53, and fight 35 a heroic Ula'tek wipe the Fire Mage died on.
// Actor ids are the real ones — the redaction renames, it does not renumber —
// which is what cmd/dev/record -players takes.
//
// The file names carry each fight's start time because the recorder keys a
// timeline on it; they are named here once so that the next re-record is an
// edit to this block and nowhere else.
const (
	recordedKillID       = 29
	recordedWipeID       = 35
	recordedFireMage     = 5
	recordedHolyPaladin  = 11
	recordedShadowPriest = 53

	recordedKillFile  = "../../testdata/timeline-29-5-5627434.json"
	recordedWipeFile  = "../../testdata/timeline-35-5-9561554.json"
	recordedKillFight = "../../testdata/fight-29.json"
)

// recordedWipe loads the committed recording of the pull the Fire Mage died
// on, which is the one that makes death attribution worth doing.
func recordedWipe(t *testing.T) (*timelineReport, Fight) {
	t.Helper()
	raw, err := os.ReadFile(recordedWipeFile)
	if err != nil {
		skipWithoutRecording(t, err)
	}
	var env struct{ Data timelineResponse }
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	report := env.Data.ReportData.Report
	if report == nil {
		t.Fatal("the recorded wipe holds no report")
	}
	raw, err = os.ReadFile("../../testdata/masterdata.json")
	if err != nil {
		t.Fatalf("the recording has no masterdata.json: %v", err)
	}
	var master struct{ Data masterDataResponse }
	if err := json.Unmarshal(raw, &master); err != nil {
		t.Fatal(err)
	}
	report.MasterData = master.Data.ReportData.Report.MasterData
	return report, recordedFight(t, recordedWipeID)
}

func (r *timelineReport) names() map[int]string      { return r.MasterData.names() }
func (r *timelineReport) actorNames() map[int]string { return r.MasterData.actorNames() }
func (r *timelineReport) npcs() map[int]Actor        { return r.MasterData.npcsByID() }
