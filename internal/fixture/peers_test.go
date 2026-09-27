package fixture

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These stand in for other players: invented, but in the position a real
// name, realm, guild and report code would occupy, so that a test can assert
// none of them reaches the disk. The subject's report sorts *after* every
// peer's on purpose — the subject must still be ExampleReport123.
const (
	subjectCode = "zzSubjectReport1"
	peerCodeA   = "aaPeerReportOne1" // two peers raided together in this one
	peerCodeB   = "bbPeerReportTwo2"
)

type peer struct {
	name, realm, guild, code string
	fight                    int
}

// peers returns three players the way a rankings page lists them. The first
// two share a report and a fight, which is what top performers who raid
// together look like, and is why a peer is keyed on their name as well.
func peers() []peer {
	return []peer{
		{"Vexmira", "Grimspire", "Ashen Covenant", peerCodeA, 7},
		// A realm and a guild that are also words in the subject's own pull,
		// the way a live page's realms and guilds were.
		{"Oruthane", "Hollowmere", "Cindermark", peerCodeA, 7},
		// A guild whose name contains a realm's and a player's, which is
		// what the top page of a real boss held: redacted realm-first, it
		// left a real word behind and the recorder refused to write.
		{"Sallowfen", "Grimspire", "Wardens of Vexmira and Grimspire", peerCodeB, 3},
	}
}

func filterFor(name string) string { return fmt.Sprintf("source.name = %q", name) }

// peerAPI answers the two operations #60 adds, plus the subject's Report so
// that a directory holding them is one Open accepts.
func peerAPI(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api", func(w http.ResponseWriter, r *http.Request) {
		var body graphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		switch body.OperationName {
		case "Report":
			_, _ = w.Write([]byte(`{"data":{"reportData":{"report":{"code":"` + subjectCode + `","title":"A night",
				"startTime":1700000000000,"endTime":1700000600000,"owner":{"name":"Oathbinder"},"zone":{"name":"The Venomous Abyss"},
				"fights":[{"id":12,"name":"The Coiled Altar","kill":true,"difficulty":4,"startTime":1000,"endTime":301000}]}}}}`))
		case "MasterData":
			// The subject's roster. Its Mage has a shorter name than every
			// peer on the rankings page, so any numbering that interleaved the
			// two would hand Testmage to a peer. Its abilities carry a peer's
			// realm and guild as ordinary words, which are not people here.
			_, _ = w.Write([]byte(`{"data":{"reportData":{"report":{"masterData":{
				"abilities":[{"gameID":391403,"name":"Mind Flay: Cindermark","icon":"x","type":"32"},
					{"gameID":1236341,"name":"Hollowmere's Guillotine Technique","icon":"x","type":"1"}],"npcs":[],
				"actors":[{"id":5,"name":"Emberly","type":"Player","subType":"Mage","server":"Grimspire"}]}}}}}`))
		case "Rankings":
			var rows []string
			for i, p := range peers() {
				rows = append(rows, fmt.Sprintf(`{"name":%q,"class":"Mage","spec":"Fire","amount":%d,"bracketData":324,
					"server":{"id":%d,"name":%q,"region":"EU"},"guild":{"id":%d,"name":%q,"faction":0},"faction":0,
					"report":{"code":%q,"fightID":%d,"startTime":1700000000000},"duration":300000,
					"gear":[{"id":1,"name":"Ember of the Altar","itemLevel":324}],"talents":[{"talentID":7,"points":1}]}`,
					p.name, 900000-i, 100+i, p.realm, 200+i, p.guild, p.code, p.fight))
			}
			_, _ = w.Write([]byte(`{"data":{"worldData":{"encounter":{"characterRankings":{"page":1,"hasMorePages":false,"count":3,
				"rankings":[` + strings.Join(rows, ",") + `]}}}}}`))
		case "Peer":
			filter, _ := body.Variables["filter"].(string)
			for _, p := range peers() {
				if filter != filterFor(p.name) {
					continue
				}
				// Casts come back keyed by actor id, nothing inlined; the
				// damage table under the same filter carries the name.
				_, _ = fmt.Fprintf(w, `{"data":{"reportData":{"report":{
					"casts":{"data":[{"timestamp":2000,"type":"cast","sourceID":41,"targetID":-1,"abilityGameID":133}],"nextPageTimestamp":null},
					"damage":{"data":{"entries":[{"id":41,"name":%q,"guid":55512345,"type":"Mage","icon":"Mage-Fire",
						"total":270000000,"activeTime":290000,"gear":[],"talents":[]}],"totalTime":300000}}}}}}`, p.name)
				return
			}
			t.Errorf("a Peer request named nobody the fake knows")
		default:
			t.Errorf("unexpected operation %q", body.OperationName)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// post sends one GraphQL request through rt, as the client would.
func post(t *testing.T, rt http.RoundTripper, url, op string, vars map[string]any) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"operationName": op, "query": "query " + op + " { }", "variables": vars})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url+"/api", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

