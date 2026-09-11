package warcraftlogs

import (
	"strconv"
	"strings"
)

// An operation is one named GraphQL document. The name travels with every
// request as operationName — GraphQL's own way of saying which document this
// is — and is what the fixture recorder keys files on. Every document is a
// const: nothing here is built by concatenation, and a test asserts it.
//
// Every document also asks for rateLimitData at the root. It is free, and it
// is how the client knows where the hourly budget stands after every call.
type operation struct {
	name     string
	document string
}

const budgetFragment = `rateLimitData { limitPerHour pointsSpentThisHour pointsResetIn }`

var (
	rateLimitOp = operation{"RateLimit", `query RateLimit { ` + budgetFragment + ` }`}

	reportOp = operation{"Report", `query Report($code: String!) {
  ` + budgetFragment + `
  reportData {
    report(code: $code) {
      code
      title
      startTime
      endTime
      owner { name }
      zone { name }
      fights { id name kill difficulty startTime endTime }
    }
  }
}`}

	// masterDataOp is report-scoped: the ability, player and NPC tables are
	// the same for every fight in a report, so they are asked for once per
	// report rather than riding on every timeline query — and the per-report
	// cache key #2 will use can dedupe them.
	masterDataOp = operation{"MasterData", `query MasterData($code: String!) {
  ` + budgetFragment + `
  reportData {
    report(code: $code) {
      masterData {
        abilities { gameID name }
        actors(type: "Player") { id name subType }
        npcs: actors(type: "NPC") { id name subType gameID }
      }
    }
  }
}`}

	fightOp = operation{"Fight", `query Fight($code: String!, $id: Int!) {
  ` + budgetFragment + `
  reportData {
    report(code: $code) {
      code
      title
      fights(fightIDs: [$id]) {
        id name kill difficulty startTime endTime bossPercentage friendlyPlayers
      }
      masterData { actors(type: "Player") { id name type subType server } }
      damage: table(dataType: DamageDone, fightIDs: [$id])
      healing: table(dataType: Healing, fightIDs: [$id])
      deaths: table(dataType: Deaths, fightIDs: [$id])
    }
  }
}`}

	// The four filters are nullable String variables. A variable that is not
	// provided leaves its argument out of the query altogether (that is what
	// the GraphQL spec says an absent variable does), which is how a spec
	// with no tracked procs avoids sending "ability.id in ()".
	timelineOp = operation{"Timeline", `query Timeline($code: String!, $id: Int!, $source: Int!, $start: Float!, $end: Float!,
                $lust: String, $procs: String, $cooldowns: String, $raidCDs: String, $bosses: String) {
  ` + budgetFragment + `
  reportData {
    report(code: $code) {
      casts: events(
        dataType: Casts, fightIDs: [$id], sourceID: $source,
        startTime: $start, endTime: $end, limit: 10000
      ) { data nextPageTimestamp }
      lust: events(
        dataType: Buffs, fightIDs: [$id],
        startTime: $start, endTime: $end, limit: 10000,
        filterExpression: $lust
      ) { data nextPageTimestamp }
      procs: events(
        dataType: Buffs, fightIDs: [$id], targetID: $source,
        startTime: $start, endTime: $end, limit: 10000,
        filterExpression: $procs
      ) { data nextPageTimestamp }
      cooldowns: events(
        dataType: Buffs, fightIDs: [$id], targetID: $source,
        startTime: $start, endTime: $end, limit: 10000,
        filterExpression: $cooldowns
      ) { data nextPageTimestamp }
      raidCDs: events(
        dataType: Buffs, fightIDs: [$id],
        startTime: $start, endTime: $end, limit: 10000,
        filterExpression: $raidCDs
      ) { data nextPageTimestamp }
      damage: graph(
        dataType: DamageDone, fightIDs: [$id], sourceID: $source,
        startTime: $start, endTime: $end
      )
      taken: graph(
        dataType: DamageTaken, fightIDs: [$id], sourceID: $source,
        startTime: $start, endTime: $end
      )
      bossCasts: events(
        dataType: Casts, fightIDs: [$id], hostilityType: Enemies,
        startTime: $start, endTime: $end, limit: 10000,
        filterExpression: $bosses
      ) { data nextPageTimestamp }
      fights(fightIDs: [$id]) { encounterID phaseTransitions { id startTime } }
      phases { encounterID phases { id name isIntermission } }
    }
  }
}`}

	castPageOp = operation{"CastPage", `query CastPage($code: String!, $id: Int!, $source: Int!, $start: Float!, $end: Float!) {
  ` + budgetFragment + `
  reportData { report(code: $code) {
    casts: events(dataType: Casts, fightIDs: [$id], sourceID: $source,
                  startTime: $start, endTime: $end, limit: 10000) { data nextPageTimestamp }
  } }
}`}
)

// operations lists every document, for the tests that hold them to the rules.
var operations = []operation{rateLimitOp, reportOp, masterDataOp, fightOp, timelineOp, castPageOp}

// expensive names the operations the budget guard applies to: the ones that
// cost real points. A report's fight list and its master data are cheap and
// are what a user needs to see the budget message in the first place.
func (op operation) expensive() bool {
	return op.name == fightOp.name || op.name == timelineOp.name || op.name == castPageOp.name
}

// filterVariable is the value for one of the timeline's filter variables: the
// expression, or nothing at all for an empty set, so the argument is omitted.
func filterVariable(vars map[string]any, name string, ids []int) {
	if expr, ok := abilityFilter(ids); ok {
		vars[name] = expr
	}
}

// sourceFilter builds an events filter expression for a set of NPCs by their
// game ids — in the API's expression language source.id is the game's id for
// the creature, not the report's actor id, which matches nothing — reporting
// false for an empty set so the argument is omitted.
func sourceFilter(ids []int) (string, bool) {
	if len(ids) == 0 {
		return "", false
	}
	text := make([]string, len(ids))
	for i, id := range ids {
		text[i] = strconv.Itoa(id)
	}
	return "source.id in (" + strings.Join(text, ",") + ")", true
}

// abilityFilter builds an events filter expression for a set of ability IDs,
// reporting false for an empty set — there is no expression for "none of
// them", and "ability.id in ()" is rejected by the API.
func abilityFilter(ids []int) (string, bool) {
	if len(ids) == 0 {
		return "", false
	}
	text := make([]string, len(ids))
	for i, id := range ids {
		text[i] = strconv.Itoa(id)
	}
	return "ability.id in (" + strings.Join(text, ",") + ")", true
}
