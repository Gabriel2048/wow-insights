package warcraftlogs

import (
	"testing"
	"time"

	"wowinsight/internal/knowledge"
)

// hardCast is a cast with a real bar, which is what the estimator measures.
func hardCast(at time.Duration, ability int, bar time.Duration) Cast {
	return Cast{
		AbilityID: ability, Name: "Fireball", Offset: at, End: at + bar,
		CastTime: bar, HadBegincast: true,
	}
}

// fireBases is a spec that knows two cast times, the way Fire Mage does.
func fireBases() knowledge.Knowledge {
	return knowledge.Knowledge{
		Spec: knowledge.SpecID{Class: "Mage", Spec: "Fire"},
		BaseCasts: map[int]knowledge.BaseCast{
			133:  {Base: 1750 * time.Millisecond},
			2948: {Base: 1500 * time.Millisecond},
		},
	}
}

func fightOf(d time.Duration) Fight {
	return Fight{StartTime: 0, EndTime: float64(d.Milliseconds())}
}

// **The number this whole change exists for.** Before the global cooldown was
// modelled the recorded kill reported 357 gaps totalling 264.7 s on a 431 s
// fight, beside a damage uptime of 97.1% — two numbers on one page that cannot
// both be true. A pull is not 61% idle.
//
// The six below are pinned by count and total rather than by their exact
// lengths, so a legitimate change to the estimator moves them without going
// red, while a table edit that doubles the count does not.
func TestTheRecordedKillReportsAHandfulOfPausesNotHundreds(t *testing.T) {
	rep, fight := recordedKill(t)
	tl := buildTimeline(rep, rep.Casts.Data, fight, fire)

	if !tl.GCD.Modelled {
		t.Fatal("the global cooldown was not modelled on a pull with 88 cast bars in it")
	}
	if len(tl.Pauses) > 12 {
		t.Errorf("got %d pauses, want a handful; the model is reporting ordinary global cooldowns as idle", len(tl.Pauses))
	}
	if len(tl.Pauses) == 0 {
		t.Error("got no pauses at all, so either the model is charging too much busy time or nothing is being computed")
	}
	// The player was not idle for most of the pull. The damage table puts
	// uptime at 97.1%, and while the two measure different things they cannot
	// disagree by half a fight.
	if paused := tl.TotalPaused(); paused > tl.Duration/4 {
		t.Errorf("pauses total %v of a %v pull; the old unmodelled number said 264.7s and that was the defect", paused, tl.Duration)
	}
	for _, p := range tl.Pauses {
		if p.GCD <= 0 {
			t.Errorf("the pause at %s carries no global cooldown, so the page cannot show its working", p.Timestamp())
		}
		if p.Duration <= deadBand*p.GCD {
			t.Errorf("the pause at %s is %v against a %v global cooldown, inside the dead band", p.Timestamp(), p.Duration, p.GCD)
		}
	}
}

// A spec nobody has authored has no base cast times, so nothing can be
// measured and the page must say so. The alternative is worse than silence: a
// nil map reads a zero duration, and a global cooldown of zero makes every
// moment of the fight a pause — measured, 116 of them totalling 271 s.
func TestAnUnauthoredSpecReportsNoPausesAndSaysWhy(t *testing.T) {
	rep, fight := recordedKill(t)
	var none knowledge.Knowledge

	tl := buildTimeline(rep, rep.Casts.Data, fight, none)
	if tl.GCD.Modelled {
		t.Error("the global cooldown is claimed to be modelled for a spec with no table")
	}
	if len(tl.Pauses) != 0 {
		t.Errorf("got %d pauses for a spec nothing is known about; every one of them is fiction", len(tl.Pauses))
	}
}

// A spec CAN have a table and still be unmodelled, and the difference is not
// hypothetical: the Holy Paladin in the recorded raid cast 546 times in 431
// seconds with zero cast bars. Gating on the table rather than on the
// measurement would hand that player a fabricated global cooldown.
func TestAPullWithNoCastBarsIsUnmodelledEvenWithATable(t *testing.T) {
	var casts []Cast
	for at := time.Second; at < 3*time.Minute; at += 2 * time.Second {
		casts = append(casts, Cast{AbilityID: 133, Name: "Fireball", Offset: at, End: at})
	}
	pauses, model := buildPauses(casts, fightOf(3*time.Minute), fireBases())
	if model.Modelled {
		t.Error("a pull of pure instants claims a measured global cooldown")
	}
	if len(pauses) != 0 {
		t.Errorf("got %d pauses from a pull nothing could be measured in", len(pauses))
	}
}