func rankingsVars() map[string]any {
	return map[string]any{"encounter": 3429, "difficulty": 4, "className": "Mage", "specName": "Fire", "bracket": 18, "page": 1}
}

// recordPeers records the subject's report, a rankings page and every peer's
// pull, and returns the directory, the way #86's cohort fetch will.
func recordPeers(t *testing.T, withRankings bool) (string, error) {
	t.Helper()
	srv := peerAPI(t)
	rec := NewRecorder()
	post(t, rec, srv.URL, "Report", map[string]any{"code": subjectCode})
	if withRankings {
		post(t, rec, srv.URL, "Rankings", rankingsVars())
	}
	for _, p := range peers() {
		post(t, rec, srv.URL, "Peer", map[string]any{"code": p.code, "id": p.fight, "filter": filterFor(p.name)})
	}
	dir := t.TempDir()
	return dir, rec.Write(dir)
}

func readAll(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = string(b)
	}
	return files
}

// **The whole point of #85.** A recording of other players' pulls holds none
// of them: no name, no realm, no guild, no report code — in the files and in
// the file names, which are as public as the files.
func TestARecordingOfOtherPlayersNamesNobody(t *testing.T) {
	dir, err := recordPeers(t, true)
	if err != nil {
		t.Fatalf("Write refused a recording it can redact: %v", err)
	}
	files := readAll(t, dir)
	if len(files) != 5 {
		t.Errorf("wrote %d files, want the report, the rankings page and three peers", len(files))
	}
	var real []string
	for _, p := range peers() {
		real = append(real, p.name, p.realm, p.guild, p.code)
	}
	real = append(real, subjectCode, "Oathbinder")
	for name, body := range files {
		for _, r := range real {
			if strings.Contains(strings.ToLower(name), strings.ToLower(r)) {
				t.Errorf("the file name %s carries a real value", name)
			}
			if strings.Contains(strings.ToLower(body), strings.ToLower(r)) {
				t.Errorf("%s carries a real value", name)
			}
		}
	}
}

// The same player is the same pseudonym on the rankings page and in their
// own pull. Without that, the cohort the app builds from the page could not be
// joined to the pulls it fetched, and the replay would be a different
// comparison from the one that was recorded.
func TestAPeerIsTheSamePseudonymEverywhere(t *testing.T) {
	dir, err := recordPeers(t, true)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Data struct {
			WorldData struct {
				Encounter struct {
					CharacterRankings struct {
						Rankings []struct {
							Name   string
							Report struct {
								Code    string
								FightID int
							}
						}
					}
				}
			}
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, filename(mustKey(t, "Rankings", rankingsVars()))+".json"))
	if err != nil {
		t.Fatalf("no rankings file under the name the replay will ask for: %v", err)
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	rows := page.Data.WorldData.Encounter.CharacterRankings.Rankings
	if len(rows) != 3 {
		t.Fatalf("the rankings page holds %d rows, want 3", len(rows))
	}
	for _, row := range rows {
		// This is exactly what the replay does: ask for the pull by the
		// pseudonyms the page gave it.
		k := mustKey(t, "Peer", map[string]any{"code": row.Report.Code, "id": row.Report.FightID, "filter": filterFor(row.Name)})
		body, err := os.ReadFile(filepath.Join(dir, filename(k)+".json"))
		if err != nil {
			t.Errorf("%s's pull is not where the replay will look for it: %v", row.Name, err)
			continue
		}
		if !strings.Contains(string(body), `"name":"`+row.Name+`"`) {
			t.Errorf("%s's own pull does not call them %s", row.Name, row.Name)
		}
	}
}

// Two peers who raided together share a report and a fight. They are two
// files, and the recorder refuses outright rather than let one silently
// replace the other.
func TestTwoPeersFromOneFightAreTwoFiles(t *testing.T) {
	dir, err := recordPeers(t, true)
	if err != nil {
		t.Fatal(err)
	}
	var peerFiles int
	for name := range readAll(t, dir) {
		if strings.HasPrefix(name, "peer-") {
			peerFiles++
		}
	}
	if peerFiles != 3 {
		t.Errorf("%d peer files, want 3 — two peers from one fight collapsed into one", peerFiles)
	}
}

