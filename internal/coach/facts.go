package coach

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"wowinsight/internal/warcraftlogs"
)

// The fact sheet is the pull compressed to what a reader needs in order to
// judge the findings — about five kilobytes, against roughly seventy-five of
// raw timeline for the same pull.
//
// **It is assembled field by field into fresh types declared here, never by
// marshalling a warcraftlogs value.** That is deliberate and it is the lesson
// internal/fixture/safety.go is already written around: a body nobody taught
// the redactor about produced no rules, so nothing could survive, so it
// passed. If this sheet embedded a Timeline, a field added upstream would
// appear in an outbound request body with nobody deciding that it should.
// Here it is a compile-time no-op instead, which is the only version of that
// question with a safe default.
//
// Two whole fields are dropped for exactly this reason. RaidWindow.Source is
// the name of the raider who pressed Bloodlust, and it is populated in both
// the lust and the raid-cooldown lanes; BossCast.Source is an NPC and harmless
// but is dropped with it, because "drop every Source" is a rule somebody can
// keep and "drop the ones that are people" is not.

// bucket is the grain everything periodic is reduced to. Ten seconds is the
// smallest interval that says anything: the global cooldown is a second and a
// half, so a one-second bucket reads "1" everywhere and costs forty times as
// much to say it.
const bucket = 10 * time.Second

// mechanicBurstCap and mechanicTotalCap keep the boss's rotation filler out of
// the sheet. A cast the boss does forty times is the encounter's heartbeat; a
// cast it does twice is the thing a player held a cooldown for.
const (
	mechanicBurstCap = 3
	mechanicTotalCap = 4
)

// A moment in the pull is written the way the page writes it — "3:44" — and
// never as a count of seconds. The findings' own evidence is in that form, the
// timeline the player clicks through to is labelled in that form, and a sheet
// that said 224 would be asking the model to convert between two notations for
// the same fact on every line. It would get that wrong eventually, and the
// wrong answer would be a confident timestamp pointing at nothing.
//
// A *length* of time stays a number of seconds, because that is how the
// evidence states one ("77s") and the two are genuinely different things.
type facts struct {
	Boss       string `json:"boss"`
	Outcome    string `json:"outcome"`
	Ends       string `json:"ends_at"`
	LengthS    int    `json:"length_s"`
	Spec       string `json:"spec"`
	Percentile string `json:"percentile,omitempty"`
	// ActedUntil is the last moment the player could do anything about the
	// pull. It differs from the end only when they died, and that difference
	// is the commonest honest reason for a finding to be set aside.
	ActedUntil string `json:"acted_until"`
	DiedAt     string `json:"died_at,omitempty"`

	Phases    []factPhase    `json:"phases,omitempty"`
	Lust      []factWindow   `json:"lust,omitempty"`
	RaidCDs   []factWindow   `json:"raid_cooldowns,omitempty"`
	Mechanics []factMechanic `json:"boss_mechanics,omitempty"`
	Cooldowns []factWindow   `json:"your_cooldown_windows,omitempty"`
	Judged    []factJudged   `json:"judged_cooldowns,omitempty"`
	Rotation  []factBucket   `json:"per_10s,omitempty"`
	Pauses    []factPause    `json:"pauses,omitempty"`

	// Tables is the digest of the spec tables this analysis used, so a
	// recorded exchange says which knowledge produced it.
	Tables string `json:"knowledge_version"`
}

type factPhase struct {
	Name         string `json:"name"`
	Start        string `json:"start"`
	End          string `json:"end"`
	Intermission bool   `json:"intermission,omitempty"`
}

type factWindow struct {
	Name  string `json:"name"`
	Start string `json:"start"`
	End   string `json:"end"`
}

type factMechanic struct {
	Name        string `json:"name"`
	At          string `json:"at"`
	Count       int    `json:"count,omitempty"`
	Interrupted int    `json:"interrupted,omitempty"`
}

type factJudged struct {
	Name string `json:"name"`
	// UsedAt is every use, in order. This is the rule's own input, so it is
	// raw rather than aggregated: there are never many of them.
	UsedAt []string `json:"used_at"`
	// ObservedCooldownS is the cooldown the player demonstrated — their own
	// shortest gap, bounded. Without it a reader sees a 60s gap under a
	// "base 120s" cooldown and concludes the player double-used, which is
	// wrong and which they will say out loud.
	ObservedCooldownS int `json:"observed_cooldown_s"`
	BaseCooldownS     int `json:"untalented_cooldown_s"`
}

