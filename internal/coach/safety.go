package coach

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"wowinsight/internal/warcraftlogs"
)

// AGENTS.md's rule is that no real character name, guild name, server or
// report code appears in tests, fixtures, doc comments or user-facing
// strings. This package adds the destination that rule was written before
// there was: **an outbound request body**. It is the worst of the five. The
// other four are in the repository, where a reviewer sees them and a grep
// finds them; this one leaves the process, reaches a third party, and is
// invisible here forever after.
//
// So the sheet is built to carry no identity (see facts.go) and then checked
// as though it were built by somebody who forgot. The check is not an
// allowlist of fields — internal/fixture/safety.go explains at length why an
// allowlist of *known* shapes is the wrong way round — it is the roster of
// this very fight, read back out of the response that produced the sheet and
// looked for in the bytes about to go out. A name that arrived through a path
// nobody enumerated is still that player's name, and it is still in the list.

// minChecked is the shortest name worth searching for. Two-character names
// exist in the game and match inside ordinary words constantly; a false
// refusal costs this page its prose, which is cheap, but a rule that refuses
// every pull is not a rule, it is an outage.
const minChecked = 3

// identity is everything about one report that must not leave the process.
type identity struct {
	// values are the strings to look for. kinds[i] says what values[i] is, so
	// a refusal can name what leaked without printing it — the same reason
	// internal/fixture's rule carries a kind.
	values []string
	kinds  []string
}

// identityOf collects the roster of the fight the sheet describes.
func identityOf(in Input) identity {
	var id identity
	add := func(kind, value string) {
		value = strings.TrimSpace(value)
		if len([]rune(value)) < minChecked {
			return
		}
		if slices.Contains(id.values, value) {
			return
		}
		id.values = append(id.values, value)
		id.kinds = append(id.kinds, kind)
	}
	if in.Detail != nil {
		add("report code", in.Detail.ReportCode)
		add("report title", in.Detail.ReportTitle)
		// Every player in the fight, not only the one being analysed: the
		// lust and raid-cooldown lanes name whoever pressed them.
		for _, p := range in.Detail.Players {
			add("a player's name", p.Name)
			add("a player's server", p.Server)
		}
	}
	add("a player's name", in.Player.Name)
	add("a player's server", in.Player.Server)
	add("report code", in.Timeline.Subject.ReportCode)
	return id
}

// check refuses a body carrying anything from the roster, and refuses a
// finding that carries one too — the findings go out with the sheet, and a
// rule that one day quotes a player's name would otherwise walk straight past
// this.
//
// It errs towards refusing. A raider called "Frost" makes every Frost Mage's
// page fall back to the deterministic wording, which is a worse-written page
// and not a wrong one; the other direction puts twenty raiders' names in a
// third party's request log, where nobody will ever think to look for them.
func (id identity) check(body []byte, found []warcraftlogs.Finding) error {
	haystacks := []string{string(body)}
	for _, f := range found {
		haystacks = append(haystacks, f.Title, f.Detail)
		for _, e := range f.Evidence {
			haystacks = append(haystacks, e.Label, e.Value)
		}
	}
	for _, hay := range haystacks {
		lower := strings.ToLower(hay)
		for i, v := range id.values {
			if containsWord(lower, strings.ToLower(v)) {
				return fmt.Errorf("%w: it holds %s", ErrWouldLeak, id.kinds[i])
			}
		}
	}
	return nil
}

// containsWord reports whether needle appears in haystack on word boundaries,
// so that a player called "Ana" is not found inside "analysis" — while a
// player called "Ana" written as "Ana's" still is.
func containsWord(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	for from := 0; ; {
		i := strings.Index(haystack[from:], needle)
		if i < 0 {
			return false
		}
		at := from + i
		// Decoded as runes, not bytes: a name with an accent in it is one
		// character to a reader and two bytes to Index, and a byte-wise
		// boundary test reads the tail of a multi-byte rune as a letter.
		prev, _ := utf8.DecodeLastRuneInString(haystack[:at])
		next, _ := utf8.DecodeRuneInString(haystack[at+len(needle):])
		if !wordRune(prev) && !wordRune(next) {
			return true
		}
		from = at + 1
	}
}

// wordRune reports whether r is part of a word for the purposes above. An
// apostrophe is not, so a possessive still matches the name inside it, and
// RuneError — what DecodeRune returns at either end of the string — is not,
// so a match at the very start or end of the body counts as bounded.
func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
