package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

// Cloud Logging grades on severity and message; slog writes level and msg.
// Without the rename every line is DEFAULT severity and nothing can be
// alerted on, which is the failure #7 needs to never see.
func TestLoggerSpeaksCloudLogging(t *testing.T) {
	var buf bytes.Buffer
	newLogger(&buf, "my-project").With("trace_id", "abc123").Warn("timeline failed", "fight", 1)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("the log line is not JSON: %v\n%s", err, buf.String())
	}
	for key, want := range map[string]any{
		"severity":                     "WARNING",
		"message":                      "timeline failed",
		"logging.googleapis.com/trace": "projects/my-project/traces/abc123",
		"fight":                        float64(1),
	} {
		if line[key] != want {
			t.Errorf("%s = %v, want %v", key, line[key], want)
		}
	}
	for _, gone := range []string{"level", "msg", "trace_id"} {
		if _, present := line[gone]; present {
			t.Errorf("%s is still present; it should have been renamed", gone)
		}
	}
}

// With no project known the trace id is still logged, under its raw name,
// rather than as a malformed resource path.
func TestLoggerKeepsTheRawTraceIDWithoutAProject(t *testing.T) {
	var buf bytes.Buffer
	newLogger(&buf, "").With("trace_id", "abc123").Info("x")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["trace_id"] != "abc123" {
		t.Errorf("trace_id = %v, want abc123", line["trace_id"])
	}
}

func TestSeverityNames(t *testing.T) {
	for l, want := range map[slog.Level]string{slog.LevelDebug: "DEBUG", slog.LevelInfo: "INFO", slog.LevelWarn: "WARNING", slog.LevelError: "ERROR", slog.LevelError + 4: "ERROR"} {
		if got := severity(l); got != want {
			t.Errorf("severity(%v) = %s, want %s", l, got, want)
		}
	}
}
