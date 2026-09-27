// Package fixture records real Warcraft Logs API responses to disk and plays
// them back, so that a fight page can be rendered on a machine with no
// credentials.
//
// Both halves sit at the HTTP transport, underneath the real client. That is
// the load-bearing choice: a fake client would hand the page a Timeline that
// no decoding, paging or building ever touched, so the offline page would
// come from a path no user takes. Swapping the wire instead drives every
// line of production code, the cast pagination included, with only the
// network replaced.
package fixture

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Every response the client asks for is identified by the operation's name
// and the GraphQL variables it sends, never by the query text. Keying on the
// text would make every edit to a query a re-record, which needs credentials
// that agents do not have; the name is what GraphQL itself calls the
// document, and two operations that take the same variables (Report and
// MasterData both take {code}) need it to tell them apart.
//
//	RateLimit                                    -> ratelimit
//	Report {code}                                -> report
//	MasterData {code}                            -> masterdata
//	Fight {code, id}                             -> fight-<id>
//	Timeline {code, id, source, start, end, ...} -> timeline-<id>-<source>-<start>
//	CastPage {code, id, source, start, end}      -> castpage-<id>-<source>-<start>
//	Rankings {encounter, difficulty, className, specName, bracket, page}
//	                                             -> rankings-<encounter>-<difficulty>-<class>-<spec>-<bracket>-<page>
//	Peer {code, id, filter}                      -> peer-<id>-<code>-<filter>
//
// start is part of the cast keys because it is the pagination cursor. The
// subject's report code is deliberately absent from every key — see Redact.
// The filter variables are absent too: they are derived from the spec tables,
// not from the request, and a change to them is not a different recording.
//
// **Peer is the exception, and it is keyed on identity on purpose.** It is
// another player's pull, found by their report code and a filter naming them —
// and two of them can share a report and a fight, because top performers raid
// together (four of the top hundred on one boss did). So the code and the name
// are what tell two peers apart, and both are real when the recorder sees the
// request and fake when the replay does. The key is therefore only ever turned
// into a file name after redaction: the recorder rewrites it with the same
// rules it applies to the bodies (see Recorder.Write), and the replay, whose
// request already carries the pseudonyms, arrives at the same name.
func key(op string, vars map[string]any) (string, error) {
	name := strings.ToLower(op)
	switch op {
	case "RateLimit", "Report", "MasterData":
		return name, nil
	case "Fight":
		return name + "-" + number(vars["id"]), nil
	case "Timeline", "CastPage":
		return name + "-" + number(vars["id"]) + "-" + number(vars["source"]) + "-" + number(vars["start"]), nil
	case "Rankings":
		return name + "-" + number(vars["encounter"]) + "-" + number(vars["difficulty"]) + "-" +
			fmt.Sprint(vars["className"]) + "-" + fmt.Sprint(vars["specName"]) + "-" +
			number(vars["bracket"]) + "-" + number(vars["page"]), nil
	case "Peer":
		return name + "-" + number(vars["id"]) + "-" + fmt.Sprint(vars["code"]) + "-" + fmt.Sprint(vars["filter"]), nil
	case "":
		return "", fmt.Errorf("fixture: the request names no operation")
	}
	return "", fmt.Errorf("fixture: no key for operation %s", op)
}

// filename turns a key into the name of the file it is kept in: lowercase,
// with every run of anything but a letter or a digit collapsed to one hyphen.
// A key built from plain numbers passes through unchanged, which is why the
// existing recording's file names did not move when this was added. A Peer key
// carries a filter expression — `source.name = "Testmage3"` — and needs it.
//
// It must only ever see a key that has already been redacted. The recorder
// guarantees that; the replay's keys are made from pseudonyms to begin with.
func filename(k string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(k) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// number formats a JSON number the way it is written in a filename. Both the
// recorder and the replay see variables after a JSON round trip, so an int the
// client sent arrives as a float64 here — which is why 12 must not become "12.0".
func number(v any) string {
	if n, ok := v.(float64); ok {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// graphQLRequest is the body the client posts. The operation name and the
// variables are what a recording is keyed on.
type graphQLRequest struct {
	OperationName string         `json:"operationName"`
	Variables     map[string]any `json:"variables"`
}

// tokenRequest reports whether a request is for the OAuth token rather than
// for the API. The token endpoint takes a form; the API takes JSON. Deciding by
// content type keeps this independent of which URLs the client was built with.
func tokenRequest(contentType string) bool {
	return strings.HasPrefix(contentType, "application/x-www-form-urlencoded")
}
