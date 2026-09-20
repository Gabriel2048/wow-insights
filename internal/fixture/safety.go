package fixture

import (
	"fmt"
	"slices"
	"strings"
)

// The recorder's promise, in AGENTS.md's words, is that it "redacts every
// name, server, owner and code before writing, and refuses to write if one
// survives". This file is what makes that true of responses nobody has
// taught it about yet, rather than only of the three places it happened to
// look.
//
// The old check could only fail on a value it already had a rule for, which
// is exactly backwards: a payload the redactor never read produced no rules,
// so nothing could survive, so it passed. A peer's report reaches the
// recorder through `events(..., useActorIDs: false)`, which inlines the actor
// in every event and needs no masterData call — so there was no roster to
// build rules from and the refusal was silent. Character rankings are worse
// still: they live under `data.worldData`, which the redactor never opened,
// and carry names, guilds, servers and report codes for up to a hundred real
// people.
//
// So the rule is inverted. A recording may only hold shapes this file
// recognises, and every field known to carry someone's identity must hold a
// pseudonym. An unknown shape is refused rather than written, which means the
// next query somebody adds fails loudly here instead of quietly committing
// real names.

// knownFields are the response fields the redactor understands. The check is
// on the path, not on the value, so a query returning something new is
// refused until somebody teaches this file how to scrub it.
var knownFields = []string{
	"data.rateLimitData",
	"data.reportData.report.code",
	"data.reportData.report.title",
	"data.reportData.report.startTime",
	"data.reportData.report.endTime",
	"data.reportData.report.owner",
	"data.reportData.report.zone",
	"data.reportData.report.fights",
	"data.reportData.report.masterData",
	"data.reportData.report.rankings",
	"data.reportData.report.phases",
	"data.reportData.report.damage",
	"data.reportData.report.healing",
	"data.reportData.report.deaths",
	"data.reportData.report.taken",
	"data.reportData.report.casts",
	"data.reportData.report.lust",
	"data.reportData.report.procs",
	"data.reportData.report.cooldowns",
	"data.reportData.report.raidCDs",
	"data.reportData.report.bossCasts",
	"errors",
}

// identityFields are the paths that carry a person rather than a thing. Every
// value reached through one of these must be a pseudonym by the time a body
// is written. "name" is not enough on its own — an ability is called
// Pyroblast and a boss is called Ula'tek, both real and both fine — so the
// path is what distinguishes a player's name from a spell's.
//
// A "*" matches one path element, which is how it walks arrays and the
// role buckets rankings are grouped into.
var identityFields = []string{
	"data.reportData.report.code",
	"data.reportData.report.title",
	"data.reportData.report.owner.name",
	"data.reportData.report.masterData.actors.*.name",
	"data.reportData.report.masterData.actors.*.server",
	"data.reportData.report.rankings.data.*.roles.*.characters.*.name",
	"data.reportData.report.rankings.data.*.roles.*.characters.*.server",
}

// playerTables are the tables whose rows are sometimes a person. A row also
// names bosses and NPCs — "Hex Lord Malacrass" is in the recorded healing
// table, real and entirely fine — so the row's own declared type is what
// says whether its name belongs to somebody.
var playerTables = []string{"damage", "healing", "deaths"}

// notPeople are the entry types whose names are the game's rather than a
// player's.
var notPeople = []string{"Boss", "NPC", "Pet"}

// eventStreams are the fields that hold raw event rows. Every query this app
// makes asks for events by actor id, so a row carries sourceID and targetID
// as numbers and the roster is the only place a name appears. A query that
// asked for `useActorIDs: false` instead — which is exactly how a peer's
// casts are fetched without paying for their master data — inlines the actor
// in every single row, names and all.
//
// Nothing here knows how to scrub that, so nothing here may record it. This
// is the shape the whole issue is about: the old refusal could not see it,
// because a recording with no master data produced no rules and therefore had
// nothing that could survive.
var eventStreams = []string{"casts", "lust", "procs", "cooldowns", "raidCDs", "bossCasts"}