type factBucket struct {
	At    string `json:"at"`
	Casts int    `json:"casts"`
	// IdleS is the summed gap between casts in this bucket, in whole seconds.
	// Nine casts with no idle and nine casts with five seconds of it are
	// different pulls.
	IdleS int `json:"idle_s"`
	// DPSK is mean damage per second over the bucket, in thousands. It is
	// what says whether a cooldown held for later actually paid.
	DPSK int `json:"dps_k,omitempty"`
}

// factPause is one stretch the player was not occupied, as the timeline page
// reports it.
//
// **It replaced a list built from Cast.Gap, which was wrong and loudly so.**
// Raw gaps include the global cooldown, so the sheet was telling the model the
// player spent 4m25s of a 7m11s pull idle — 61% — on the same page as a damage
// uptime of 97.1%. The timeline page has said 6 pauses and 32s since the
// cooldown was modelled. Handing a model two numbers that differ by a factor
// of eight, on the one page whose whole claim is that it says nothing the log
// cannot prove, is the sheet arguing with the product.
type factPause struct {
	From   string `json:"from"`
	LenS   int    `json:"length_s"`
	GCDS   string `json:"global_cooldown_s"`
	Reason string `json:"reason,omitempty"`
}

// Facts is exactly what would be put in front of the model for this pull,
// pretty-printed. Nothing else is sent but the system prompt and the findings.
//
// It is exported for one reason: this package promises that nothing belonging
// to a person leaves the process, and a promise nobody can check is worth
// very little. cmd/dev/measure-insight prints this, so the claim can be read
// rather than believed. It runs the same identity check the real path does
// and returns its refusal, so inspecting a sheet can never be the thing that
// leaks one.
func Facts(in Input) ([]byte, error) {
	sheet, err := buildFacts(in)
	if err != nil {
		return nil, err
	}
	body, err := sheet.marshal()
	if err != nil {
		return nil, err
	}
	if err := identityOf(in).check(body, in.Findings); err != nil {
		return nil, err
	}
	var tree any
	if err := json.Unmarshal(body, &tree); err != nil {
		return nil, err
	}
	return json.MarshalIndent(tree, "", "  ")
}

// buildFacts compresses one pull.
func buildFacts(in Input) (*facts, error) {
	if in.Timeline == nil || in.Detail == nil {
		return nil, fmt.Errorf("%w: nothing to describe", ErrUntrustworthy)
	}
	t := in.Timeline
	f := &facts{
		Boss:       in.Detail.Fight.Name,
		Outcome:    strings.ToLower(in.Detail.Fight.Outcome()),
		Ends:       clock(t.Duration),
		LengthS:    secs(t.Duration),
		Spec:       strings.TrimSpace(in.Player.Spec + " " + in.Player.ClassName()),
		ActedUntil: clock(in.Player.ActedUntil()),
		Tables:     in.Know.Version(),
	}
	if in.Player.Ranked() {
		f.Percentile = in.Player.Ranking.Percentile()
	}
	if in.Player.Deaths > 0 && in.Player.DiedAt > 0 {
		f.DiedAt = clock(in.Player.DiedAt)
	}

	for _, p := range t.Phases {
		f.Phases = append(f.Phases, factPhase{Name: p.Name, Start: clock(p.Start), End: clock(p.End), Intermission: p.IsIntermission})
	}
	// Source is dropped from both of these. See the note at the top.
	for _, l := range t.Lusts {
		f.Lust = append(f.Lust, factWindow{Name: l.Name, Start: clock(l.Start), End: clock(l.End)})
	}
	for _, c := range t.RaidCDs {
		f.RaidCDs = append(f.RaidCDs, factWindow{Name: c.Name, Start: clock(c.Start), End: clock(c.End)})
	}
	for _, c := range t.Cooldowns {
		f.Cooldowns = append(f.Cooldowns, factWindow{Name: c.Name, Start: clock(c.Start), End: clock(c.End)})
	}
	f.Mechanics = mechanics(t.BossCasts)
	f.Judged = judgedUses(in)
	f.Rotation = rotation(t)
	f.Pauses = pauses(t, in.Player.DiedAt)
	return f, nil
}

