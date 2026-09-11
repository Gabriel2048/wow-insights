// Package fixture records real Warcraft Logs API responses to disk and plays
// them back, so that a fight page can be rendered on a machine with no
// credentials.
//
// Both halves sit at the HTTP transport, underneath the real client. That is
// the load-bearing choice: a fake client in package main could return a
// Timeline, but not a laid-out one — layout() is unexported and runs only
// inside the client — so the offline page would come from a path no user
// takes. Swapping the wire instead drives every line of production code, the
// cast pagination included, with only the network replaced.
package fixture

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Every response the client asks for is identified by the GraphQL variables it
// sends, never by the query text. Keying on the text would make every edit to
// a query a re-record, which needs credentials that agents do not have.
//
// The shapes the client sends today:
//
//	none                                   -> ratelimit
//	{code}                                 -> report
//	{code, id}                             -> fight-<id>
//	{code, id, source, start, end}         -> timeline-<id>-<source>-<start>
//
// start is part of the key because it is the pagination cursor: the first
// page starts at the fight's start time and each further page of casts at the
// nextPageTimestamp the previous one returned. The report code is deliberately
// absent from every key — see Redact.
func key(vars map[string]any) (string, error) {
	switch {
	case len(vars) == 0:
		return "ratelimit", nil
	case vars["source"] != nil:
		return "timeline-" + number(vars["id"]) + "-" + number(vars["source"]) + "-" + number(vars["start"]), nil
	case vars["id"] != nil:
		return "fight-" + number(vars["id"]), nil
	case vars["code"] != nil:
		return "report", nil
	}
	return "", fmt.Errorf("fixture: no key for variables %v", vars)
}

// number formats a JSON number the way it is written in a filename. Both the
// recorder and the replay see variables after a JSON round trip, so an int the
// client sent arrives as a float64 here — which is why 12 must not become "12.0".
func number(v any) string {
	switch n := v.(type) {
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	case json.Number:
		return n.String()
	}
	return fmt.Sprint(v)
}

// graphQLRequest is the body the client posts. Only the variables matter here.
type graphQLRequest struct {
	Variables map[string]any `json:"variables"`
}

// tokenRequest reports whether a request is for the OAuth token rather than
// for the API. The token endpoint takes a form; the API takes JSON. Deciding by
// content type keeps this independent of which URLs the client was built with.
func tokenRequest(contentType string) bool {
	return strings.HasPrefix(contentType, "application/x-www-form-urlencoded")
}
