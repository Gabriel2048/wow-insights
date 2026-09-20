package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"wowinsight/internal/coach"
	"wowinsight/internal/warcraftlogs"
)

// A problem is what a failure looks like to the person who hit it: a status
// and one fixed sentence. It never carries what Warcraft Logs said — that is
// on the error's fields and goes to the log with the request id.
type problem struct {
	status  int
	message string
	// level is how loudly to log it. A user asking for a fight that does
	// not exist is not an incident; Warcraft Logs answering 503 is.
	level slog.Level
}

// classify maps an error onto what to tell the user and how to log it. The
// sentinels are checked with errors.Is, which walks the wrap chain; the one
// field a message needs — how long until the budget resets — is read with
// errors.As. There is no base type to catch and no need for one.
func classify(err error) problem {
	var apiErr *warcraftlogs.APIError
	switch {
	case errors.Is(err, warcraftlogs.ErrReportNotFound):
		return problem{http.StatusNotFound, "That report was not found. It may be private, or the link may be wrong.", slog.LevelInfo}
	case errors.Is(err, warcraftlogs.ErrFightNotFound):
		return problem{http.StatusNotFound, "That fight is not in this report.", slog.LevelInfo}
	case errors.Is(err, warcraftlogs.ErrRateLimited):
		message := "Warcraft Logs' hourly budget for this app is spent. Try again later."
		if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
			message = fmt.Sprintf("Warcraft Logs' hourly budget for this app is spent. Try again in about %d minutes.", int(apiErr.RetryAfter.Round(time.Minute).Minutes()))
		}
		return problem{http.StatusServiceUnavailable, message, slog.LevelWarn}
	case errors.Is(err, errJobsBusy):
		// Ours, not the upstream's. Without this it falls through to "Warcraft
		// Logs did not answer properly", which blames someone else for this
		// server's own queue.
		return problem{http.StatusServiceUnavailable, "This server is already running as many analyses as it can. Try again in a moment.", slog.LevelWarn}
	case errors.Is(err, warcraftlogs.ErrBudgetExhausted):
		message := "Warcraft Logs' hourly budget for this app is nearly spent, so this page is on hold. Try again later."
		var budgetErr *warcraftlogs.BudgetError
		if errors.As(err, &budgetErr) && budgetErr.ResetIn > 0 {
			message = fmt.Sprintf("Warcraft Logs' hourly budget for this app is nearly spent, so this page is on hold. Try again in about %d minutes.", max(1, int(budgetErr.ResetIn.Round(time.Minute).Minutes())))
		}
		return problem{http.StatusServiceUnavailable, message, slog.LevelWarn}
	// The coaching model's failures. They are rows here rather than handled
	// where they happen for two reasons: the fallback below this switch names
	// Warcraft Logs, so anything reaching it blames the wrong vendor for an
	// Anthropic outage; and none of these is a page failure — the findings
	// were computed before the model was asked, so every one of these
	// sentences is a notice on a page that otherwise worked. The status is
	// what the level is chosen against, and nothing renders it.
	case errors.Is(err, coach.ErrUntrustworthy):
		// Not an outage. The model said something the evidence does not
		// support and was dropped, which is this page working as designed —
		// but it is worth an Error line, because a model that keeps doing
		// this means the prompt or the rules have drifted.
		return problem{http.StatusOK, "The findings below are in the analyser's own words: what the model wrote did not match the evidence, so it was dropped.", slog.LevelError}
	case errors.Is(err, coach.ErrWouldLeak):
		// This one is a defect in internal/coach, not a condition. Nothing
		// was sent, which is the right outcome, and it must be loud.
		return problem{http.StatusOK, "The findings below are in the analyser's own words.", slog.LevelError}
	case errors.Is(err, coach.ErrBadKey):
		return problem{http.StatusOK, "The findings below are in the analyser's own words: this server's key for the writing model is missing or rejected.", slog.LevelError}
	case errors.Is(err, coach.ErrModelBusy), errors.Is(err, coach.ErrModelUnavailable), errors.Is(err, coach.ErrDeclined):
		return problem{http.StatusOK, "The findings below are in the analyser's own words: the model that usually writes them up was not available.", slog.LevelWarn}
	case errors.Is(err, warcraftlogs.ErrNoCredentials), errors.Is(err, warcraftlogs.ErrBadCredentials):
		return problem{http.StatusServiceUnavailable, "This server cannot reach Warcraft Logs: its credentials are missing or rejected.", slog.LevelError}
	case errors.Is(err, context.DeadlineExceeded):
		return problem{http.StatusGatewayTimeout, "Warcraft Logs took too long to answer. Try again shortly.", slog.LevelWarn}
	case errors.Is(err, context.Canceled):
		// The client went away. Nothing to say and nobody to say it to.
		return problem{0, "", slog.LevelInfo}
	}
	return problem{http.StatusBadGateway, "Warcraft Logs did not answer properly. Try again shortly.", slog.LevelError}
}

// logProblem writes the failure to the log with everything the API said —
// the one place that text belongs.
func (s *Server) logProblem(r *http.Request, p problem, what string, err error, attrs ...any) {
	// The two that are not failures of the upstream say so in the message,
	// which is what a log-based metric keys on.
	switch {
	case errors.Is(err, context.Canceled):
		what = "client went away during " + what
	case errors.Is(err, context.DeadlineExceeded):
		what = "deadline exceeded during " + what
	}
	attrs = append(attrs, "err", err, "status", p.status)
	var apiErr *warcraftlogs.APIError
	if errors.As(err, &apiErr) {
		attrs = append(attrs, "upstream_status", apiErr.Status)
		if apiErr.Body != "" {
			attrs = append(attrs, "upstream_body", apiErr.Body)
		}
		if len(apiErr.Messages) > 0 {
			attrs = append(attrs, "upstream_errors", apiErr.Messages, "upstream_paths", apiErr.Paths)
		}
	}
	s.logger(r).Log(r.Context(), p.level, what, attrs...)
}

// errorPageData is what the error template renders.
type errorPageData struct {
	Status  int
	Message string
}

// fail renders the error page for a classified failure. For a client that
// went away it writes nothing.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, p problem) {
	if p.status == 0 {
		return
	}
	s.render(w, r, p.status, "error.html", errorPageData{Status: p.status, Message: p.message})
}

// render executes a template into a buffer and commits the response only if
// the whole thing succeeded. html/template streams as it goes, and a template
// that fails halfway has already sent a 200 and half a page; buffering is
// what makes a clean 500 possible at all, and what lets Content-Length be
// set. On failure the log gets the reason and the client gets one line.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	var buf bytes.Buffer
	if err := s.tpl.Execute(&buf, name, data); err != nil {
		s.logger(r).Error("render "+name, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		s.logger(r).Info("client went away during write of "+name, "err", err)
	}
}

// writeJSON encodes before it writes, for the same reason render buffers: a
// value that fails to marshal must produce a 500, not a 200 with no body.
func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		s.logger(r).Error("encode response", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(body)+1))
	w.WriteHeader(status)
	if _, err := w.Write(append(body, '\n')); err != nil {
		s.logger(r).Info("client went away during write", "err", err)
	}
}