// mechanics keeps the boss casts that mark a moment and drops the ones that
// mark the encounter's pulse.
func mechanics(casts []warcraftlogs.BossCast) []factMechanic {
	total := map[string]int{}
	for _, b := range casts {
		total[b.Name] += max(b.Count, 1)
	}
	var out []factMechanic
	for _, b := range casts {
		if total[b.Name] > mechanicTotalCap || b.Count > mechanicBurstCap {
			continue
		}
		out = append(out, factMechanic{Name: b.Name, At: clock(b.Offset), Count: b.Count, Interrupted: b.Interrupted})
	}
	return out
}

// judgedUses is the input to the only rule that exists today, stated plainly
// so that a reader can check the finding against it rather than take it on
// trust.
func judgedUses(in Input) []factJudged {
	var out []factJudged
	for ability, rule := range in.Know.JudgedCooldowns {
		var used []string
		var offsets []time.Duration
		for _, c := range in.Timeline.Casts {
			if c.AbilityID == ability && !c.Cancelled {
				used = append(used, clock(c.Offset))
				offsets = append(offsets, c.Offset)
			}
		}
		if len(used) == 0 {
			continue
		}
		row := factJudged{Name: rule.Name, UsedAt: used, BaseCooldownS: rule.Base}
		// False with one use: there is no gap to measure, so the sheet says
		// nothing about spacing rather than guessing at it.
		if floor, ok := warcraftlogs.ObservedCooldown(rule, offsets); ok {
			row.ObservedCooldownS = secs(floor)
		}
		out = append(out, row)
	}
	// Map iteration is random and this is going into a request body that a
	// recording is keyed beside; sorted, it is the same bytes every run.
	slices.SortFunc(out, func(a, b factJudged) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// rotation reduces the cast list to one row per ten seconds.
func rotation(t *warcraftlogs.Timeline) []factBucket {
	if t.Duration <= 0 {
		return nil
	}
	n := int(t.Duration/bucket) + 1
	rows := make([]factBucket, n)
	idle := make([]time.Duration, n)
	for i := range rows {
		rows[i].At = clock(time.Duration(i) * bucket)
	}
	for _, c := range t.Casts {
		i := int(c.Offset / bucket)
		if i < 0 || i >= n {
			continue
		}
		rows[i].Casts++
		idle[i] += c.Gap
	}
	for i := range rows {
		rows[i].IdleS = secs(idle[i])
	}
	if t.DPS != nil {
		sum, count := make([]float64, n), make([]int, n)
		for _, p := range t.DPS.Points {
			i := int(p.Offset / bucket)
			if i < 0 || i >= n {
				continue
			}
			sum[i] += p.DPS
			count[i]++
		}
		for i := range rows {
			if count[i] > 0 {
				rows[i].DPSK = int(sum[i] / float64(count[i]) / 1000)
			}
		}
	}
	return rows
}

// pauses is what the timeline page reports, so the two agree. A pause here
// already has the global cooldown taken off it, which is what makes it a
// stretch the player could have used rather than a wait the game imposed.
func pauses(t *warcraftlogs.Timeline, diedAt time.Duration) []factPause {
	if !t.GCD.Modelled {
		return nil
	}
	var out []factPause
	for _, p := range t.Attributed(diedAt) {
		out = append(out, factPause{
			From:   clock(p.Start),
			LenS:   secs(p.Duration),
			GCDS:   fmt.Sprintf("%.2f", p.GCD.Seconds()),
			Reason: string(p.Reason),
		})
	}
	return out
}

// marshal renders the sheet. Every map is sorted into a slice before it gets
// here, so the same pull produces the same bytes on every run — which is what
// a recording keyed beside this body depends on.
func (f *facts) marshal() ([]byte, error) {
	body, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUntrustworthy, err)
	}
	return body, nil
}

