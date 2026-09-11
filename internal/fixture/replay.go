package fixture

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
)

// Replay is an http.RoundTripper that answers the client from a recorded
// directory and never reaches the network: a request nothing was recorded for
// is an error, not a fall-through.
type Replay struct {
	dir  string
	code string
	// fights maps each recorded fight to the players a timeline was recorded
	// for, so the startup log can list every page that will actually render.
	fights map[int][]int
}

// Open reads a directory written by the recorder. It fails up front, rather
// than on the first request, when the directory holds no report.
func Open(dir string) (*Replay, error) {
	body, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		return nil, fmt.Errorf("fixture: %w (is %s a directory written by 'go run ./cmd/dev/record'?)", err, dir)
	}
	var report struct {
		Data struct {
			ReportData struct {
				Report struct {
					Code string `json:"code"`
				} `json:"report"`
			} `json:"reportData"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		return nil, fmt.Errorf("fixture: decode %s: %w", filepath.Join(dir, "report.json"), err)
	}
	code := report.Data.ReportData.Report.Code
	if code == "" {
		return nil, fmt.Errorf("fixture: %s carries no report code", filepath.Join(dir, "report.json"))
	}

	r := &Replay{dir: dir, code: code, fights: map[int][]int{}}
	names, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	fight := regexp.MustCompile(`^fight-(\d+)\.json$`)
	timeline := regexp.MustCompile(`^timeline-(\d+)-(\d+)-\d+\.json$`)
	for _, name := range names {
		base := filepath.Base(name)
		if m := fight.FindStringSubmatch(base); m != nil {
			id, _ := strconv.Atoi(m[1])
			if _, ok := r.fights[id]; !ok {
				r.fights[id] = nil // present even with no timelines
			}
		}
		if m := timeline.FindStringSubmatch(base); m != nil {
			id, _ := strconv.Atoi(m[1])
			source, _ := strconv.Atoi(m[2])
			if !slices.Contains(r.fights[id], source) {
				r.fights[id] = append(r.fights[id], source)
			}
		}
	}
	for id := range r.fights {
		slices.Sort(r.fights[id])
	}
	return r, nil
}

// Code is the report code the recording answers to.
func (r *Replay) Code() string { return r.code }

// Dir is the directory the recording is read from.
func (r *Replay) Dir() string { return r.dir }

// Fights lists the recorded fights in id order, each with the players a
// timeline was recorded for.
func (r *Replay) Fights() []Fight {
	fights := make([]Fight, 0, len(r.fights))
	for id, players := range r.fights {
		fights = append(fights, Fight{ID: id, Players: players})
	}
	slices.SortFunc(fights, func(a, b Fight) int { return a.ID - b.ID })
	return fights
}

// Fight is one recorded fight and the players it has timelines for.
type Fight struct {
	ID      int
	Players []int
}

// Client returns an http.Client that talks only to the recording.
func (r *Replay) Client() *http.Client {
	return &http.Client{Transport: r}
}

// RoundTrip serves the token endpoint with a token that means nothing, and
// every API request from the file its variables name.
func (r *Replay) RoundTrip(req *http.Request) (*http.Response, error) {
	if tokenRequest(req.Header.Get("Content-Type")) {
		return respond(req, `{"access_token":"fixture","expires_in":3600}`), nil
	}

	var body graphQLRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("fixture: decode request: %w", err)
	}
	// A code the recording does not hold gets the same answer the real
	// service gives for a report it does not hold, so the page shows the real
	// "not found" message rather than a fixture-specific one.
	if code, ok := body.Variables["code"].(string); ok && code != r.code {
		return respond(req, `{"data":{"reportData":{"report":null}}}`), nil
	}
	k, err := key(body.OperationName, body.Variables)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(r.dir, k+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("fixture: nothing recorded for %s (no %s in %s)", k, k+".json", r.dir)
	}
	if err != nil {
		return nil, err
	}
	return respond(req, string(data)), nil
}

func respond(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
		Request:    req,
	}
}
