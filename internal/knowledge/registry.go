package knowledge

import "fmt"

// specs is the table of every authored specialisation. Adding one is a new
// file declaring its Knowledge and one row here; nothing else changes.
var specs = []Knowledge{
	fireMage,
}

var byID = index(specs)

// Lookup finds the tables for a specialisation. ok is false for a spec nobody
// has authored yet, and the Knowledge returned then is the zero value, which
// is safe to analyse with.
func Lookup(id SpecID) (k Knowledge, ok bool) {
	k, ok = byID[id]
	return k, ok
}

// index keys the table by spec. Two rows for one spec is a mistake in the
// table, not a runtime condition, so it fails at start-up rather than letting
// one row silently shadow the other.
func index(table []Knowledge) map[SpecID]Knowledge {
	m := make(map[SpecID]Knowledge, len(table))
	for _, k := range table {
		if _, dup := m[k.Spec]; dup {
			panic(fmt.Sprintf("knowledge: %s %s is in the table twice", k.Spec.Spec, k.Spec.Class))
		}
		m[k.Spec] = k
	}
	return m
}
