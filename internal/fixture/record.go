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
	"sync"
)

// Recorder is an http.RoundTripper that lets every request through to the
// real transport and keeps a copy of each API response, keyed the way Replay
// will look it up. The token exchange is passed through and not kept: a
// recording must never hold a credential, and Replay mints its own.
type Recorder struct {
	next http.RoundTripper

	mu        sync.Mutex
	exchanges map[string][]byte
}

// NewRecorder returns a Recorder over the default transport.
func NewRecorder() *Recorder {
	return &Recorder{next: http.DefaultTransport, exchanges: map[string][]byte{}}
}

// Client returns an http.Client whose every exchange is recorded.
func (r *Recorder) Client() *http.Client {
	return &http.Client{Transport: r}
}

// RoundTrip forwards the request and keeps a successful API response.
func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if tokenRequest(req.Header.Get("Content-Type")) {
		return r.next.RoundTrip(req)
	}

	// The body is consumed to read the variables, so the request is sent
	// with a fresh copy of the same bytes.
	payload, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(payload))
	var body graphQLRequest
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, fmt.Errorf("fixture: decode request: %w", err)
	}
	k, err := key(body.Variables)
	if err != nil {
		return nil, err
	}

	resp, err := r.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	// A failed query is the client's to report; only what it will accept is
	// worth keeping. GraphQL errors arrive inside a 200 and are kept too — the
	// client rejects them on replay exactly as it did live.
	if resp.StatusCode == http.StatusOK {
		r.mu.Lock()
		r.exchanges[k] = data
		r.mu.Unlock()
	}
	return resp, nil
}

// Write redacts everything recorded and writes it into dir, one file per
// exchange. Nothing is written until every file has passed the redaction
// check, so a refusal leaves no partial recording behind.
//
// A directory holds exactly one report. Writing a second report's fights into
// it would leave a report.json that lists fights the directory cannot serve,
// so a directory that already holds a different report is refused.
func (r *Recorder) Write(dir string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.exchanges) == 0 {
		return errors.New("fixture: nothing was recorded")
	}

	bodies := make([][]byte, 0, len(r.exchanges))
	for _, body := range r.exchanges {
		bodies = append(bodies, body)
	}
	red, err := newRedactor(bodies)
	if err != nil {
		return err
	}
	files := make(map[string][]byte, len(r.exchanges))
	for k, body := range r.exchanges {
		out, err := red.apply(body)
		if err != nil {
			return fmt.Errorf("%s.json: %w", k, err)
		}
		files[k+".json"] = out
	}

	if report, ok := files["report.json"]; ok {
		if err := sameReport(filepath.Join(dir, "report.json"), report); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// sameReport refuses when path already holds a report that is not the one
// about to be written. The code is the same in every recording once redacted,
// so the start time is what tells two reports apart.
func sameReport(path string, incoming []byte) error {
	existing, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if startTime(existing) != startTime(incoming) {
		return fmt.Errorf("fixture: %s already holds a different report; a fixture directory holds one report, so record into another directory or delete this one first", filepath.Dir(path))
	}
	return nil
}

func startTime(body []byte) string {
	tree, err := decode(body)
	if err != nil {
		return ""
	}
	n, _ := lookup(tree, "data", "reportData", "report", "startTime").(json.Number)
	return n.String()
}
