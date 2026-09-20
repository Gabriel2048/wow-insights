package knowledge

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"maps"
	"slices"
	"strconv"
)

// Version is a digest of everything in these tables that changes an analysis.
// Two Knowledge values produce the same Version exactly when analysing a pull
// with either would produce the same result, so anything cached against a
// spec can be keyed on it and a table edit invalidates what it should.
//
// **What is sorted and what is not is the whole point.** The maps are written
// in the order whoever authored the spec found convenient, and Go ranges them
// randomly, so their keys are sorted before hashing or the digest would
// differ between two runs of the same binary.
//
// CastRule.Instant and CastRule.HardCast are **not** sorted, and must never
// be. They are ordered lists: the analysis reports the first aura in them
// that was up, so "Hyperthermia, Hot Streak!" and "Hot Streak!, Hyperthermia"
// name different auras on the same cast. Sorting them here would make a
// reordering that changes what the page says invisible to every cache keyed
// on this — the quietest kind of stale.
//
// A base cast time is in here because it decides where every pause on the
// page falls: an edit to that table with no change of digest would serve a
// cached timeline computed against the old numbers, with nothing to notice.
//
// The zero value has a Version of its own, distinct from any authored spec,
// because "nobody has written this spec down" is a real state a result can be
// computed under.
func (k Knowledge) Version() string {
	h := sha256.New()

	write := func(s string) {
		// The length prefix is what stops "ab"+"c" and "a"+"bc" colliding,
		// which is otherwise reachable by renaming two auras.
		var n [8]byte
		binary.LittleEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	writeNames := func(m map[int]string) {
		for _, id := range slices.Sorted(maps.Keys(m)) {
			write(strconv.Itoa(id))
			write(m[id])
		}
	}

	write(k.Spec.Class)
	write(k.Spec.Spec)
	writeNames(k.ProcAuras)
	writeNames(k.Cooldowns)
	for _, id := range slices.Sorted(maps.Keys(k.CastRules)) {
		rule := k.CastRules[id]
		write(strconv.Itoa(id))
		// In order, deliberately. See the doc comment.
		for _, aura := range rule.Instant {
			write(aura)
		}
		write("|") // the two lists are distinct even when one is empty
		for _, aura := range rule.HardCast {
			write(aura)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(k.JudgedCooldowns)) {
		cd := k.JudgedCooldowns[id]
		write(strconv.Itoa(id))
		write(cd.Name)
		write(strconv.Itoa(cd.Base))
	}
	for _, id := range slices.Sorted(maps.Keys(k.BaseCasts)) {
		b := k.BaseCasts[id]
		write(strconv.Itoa(id))
		// The integer nanoseconds, not String(): two durations that format
		// identically are the same digest either way, but a formatting change
		// in the standard library would silently move every key.
		write(strconv.FormatInt(int64(b.Base), 10))
		write(strconv.FormatBool(b.Channel))
	}

	// Twelve hex characters is plenty to key a process-lifetime cache on and
	// short enough to read in a log line.
	return hex.EncodeToString(h.Sum(nil))[:12]
}
