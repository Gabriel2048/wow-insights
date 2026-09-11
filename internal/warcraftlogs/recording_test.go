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
	raw, err := os.ReadFile("../../testdata/timeline-1-21-1141518.json")
	if err != nil {
		t.Skipf("no recording: %v (record one with go run ./cmd/dev/record)", err)
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
	// The fight is anchored on the first cast, which is what the goldens
	// pinned their offsets against.
	first := report.Casts.Data[0].Timestamp
	return report, Fight{StartTime: first, EndTime: first + 425000}
}

func (r *timelineReport) names() map[int]string {
	names := map[int]string{}
	for _, a := range r.MasterData.Abilities {
		names[a.GameID] = a.Name
	}
	return names
}

func (r *timelineReport) actorNames() map[int]string {
	actors := map[int]string{}
	for _, a := range r.MasterData.Actors {
		actors[a.ID] = a.Name
	}
	return actors
}

func (r *timelineReport) npcs() map[int]Actor {
	npcs := map[int]Actor{}
	for _, n := range r.MasterData.NPCs {
		npcs[n.ID] = n
	}
	return npcs
}