// The global cooldown runs alongside the cast bar, not after it. This is the
// one thing the decision record got wrong before it was measured: on the
// recorded kill a quarter of the waits after a hard cast are under 30 ms,
// which is impossible if a fresh cooldown began when the bar ended.
//
// Four Fireballs chain-cast back to back leave no pause. Under the wrong
// model each would be followed by an uncharged global and the run would show
// three pauses of about a second.
func TestTheGlobalCooldownRunsAlongsideTheCastBar(t *testing.T) {
	bar := 1250 * time.Millisecond // 1750ms base at ~1.4 haste
	var casts []Cast
	for i := range 4 {
		casts = append(casts, hardCast(time.Duration(i)*bar, 133, bar))
	}
	pauses, model := buildPauses(casts, fightOf(4*bar), fireBases())
	if !model.Modelled {
		t.Fatal("four cast bars were not enough to model the global cooldown")
	}
	if len(pauses) != 0 {
		t.Errorf("got %d pauses in a run of back-to-back hard casts: %v", len(pauses), pauses)
	}
}

// An instant occupies the player for one global cooldown and no longer. Two
// instants a global apart are continuous; two instants three globals apart are
// a pause.
func TestAnInstantOccupiesOneGlobalCooldown(t *testing.T) {
	know := fireBases()
	bar := 1250 * time.Millisecond
	// Three bars up front so there is something to measure, then instants.
	casts := []Cast{hardCast(0, 133, bar), hardCast(2*time.Second, 133, bar), hardCast(4*time.Second, 133, bar)}
	casts = append(casts,
		Cast{AbilityID: 108853, Name: "Fire Blast", Offset: 6 * time.Second, End: 6 * time.Second},
		Cast{AbilityID: 108853, Name: "Fire Blast", Offset: 20 * time.Second, End: 20 * time.Second},
	)
	pauses, model := buildPauses(casts, fightOf(21*time.Second), know)
	if !model.Modelled {
		t.Fatal("not modelled")
	}
	if len(pauses) != 1 {
		t.Fatalf("got %d pauses, want the one between the two instants: %v", len(pauses), pauses)
	}
	// It begins one global after the first instant, not at it.
	if got := pauses[0].Start; got < 6*time.Second+model.Median || got > 7500*time.Millisecond {
		t.Errorf("the pause begins at %v; an instant at 6s occupies the player for one global cooldown first", got)
	}
}

// A pause inside the dead band is not reported at all. The estimate has error,
// and a page that prints that error as an accusation has done something worse
// than saying nothing.
func TestAPauseInsideTheDeadBandIsNotReported(t *testing.T) {
	know := fireBases()
	bar := 1250 * time.Millisecond
	casts := []Cast{hardCast(0, 133, bar), hardCast(2*time.Second, 133, bar), hardCast(4*time.Second, 133, bar)}

	// A gap of just under two globals after the last bar, then a cast.
	_, model := buildPauses(casts, fightOf(30*time.Second), know)
	gcd := model.Median
	for _, c := range []struct {
		name string
		gap  time.Duration
		want int
	}{
		{"just inside the band", 2*gcd - 50*time.Millisecond, 0},
		{"just outside it", 2*gcd + 400*time.Millisecond, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			resumeAt := 4*time.Second + bar + c.gap
			with := append(append([]Cast{}, casts...), hardCast(resumeAt, 133, bar))
			pauses, _ := buildPauses(with, fightOf(resumeAt+bar), fireBases())
			if len(pauses) != c.want {
				t.Errorf("got %d pauses for a %v gap against a %v global cooldown, want %d", len(pauses), c.gap, gcd, c.want)
			}
		})
	}
}

