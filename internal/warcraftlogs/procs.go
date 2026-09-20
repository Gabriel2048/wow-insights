package warcraftlogs

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"wowinsight/internal/knowledge"
)

// ProcLedger is what became of one proc aura over a pull: how many times it
// came up, and how many of those were spent on something the spec judges.
//
// **It deliberately does not count waste.** An unspent window and a wasted
// proc are not the same thing, and the difference is not recoverable from the
// log: a proc is spent by whichever spell the player chose, and a spec table
// that has not authored that spell sees a correct cast as a missing one. That
// is not hypothetical — before Flamestrike was authored, seven Hot Streaks on
// the recorded wipe counted as unspent, in a stretch where the player spent
// every one of them correctly on an AoE pack. A page that had published that
// number would have accused a player who did nothing wrong, with arithmetic
// to back it up.
//
// So the ledger states what it can stand behind — generated, spent, and how
// many were still up when the pull ended — and names the spells that count as
// a spend, so a reader can see the list this rests on and say it is short.
type ProcLedger struct {
	Name      string
	Generated int
	Spent     int
	// StillUp is how many were up when the fight ended. They are not waste:
	// a proc held when the boss dies was never going to be spent.
	StillUp int
	// SpentOn names the spells that count, in spell order, so the reader can
	// check what the count means.
	SpentOn []string
}

// procLedgers counts every tracked aura against the casts that spent it.
//
// Windows of the same name are merged before anything is counted. The game
// reports one effect under several ability ids — Hyperthermia arrives as both
// 383874 and 1242220 — and auraWindows keys on the id, so one proc appears as
// two overlapping windows. Counting those separately doubles the generated
// total and makes half of them look unspent.
func procLedgers(casts []Cast, windows []auraWindow, know knowledge.Knowledge, fight Fight) []ProcLedger {
	if len(windows) == 0 {
		return nil
	}
	byName := map[string][]auraWindow{}
	for _, w := range windows {
		byName[w.name] = append(byName[w.name], w)
	}

	// Which spells the spec says may spend which aura, so the ledger can name
	// them rather than asking the reader to trust a count.
	spenders := map[string][]string{}
	for spell, rule := range know.CastRules {
		name := spellName(casts, spell)
		if name == "" {
			continue
		}
		for _, aura := range slices.Concat(rule.Instant, rule.HardCast) {
			if !slices.Contains(spenders[aura], name) {
				spenders[aura] = append(spenders[aura], name)
			}
		}
	}

	var out []ProcLedger
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		merged := mergeWindows(byName[name])
		ledger := ProcLedger{Name: name, Generated: len(merged)}
		ledger.SpentOn = append(ledger.SpentOn, spenders[name]...)
		slices.Sort(ledger.SpentOn)
		for _, w := range merged {
			switch {
			case spentIn(casts, w, name):
				ledger.Spent++
			case w.end >= fight.Duration():
				ledger.StillUp++
			}
		}
		out = append(out, ledger)
	}
	slices.SortFunc(out, func(a, b ProcLedger) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// mergeWindows folds overlapping windows of one aura into one each.
func mergeWindows(windows []auraWindow) []auraWindow {
	sorted := slices.Clone(windows)
	slices.SortFunc(sorted, func(a, b auraWindow) int { return cmp.Compare(a.start, b.start) })
	merged := sorted[:0]
	for _, w := range sorted {
		if n := len(merged); n > 0 && w.start <= merged[n-1].end {
			merged[n-1].end = max(merged[n-1].end, w.end)
			continue
		}
		merged = append(merged, w)
	}
	return merged
}

// spentIn reports whether a cast inside the window named this aura as what
// made it worth taking. It asks the same field the page prints, so the ledger
// and the cast table can never disagree about one cast.
func spentIn(casts []Cast, w auraWindow, name string) bool {
	for _, c := range casts {
		if c.Offset < w.start-procStartSlack || c.Offset > w.end+procSlack {
			continue
		}
		if slices.Contains(splitProcs(c.Proc), name) {
			return true
		}
	}
	return false
}

// spellName is what the log called a spell, so the ledger names spells the
// way the rest of the page does rather than by id. Empty when the player
// never cast it, which is why a spender list can be shorter than the table.
func spellName(casts []Cast, spell int) string {
	for _, c := range casts {
		if c.AbilityID == spell {
			return c.Name
		}
	}
	return ""
}

// splitProcs undoes the " + " that Cast.Proc joins several auras with.
func splitProcs(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, " + ")
}
