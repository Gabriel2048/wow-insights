package warcraftlogs

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The sentinels a caller decides on. The set is flat on purpose: errors.Is
// walks a wrap chain, not a type tree, so there is no "base API error" to
// catch — a caller asks about the one thing it can act on.
var (
	// ErrNoCredentials: the client was built without a client ID and secret.
	ErrNoCredentials = errors.New("warcraftlogs: missing client ID or secret")
	// ErrBadCredentials: Warcraft Logs rejected the client ID and secret.
	ErrBadCredentials = errors.New("warcraftlogs: credentials rejected")
	// ErrReportNotFound: no such report, or a private one.
	ErrReportNotFound = errors.New("warcraftlogs: report not found")
	// ErrFightNotFound: the report exists but has no fight with that id.
	ErrFightNotFound = errors.New("warcraftlogs: fight not found")
	// ErrRateLimited: the hourly points budget is spent.
	ErrRateLimited = errors.New("warcraftlogs: rate limited")
	// ErrBudgetExhausted: the client refused to spend, because the budget is
	// nearly gone. Not the API's doing; the app's own guard.
	ErrBudgetExhausted = errors.New("warcraftlogs: budget nearly exhausted")
	// ErrUpstream: Warcraft Logs answered with something other than the
	// data asked for, or did not answer.
	ErrUpstream = errors.New("warcraftlogs: upstream failure")
)

// BudgetError is the guard refusing an expensive query, with what it knew.
type BudgetError struct {
	Spent   float64
	Limit   int
	ResetIn time.Duration
}

func (e *BudgetError) Error() string {
	return fmt.Sprintf("warcraftlogs: %.0f of %d points spent this hour; resets in %s", e.Spent, e.Limit, e.ResetIn.Round(time.Second))
}

func (e *BudgetError) Unwrap() error { return ErrBudgetExhausted }

// maxBodyKept bounds how much of an upstream body an APIError carries. It is
// for a log line, and a log line does not want a megabyte of HTML.
const maxBodyKept = 512

// APIError is what the API actually said, for the log and never for a page.
// Error() deliberately carries no upstream text: in Go the error string is
// the payload, and an upstream body in it would be one err.Error() away from
// a user's screen. The detail is on the fields.
type APIError struct {
	Status     int           // the HTTP status, 200 when the failure was inside GraphQL
	Body       string        // the response body, trimmed and capped, when it was not GraphQL
	Messages   []string      // the GraphQL errors, when there were any
	Paths      []string      // the fields those errors sit on, as dotted paths
	RetryAfter time.Duration // what Retry-After said, when it said anything
}

func (e *APIError) Error() string {
	switch {
	case e.Status == 429:
		return "warcraftlogs: rate limited"
	case len(e.Messages) > 0:
		return fmt.Sprintf("warcraftlogs: the API reported %d error(s) on %s", len(e.Messages), strings.Join(e.Paths, ", "))
	}
	return fmt.Sprintf("warcraftlogs: the API returned HTTP %d", e.Status)
}

// Unwrap maps the failure onto the sentinel a caller can act on.
func (e *APIError) Unwrap() error {
	switch {
	case e.Status == 429, e.mentionsRateLimit():
		return ErrRateLimited
	case e.onReport():
		// An error on the report itself is the API saying the report is
		// not there for this client — nonexistent, or private. Not an outage.
		return ErrReportNotFound
	}
	return ErrUpstream
}

// mentionsRateLimit reports whether a GraphQL error was the points budget.
// The API's wording is not documented; this matches what a rate limit
// message can reasonably say, and a 429 is handled by status regardless.
func (e *APIError) mentionsRateLimit() bool {
	for _, m := range e.Messages {
		l := strings.ToLower(m)
		if strings.Contains(l, "rate limit") || strings.Contains(l, "too many requests") {
			return true
		}
	}
	return false
}

// Fields names the GraphQL fields the errors sit on — the last element of
// each path — for telling a user which part of a page is missing.
func (e *APIError) Fields() []string {
	var fields []string
	for _, p := range e.Paths {
		if i := strings.LastIndex(p, "."); i >= 0 {
			p = p[i+1:]
		}
		if p != "" && !slices.Contains(fields, p) {
			fields = append(fields, p)
		}
	}
	return fields
}

// notFound decides whether a query that decoded no report was a report that
// does not exist. The API says so two ways: a null report with no error, or —
// what a real request gets — a null report beside a GraphQL error on
// reportData.report ("This report does not exist.", or the permission
// message for a private one). Anything else that failed is returned as it
// was. The API's own words stay in the chain, for the log.
func notFound(err error, code string) error {
	var apiErr *APIError
	switch {
	case err == nil:
		return fmt.Errorf("%w: %s", ErrReportNotFound, code)
	case errors.As(err, &apiErr) && errors.Is(err, ErrReportNotFound):
		return fmt.Errorf("%w: %s", err, code)
	}
	return err
}

// onReport reports whether every GraphQL error sits on the report itself
// rather than on a field inside it.
func (e *APIError) onReport() bool {
	if len(e.Paths) == 0 {
		return false
	}
	for _, p := range e.Paths {
		if p != "reportData.report" {
			return false
		}
	}
	return true
}

// keepBody trims and caps an upstream body for the log.
func keepBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > maxBodyKept {
		s = s[:maxBodyKept] + "…"
	}
	return s
}

// retryAfter reads a Retry-After header given in seconds.
func retryAfter(header string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 0
}