// inlinedActors reports the event streams whose rows carry an actor object
// rather than an id.
func inlinedActors(tree any) []string {
	var found []string
	for _, stream := range eventStreams {
		rows := gather(tree, strings.Split("data.reportData.report."+stream+".data.*", "."))
		for _, row := range rows {
			m, ok := row.(map[string]any)
			if !ok {
				continue
			}
			for _, side := range []string{"source", "target"} {
				if _, isObject := m[side].(map[string]any); isObject {
					if !slices.Contains(found, stream) {
						found = append(found, stream)
					}
				}
			}
		}
	}
	return found
}

// verify reports every way a body is not safe to commit. It returns the kind
// of thing that was wrong and never the value, because an error message is a
// place a real name would escape to.
func (r *redactor) verify(tree any) error {
	if unknown := unknownShapes(tree, "", knownFields); len(unknown) > 0 {
		slices.Sort(unknown)
		return fmt.Errorf("fixture: this response holds %s, which nothing here knows how to redact; teach internal/fixture about it before recording one", strings.Join(unknown, ", "))
	}
	if streams := inlinedActors(tree); len(streams) > 0 {
		slices.Sort(streams)
		return fmt.Errorf("fixture: the %s stream carries actors inline rather than by id, and nothing here knows how to scrub one; record with useActorIDs left alone", strings.Join(streams, ", "))
	}
	for _, path := range identityFields {
		for _, got := range gather(tree, strings.Split(path, ".")) {
			s, ok := got.(string)
			if !ok || s == "" {
				continue
			}
			if !r.isPseudonym(s) {
				return fmt.Errorf("fixture: %s still holds a real value; refusing to write", path)
			}
		}
	}
	for _, table := range playerTables {
		rows := gather(tree, strings.Split("data.reportData.report."+table+".data.entries.*", "."))
		for _, row := range rows {
			m, ok := row.(map[string]any)
			if !ok {
				continue
			}
			kind, _ := m["type"].(string)
			if slices.Contains(notPeople, kind) {
				continue
			}
			if name, _ := m["name"].(string); name != "" && !r.isPseudonym(name) {
				return fmt.Errorf("fixture: the %s table names a player who was not redacted; refusing to write", table)
			}
		}
	}
	return nil
}

// isPseudonym reports whether a value is one this package invented. Anything
// else in an identity field is somebody's.
func (r *redactor) isPseudonym(s string) bool {
	if s == FakeCode || s == FakeOwner || s == FakeTitle || s == FakePet {
		return true
	}
	// A recording holding more than one report numbers them from FakeCode.
	if strings.TrimRight(s, "0123456789") == strings.TrimRight(FakeCode, "0123456789") {
		return true
	}
	// The generated ones are a stem plus an optional index: Testmage,
	// Testmage2, Testrealm7.
	stem := strings.TrimRight(s, "0123456789")
	if stem == FakeRealm || stem == FakePet {
		return true
	}
	if strings.HasPrefix(stem, "Test") && len(stem) > len("Test") {
		return true
	}
	return false
}

// unknownShapes lists the fields under tree that knownFields does not cover.
// It descends only as far as it needs to: once a path is known, everything
// beneath it is that field's business.
func unknownShapes(v any, path string, known []string) []string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	var unknown []string
	for key := range m {
		here := key
		if path != "" {
			here = path + "." + key
		}
		if slices.Contains(known, here) {
			continue
		}
		// A prefix of a known field is a container to descend into.
		isPrefix := slices.ContainsFunc(known, func(k string) bool {
			return strings.HasPrefix(k, here+".")
		})
		if !isPrefix {
			unknown = append(unknown, here)
			continue
		}
		unknown = append(unknown, unknownShapes(m[key], here, known)...)
	}
	return unknown
}

// gather collects every value at a dotted path, where "*" matches one element
// of an array or one key of a map.
func gather(v any, path []string) []any {
	if len(path) == 0 {
		return []any{v}
	}
	head, rest := path[0], path[1:]
	var out []any
	switch v := v.(type) {
	case map[string]any:
		if head == "*" {
			for _, child := range v {
				out = append(out, gather(child, rest)...)
			}
			return out
		}
		if child, ok := v[head]; ok {
			return gather(child, rest)
		}
	case []any:
		if head != "*" {
			return nil
		}
		for _, child := range v {
			out = append(out, gather(child, rest)...)
		}
	}
	return out
}