// A channel is a cast bar the log does not report — Warcraft Logs emits no
// begincast for one, so a three-second Mind Flay is byte-identical to an
// instant in the event stream. Only the table can know, and if it does not the
// player is accused of standing still during the thing they were doing.
func TestAChannelIsBusyForItsWholeLength(t *testing.T) {
	know := fireBases()
	know.BaseCasts[15407] = knowledge.BaseCast{Base: 3 * time.Second, Channel: true}
	bar := 1250 * time.Millisecond

	casts := []Cast{hardCast(0, 133, bar), hardCast(2*time.Second, 133, bar), hardCast(4*time.Second, 133, bar),
		// A channel the log reports as an instant, then the next cast well
		// inside the channel's hasted length.
		{AbilityID: 15407, Name: "Mind Flay", Offset: 6 * time.Second, End: 6 * time.Second},
		hardCast(8*time.Second, 133, bar),
	}
	pauses, _ := buildPauses(casts, fightOf(10*time.Second), know)
	if len(pauses) != 0 {
		t.Errorf("got %d pauses across a channel; the model is treating it as an instant: %v", len(pauses), pauses)
	}
}

// Haste is estimated over recent casts, not averaged across the fight,
// because it moves with lust. A slow opener followed by a fast stretch must
// not leave the fast stretch measured against the opener's haste.
func TestHasteFollowsTheFightRatherThanAveragingIt(t *testing.T) {
	know := fireBases()
	var casts []Cast
	at := time.Duration(0)
	for range 6 { // unhasted: a 1750ms bar
		casts = append(casts, hardCast(at, 133, 1750*time.Millisecond))
		at += 1750 * time.Millisecond
	}
	for range 6 { // hasted hard: an 875ms bar, so haste 2.0
		casts = append(casts, hardCast(at, 133, 875*time.Millisecond))
		at += 875 * time.Millisecond
	}
	samples := hasteSamples(casts, know)
	early := gcdFrom(hasteAt(samples, 1*time.Second))
	late := gcdFrom(hasteAt(samples, at-time.Second))
	if early <= late {
		t.Errorf("the global cooldown did not shorten as haste doubled: %v then %v", early, late)
	}
	if late != gcdFloor {
		t.Errorf("at haste 2.0 the global cooldown is %v, want the %v floor", late, gcdFloor)
	}
}

// Nothing may be asked of a player after the pull stopped being theirs to
// play. A pause running to the end of the fight is usually the player being
// dead — on the recorded wipe it is 36 s of the 52 s reported — and a page
// that does not say so reads as an accusation.
func TestAPauseRunningToTheEndSaysSo(t *testing.T) {
	know := fireBases()
	bar := 1250 * time.Millisecond
	casts := []Cast{hardCast(0, 133, bar), hardCast(2*time.Second, 133, bar), hardCast(4*time.Second, 133, bar)}
	pauses, _ := buildPauses(casts, fightOf(30*time.Second), know)
	if len(pauses) != 1 {
		t.Fatalf("got %d pauses, want the one running to the end", len(pauses))
	}
	if pauses[0].Reason != PauseToFightEnd {
		t.Errorf("Reason = %q, want %q", pauses[0].Reason, PauseToFightEnd)
	}
}

// Cast.Gap is not redefined. It is wall-clock time between casts, it is used
// elsewhere, and the decision record promises its tests stay green — the pause
// model is built beside it, not on top of it.
func TestTheGapFieldIsUntouchedByThePauseModel(t *testing.T) {
	rep, fight := recordedKill(t)
	tl := buildTimeline(rep, rep.Casts.Data, fight, fire)

	var withGap int
	for _, c := range tl.Casts {
		if c.Gap > 0 {
			withGap++
		}
	}
	if withGap < 100 {
		t.Errorf("%d casts carry a gap; Cast.Gap has been redefined and the decision record says it must not be", withGap)
	}
	if len(tl.Pauses) >= withGap {
		t.Errorf("%d pauses against %d gaps: the model is not reducing anything", len(tl.Pauses), withGap)
	}
}