// lexicon is every proper noun this pull can legitimately be described with:
// the spells, the phases, the mechanics and the boss. It is what the validator
// holds the model's prose to, so that "you should have used Rune of Power" —
// a sentence with no numbers in it at all, and therefore invisible to every
// numeric check — cannot reach the page.
func (f *facts) lexicon(in Input) map[string]bool {
	words := map[string]bool{}
	add := func(s string) {
		for _, w := range properWords(s) {
			words[w] = true
		}
	}
	add(f.Boss)
	add(f.Spec)
	for _, p := range f.Phases {
		add(p.Name)
	}
	for _, list := range [][]factWindow{f.Lust, f.RaidCDs, f.Cooldowns} {
		for _, w := range list {
			add(w.Name)
		}
	}
	for _, m := range f.Mechanics {
		add(m.Name)
	}
	for _, j := range f.Judged {
		add(j.Name)
	}

	// The spec's own tables, so an aura the player has but did not use in
	// this pull is still a word that may be said about it.
	for _, n := range in.Know.ProcAuras {
		add(n)
	}
	for _, n := range in.Know.Cooldowns {
		add(n)
	}
	for _, cd := range in.Know.JudgedCooldowns {
		add(cd.Name)
	}
	// And the findings' own words, which are the thing being reworded.
	for _, fd := range in.Findings {
		add(fd.Title)
		add(fd.Detail)
		for _, e := range fd.Evidence {
			add(e.Label)
			add(e.Value)
		}
	}
	return words
}

// properWords splits a string and keeps the words that begin with a capital.
// A multi-word name is kept as its parts — "Time Warp" admits "Time" and
// "Warp" — which is looser than matching the whole name and far more robust
// to a model writing "the Time Warp window".
func properWords(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && r != '\''
	}) {
		if r := []rune(w)[0]; unicode.IsUpper(r) {
			out = append(out, strings.ToLower(w))
		}
	}
	return out
}

// secs rounds a duration to whole seconds, the grain every *length* in the
// sheet is stated at. Nothing a player can act on is finer than that.
func secs(d time.Duration) int { return int(d.Round(time.Second) / time.Second) }

// clock writes a moment as the page writes it. It is deliberately the same
// rendering warcraftlogs.Finding.Timestamp produces, so that a moment in the
// sheet and the same moment in a finding's evidence are the same characters.
func clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

// numerals is every quantity the sheet states, with the kind it states it as.
//
// It is collected from the typed fields rather than by scanning the JSON,
// because the JSON cannot say which kind a number is: "77" in length_s is a
// length and the "3:44" in a phase start is a moment, and a validator that
// confused the two would let a model call a moment a duration — which is how
// a page ends up telling somebody a cooldown was down for four minutes when
// what happened is that it came back at 4:00.
//
// This is the allow-set for a *reason*, and only for a reason. A finding's
// claim is held to the evidence printed underneath it, which is the stricter
// thing and the right one: the player can check every number in it without
// leaving the paragraph. A reason for setting a finding aside is a claim
// about the pull rather than about the finding — "the intermission started at
// 3:44, and you were holding for it" — so it is held to what the pull says.
// Every number here came out of the log and every one of them is a moment the
// player can go and look at on the timeline, which is the next-best thing to
// having it printed alongside.
func (f *facts) numerals() []numeral {
	var out []numeral
	at := func(c string) {
		for _, n := range scanNumerals(c) {
			if n.kind == kindClock {
				out = append(out, n)
			}
		}
	}
	length := func(s int) { out = append(out, numeral{kind: kindDuration, val: float64(s)}) }
	count := func(n int) { out = append(out, numeral{kind: kindCount, val: float64(n)}) }

	at(f.Ends)
	at(f.ActedUntil)
	at(f.DiedAt)
	length(f.LengthS)
	for _, p := range f.Phases {
		at(p.Start)
		at(p.End)
	}
	for _, list := range [][]factWindow{f.Lust, f.RaidCDs, f.Cooldowns} {
		for _, w := range list {
			at(w.Start)
			at(w.End)
		}
	}
	for _, m := range f.Mechanics {
		at(m.At)
		count(m.Count)
		count(m.Interrupted)
	}
	for _, j := range f.Judged {
		for _, u := range j.UsedAt {
			at(u)
		}
		length(j.ObservedCooldownS)
		length(j.BaseCooldownS)
		count(len(j.UsedAt))
	}
	for _, b := range f.Rotation {
		at(b.At)
		count(b.Casts)
		length(b.IdleS)
		count(b.DPSK)
	}
	for _, p := range f.Pauses {
		at(p.From)
		length(p.LenS)
	}
	return out
}