// A peer's pull recorded without the rankings page that names them has no
// rule to replace their name with, so it cannot be written — and the refusal
// must not say the name it is refusing to write.
func TestAPeerWithoutTheirRankingIsRefusedWithoutBeingNamed(t *testing.T) {
	_, err := recordPeers(t, false)
	if err == nil {
		t.Fatal("a peer's pull was written with no rankings page to redact their name against")
	}
	for _, p := range peers() {
		for _, v := range []string{p.name, p.code, p.realm} {
			if strings.Contains(err.Error(), v) {
				t.Errorf("the refusal quotes a real value: %v", err)
			}
		}
	}
}

// The subject is ExampleReport123 however the peers' codes sort. Every test
// and the offline server ask for the subject by that name.
func TestTheSubjectKeepsItsPseudonymWhenPeersSortFirst(t *testing.T) {
	dir, err := recordPeers(t, true)
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Data struct {
			ReportData struct{ Report struct{ Code string } }
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if got := report.Data.ReportData.Report.Code; got != FakeCode {
		t.Errorf("the subject's report is %s, want %s", got, FakeCode)
	}
}

// The replay serves a peer's pull even though its report is not the
// recording's own. Any other request for a foreign report still gets the real
// service's "not found".
func TestTheReplayServesAPeerFromAnotherReport(t *testing.T) {
	dir, err := recordPeers(t, true)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var page map[string]any
	raw, _ := os.ReadFile(filepath.Join(dir, filename(mustKey(t, "Rankings", rankingsVars()))+".json"))
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	row := lookup(page, "data", "worldData", "encounter", "characterRankings", "rankings").([]any)[0].(map[string]any)
	code := lookup(row, "report", "code").(string)
	fight := lookup(row, "report", "fightID")
	name := row["name"].(string)

	served := replayed(t, replay, "Peer", map[string]any{"code": code, "id": fight, "filter": filterFor(name)})
	if !strings.Contains(served, `"activeTime"`) {
		t.Errorf("the replay did not serve the peer's pull: %s", served)
	}
	if other := replayed(t, replay, "Fight", map[string]any{"code": code, "id": fight}); !strings.Contains(other, `"report":null`) {
		t.Errorf("a non-peer request for a foreign report got %s, want the service's not-found", other)
	}
}

// File names for the operations that were recorded before this change did not
// move, so the committed recording still replays without being re-taken.
func TestExistingFileNamesAreUnchanged(t *testing.T) {
	for _, k := range []string{"ratelimit", "report", "masterdata", "fight-29", "timeline-29-5-5627434", "castpage-35-5-9561554"} {
		if got := filename(k); got != k {
			t.Errorf("filename(%q) = %q; the committed recording would no longer be found", k, got)
		}
	}
}

func mustKey(t *testing.T, op string, vars map[string]any) string {
	t.Helper()
	// Through JSON, the way both the recorder and the replay see variables.
	raw, _ := json.Marshal(vars)
	var round map[string]any
	_ = json.Unmarshal(raw, &round)
	k, err := key(op, round)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func replayed(t *testing.T, r *Replay, op string, vars map[string]any) string {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"operationName": op, "variables": vars})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://replay/api", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.RoundTrip(req)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// Adding a comparison to a recording never renames anybody already in it. The
// kill is recorded with its peers and the wipe without, in separate runs; if
// the peers were numbered in among the subject's roster the player would be
// Testmage in one file and Testmage7 in the other, and nothing joins them.
func TestAddingPeersNeverRenamesTheSubjectsRoster(t *testing.T) {
	roster := func(withPeers bool) string {
		t.Helper()
		srv := peerAPI(t)
		rec := NewRecorder()
		post(t, rec, srv.URL, "Report", map[string]any{"code": subjectCode})
		post(t, rec, srv.URL, "MasterData", map[string]any{"code": subjectCode})
		if withPeers {
			post(t, rec, srv.URL, "Rankings", rankingsVars())
			for _, p := range peers() {
				post(t, rec, srv.URL, "Peer", map[string]any{"code": p.code, "id": p.fight, "filter": filterFor(p.name)})
			}
		}
		dir := t.TempDir()
		if err := rec.Write(dir); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "masterdata.json"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	alone, compared := roster(false), roster(true)
	if !strings.Contains(alone, `"name":"Testmage"`) {
		t.Fatalf("the subject's Mage is not Testmage even with no peers: %s", alone)
	}
	for _, spell := range []string{"Mind Flay: Cindermark", "Hollowmere's Guillotine Technique"} {
		if !strings.Contains(compared, spell) {
			t.Errorf("a peer's realm or guild was replaced in the subject's own pull, rewriting %q: %s", spell, compared)
		}
	}
	if alone != compared {
		t.Errorf("recording a comparison renamed the subject's roster:\n alone:    %s\n compared: %s", alone, compared)
	}
}