// The largest unexplained band on the recorded wipe is the player being dead:
// 36 s of the 52 s reported. Saying so is the difference between a page that
// informs and one that accuses a corpse of standing still.
func TestAPauseThePlayerWasDeadForSaysSo(t *testing.T) {
	know := fireBases()
	bar := 1250 * time.Millisecond
	casts := []Cast{hardCast(0, 133, bar), hardCast(2*time.Second, 133, bar), hardCast(4*time.Second, 133, bar)}
	tl := &Timeline{Duration: 60 * time.Second}
	tl.Pauses, tl.GCD = buildPauses(casts, fightOf(60*time.Second), know)
	if len(tl.Pauses) != 1 {
		t.Fatalf("got %d pauses, want the one running to the end", len(tl.Pauses))
	}
	if tl.Pauses[0].Reason != PauseToFightEnd {
		t.Fatalf("Reason = %q before attribution, want %q", tl.Pauses[0].Reason, PauseToFightEnd)
	}

	got := tl.Attributed(10 * time.Second)
	if got[0].Reason != PauseDead {
		t.Errorf("Reason = %q for a pause the player was dead through, want %q", got[0].Reason, PauseDead)
	}
	// And the analysis's own copy is untouched. The cache hands the same
	// *Timeline to every request for a subject, so relabelling in place would
	// be a race between two browsers and would outlive the request that did it.
	if tl.Pauses[0].Reason != PauseToFightEnd {
		t.Error("Attributed mutated the timeline's own pauses, which are shared through the cache")
	}
}

// A pull nobody died in is unchanged, and a pause that ended before the death
// is not blamed on it.
func TestADeathExplainsOnlyWhatFollowedIt(t *testing.T) {
	know := fireBases()
	bar := 1250 * time.Millisecond
	casts := []Cast{
		hardCast(0, 133, bar), hardCast(2*time.Second, 133, bar), hardCast(4*time.Second, 133, bar),
		hardCast(20*time.Second, 133, bar), // a pause before it
	}
	tl := &Timeline{Duration: 60 * time.Second}
	tl.Pauses, tl.GCD = buildPauses(casts, fightOf(60*time.Second), know)
	if len(tl.Pauses) != 2 {
		t.Fatalf("got %d pauses, want the mid-fight one and the trailing one", len(tl.Pauses))
	}

	t.Run("no death leaves everything alone", func(t *testing.T) {
		for i, p := range tl.Attributed(0) {
			if p.Reason != tl.Pauses[i].Reason {
				t.Errorf("pause %d was relabelled %q on a pull with no death", i, p.Reason)
			}
		}
	})
	t.Run("a late death explains only the late pause", func(t *testing.T) {
		got := tl.Attributed(30 * time.Second)
		if got[0].Reason == PauseDead {
			t.Errorf("the pause at %s is blamed on a death 30s in", got[0].Timestamp())
		}
		if got[1].Reason != PauseDead {
			t.Errorf("the pause at %s ran past the death and is not attributed to it", got[1].Timestamp())
		}
	})
}

// The recorded wipe end to end: the player died, and the pause that follows
// must say so rather than reading as two-thirds of the pull spent idle.
func TestTheRecordedWipeBlamesTheDeathAndNotThePlayer(t *testing.T) {
	rep, fight := recordedWipe(t)
	tl := buildTimeline(rep, rep.Casts.Data, fight, fire)
	if len(tl.Pauses) == 0 {
		t.Fatal("the recorded wipe reports no pauses at all")
	}

	last := tl.Pauses[len(tl.Pauses)-1]
	if last.Reason != PauseToFightEnd {
		t.Fatalf("the last pause reads %q; this test assumes it runs to the end", last.Reason)
	}
	// The death is what the fight query knows and the timeline does not, so
	// the page supplies it. Attribute against the moment it happened.
	got := tl.Attributed(last.Start)
	if got[len(got)-1].Reason != PauseDead {
		t.Errorf("the pause after the death reads %q, want %q", got[len(got)-1].Reason, PauseDead)
	}
	if share := float64(last.Duration) / float64(tl.TotalPaused()); share < 0.5 {
		t.Errorf("the trailing pause is %.0f%% of the reported idle; this test is built on it being most of it", share*100)
	}
}
