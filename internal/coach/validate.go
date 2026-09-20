package coach

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"

	"wowinsight/internal/warcraftlogs"
)

// validate matches the reply to the findings it was given and holds every
// sentence in it to what those findings can prove.
//
// It is all-or-nothing on purpose. Falling back per block would put two
// findings on one page in warm prose and a third in the analyser's voice,
// which reads as a page that is half broken — and it would hide the fact that
// the model said something it should not have, on the one page whose whole
// claim is that it does not do that. One failure and the page is deterministic
// and says so.
func validate(in Input, sheet *facts, blocks []block) (Findings, error) {
	byRef := map[string]warcraftlogs.Finding{}
	for _, f := range in.Findings {
		r := ref(f)
		if _, clash := byRef[r]; clash {
			// Two findings this process cannot tell apart. Nothing the model
			// says about either can be attached to the right one, so nothing
			// it says is used.
			return nil, fmt.Errorf("%w: two findings share the reference %q", ErrUntrustworthy, r)
		}
		byRef[r] = f
	}
	if len(blocks) != len(byRef) {
		return nil, fmt.Errorf("%w: %d blocks for %d findings", ErrUntrustworthy, len(blocks), len(byRef))
	}

	lexicon := sheet.lexicon(in)
	seen := map[string]bool{}
	out := make(Findings, 0, len(blocks))
	for _, b := range blocks {
		f, known := byRef[b.Ref]
		if !known {
			return nil, fmt.Errorf("%w: it worded a finding that was never given to it", ErrUntrustworthy)
		}
		if seen[b.Ref] {
			return nil, fmt.Errorf("%w: it worded one finding twice", ErrUntrustworthy)
		}
		seen[b.Ref] = true

		if strings.TrimSpace(b.Title) == "" || strings.TrimSpace(b.Detail) == "" {
			return nil, fmt.Errorf("%w: a finding came back with nothing said about it", ErrUntrustworthy)
		}
		why := strings.TrimSpace(b.Why)
		if b.SetAside && why == "" {
			return nil, fmt.Errorf("%w: it set a finding aside without saying why", ErrUntrustworthy)
		}
		if !b.SetAside {
			// A reason attached to a finding that was not set aside is never
			// shown, so it must not be carried around as though it were.
			why = ""
		}

		// The claim is held to the evidence printed under it; the reason for
		// setting a finding aside is held to the pull. See facts.numerals
		// for why those are different questions.
		claim := evidenceNumerals(f)
		aboutThePull := append(slices.Clone(claim), sheet.numerals()...)
		for _, prose := range []struct {
			text    string
			allowed []numeral
		}{
			{b.Title, claim},
			{b.Detail, claim},
			{why, aboutThePull},
		} {
			if err := checkNumbers(prose.text, prose.allowed); err != nil {
				return nil, err
			}
			if err := checkVocabulary(prose.text, lexicon); err != nil {
				return nil, err
			}
		}

		// The finding is copied and only its words replaced. Severity, At,
		// RuleID and Evidence are the analyser's and stay exactly as they
		// were — the claim may be reworded, the working behind it may not.
		worded := f
		worded.Title = strings.TrimSpace(b.Title)
		worded.Detail = strings.TrimSpace(b.Detail)
		out = append(out, Written{Finding: worded, SetAside: b.SetAside, Why: why})
	}
	// Set-aside findings go last whatever order they came back in: they are
	// the ones the player is being told they need not worry about.
	slices.SortStableFunc(out, func(a, b Written) int {
		switch {
		case a.SetAside == b.SetAside:
			return 0
		case a.SetAside:
			return 1
		default:
			return -1
		}
	})
	return out, nil
}

// numKind is what sort of quantity a numeral is. Kinds never bridge: 4:53 and
// "293 seconds" are the same number of seconds and are not interchangeable,
// because one is a moment in the pull and the other is a length of time, and
// a model that swaps them has said something the evidence does not.
type numKind uint8

const (
	kindCount numKind = iota
	kindDuration
	kindClock
	kindPercent
)

// numeral is one quantity read out of a string.
type numeral struct {
	kind numKind
	// val is seconds for a duration and a clock, points for a percentage,
	// and the number as written for a count.
	val float64
	// places is how many decimals it was written with, which is what says
	// whether it is a correct rounding of something in the evidence.
	places int
}

// evidenceNumerals is every number a finding is allowed to state: the ones in
// the working the player can see, plus the moment the finding points at.
//
// **Deliberately not the finding's own Title and Detail.** Deriving what may
// be said from the sentence being replaced is circular — it would let the
// model keep a number the analyser stated without ever having shown it. This
// way round buys a real invariant instead, and TestEveryFindingIsSelfEvident
// in internal/warcraftlogs holds every rule to it: a rule whose sentence
// contains a number it did not put in Evidence fails there, next to the rule,
// rather than quietly here.
func evidenceNumerals(f warcraftlogs.Finding) []numeral {
	var out []numeral
	out = append(out, scanNumerals(f.Timestamp())...)
	for _, e := range f.Evidence {
		out = append(out, scanNumerals(e.Value)...)
	}
	return out
}

// checkNumbers rejects prose stating a quantity the evidence does not.
func checkNumbers(prose string, allowed []numeral) error {
	if word, bad := spelledNumber(prose); bad {
		return fmt.Errorf("%w: it wrote a number as a word (%q)", ErrUntrustworthy, word)
	}
	for _, n := range scanNumerals(prose) {
		if !admits(allowed, n) {
			return fmt.Errorf("%w: it stated a number the evidence does not", ErrUntrustworthy)
		}
	}
	return nil
}

// admits reports whether the evidence backs a numeral, allowing for the one
// rewrite that is honest: stating a rounded form of a number the evidence
// gives more precisely. The tolerance is exactly half of the last place the
// model wrote, so 37.4s may be written "37s" and 69s may not be written
// "70s".
func admits(allowed []numeral, n numeral) bool {
	tol := 0.5*math.Pow(10, -float64(n.places)) + 1e-9
	for _, a := range allowed {
		if a.kind == n.kind && math.Abs(a.val-n.val) <= tol {
			return true
		}
	}
	return false
}

// unitFactor maps a unit onto its kind and how many of the kind's own units
// one of it is worth.
var unitFactor = map[string]struct {
	kind numKind
	mul  float64
}{
	"%":       {kindPercent, 1},
	"percent": {kindPercent, 1},
	"ms":      {kindDuration, 0.001},
	"s":       {kindDuration, 1},
	"sec":     {kindDuration, 1},
	"secs":    {kindDuration, 1},
	"second":  {kindDuration, 1},
	"seconds": {kindDuration, 1},
	"m":       {kindDuration, 60},
	"min":     {kindDuration, 60},
	"mins":    {kindDuration, 60},
	"minute":  {kindDuration, 60},
	"minutes": {kindDuration, 60},
	"k":       {kindCount, 1000},
}

// scanNumerals reads every quantity out of a string.
func scanNumerals(s string) []numeral {
	var out []numeral
	r := []rune(s)
	for i := 0; i < len(r); {
		if !unicode.IsDigit(r[i]) {
			i++
			continue
		}
		n, next := readNumeral(r, i)
		out = append(out, n)
		i = next
	}
	return out
}

// readNumeral reads one quantity starting at a digit and returns where it
// ended.
func readNumeral(r []rune, i int) (numeral, int) {
	start := i
	for i < len(r) && unicode.IsDigit(r[i]) {
		i++
	}
	// A clock: one or more ":dd" groups. 4:53 is 293 seconds and a moment,
	// never a count of four and a count of fifty-three.
	if i < len(r) && r[i] == ':' && i+1 < len(r) && unicode.IsDigit(r[i+1]) {
		total := digits(r[start:i])
		for i < len(r) && r[i] == ':' && i+1 < len(r) && unicode.IsDigit(r[i+1]) {
			i++
			from := i
			for i < len(r) && unicode.IsDigit(r[i]) {
				i++
			}
			total = total*60 + digits(r[from:i])
		}
		return numeral{kind: kindClock, val: total}, i
	}

	whole := digits(r[start:i])
	places := 0
	if i+1 < len(r) && (r[i] == '.' || r[i] == ',') && unicode.IsDigit(r[i+1]) {
		i++
		from := i
		for i < len(r) && unicode.IsDigit(r[i]) {
			i++
		}
		places = i - from
		whole += digits(r[from:i]) / math.Pow(10, float64(places))
	}

	// One optional space, then a unit. "37s" and "37 s" are the same claim.
	at := i
	if at < len(r) && r[at] == ' ' {
		at++
	}
	if at < len(r) && r[at] == '%' {
		return numeral{kind: kindPercent, val: whole, places: places}, at + 1
	}
	from := at
	for at < len(r) && unicode.IsLetter(r[at]) {
		at++
	}
	if unit, ok := unitFactor[strings.ToLower(string(r[from:at]))]; ok {
		return numeral{kind: unit.kind, val: whole * unit.mul, places: places}, at
	}
	return numeral{kind: kindCount, val: whole, places: places}, i
}

func digits(r []rune) float64 {
	var n float64
	for _, c := range r {
		n = n*10 + float64(c-'0')
	}
	return n
}

// numberWords are the ways a quantity can be written without a digit. A
// spelled number is invisible to every check above, so it is refused outright
// and the prompt says not to write one.
var numberWords = []string{
	"zero", "one", "two", "three", "four", "five", "six", "seven", "eight",
	"nine", "ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen",
	"sixteen", "seventeen", "eighteen", "nineteen", "twenty", "thirty",
	"forty", "fifty", "sixty", "seventy", "eighty", "ninety", "hundred",
	"thousand",
}

// countedNouns are what a spelled number is refused in front of. The
// restriction matters: "one" is ordinary English ("one of your cooldowns")
// and refusing it everywhere would refuse good prose, while "one use" and
// "thirty seconds" are quantities dressed as words.
var countedNouns = []string{
	"second", "seconds", "sec", "secs", "minute", "minutes", "min", "mins",
	"use", "uses", "cast", "casts", "time", "times", "percent",
}

// proportionWords are numeric claims with no numeral to check at all. They are
// refused whenever they are used as proportions — "half of the pull" — and
// left alone otherwise, so an ability called Quarter would still be nameable.
var proportionWords = []string{"half", "third", "quarter", "most", "majority", "all"}

// alwaysCounts are quantities that are only ever quantities.
var alwaysCounts = []string{"twice", "thrice", "double", "triple"}

// spelledNumber reports a quantity written as a word, and which word it was.
func spelledNumber(prose string) (string, bool) {
	words := strings.FieldsFunc(strings.ToLower(prose), func(r rune) bool {
		return !unicode.IsLetter(r)
	})
	for i, w := range words {
		if slices.Contains(alwaysCounts, w) {
			return w, true
		}
		next := ""
		if i+1 < len(words) {
			next = words[i+1]
		}
		if slices.Contains(numberWords, w) && slices.Contains(countedNouns, next) {
			return w + " " + next, true
		}
		if slices.Contains(proportionWords, w) && next == "of" {
			return w + " of", true
		}
	}
	return "", false
}

// checkVocabulary rejects prose naming something that is not in this pull.
//
// This is the check that matters more than the numbers, and it is worth being
// clear why. "You should have used Rune of Power before the adds" contains no
// numeral at all: it sails through every numeric check ever written, reads
// with complete authority, and is advice about a talent the player may not
// even have. Proper nouns are where invention actually lives on a page like
// this, so every capitalised word that is not opening a sentence has to be a
// word this pull contains.
func checkVocabulary(prose string, lexicon map[string]bool) error {
	for _, w := range midSentenceCapitals(prose) {
		if !lexicon[w] && w != "i" {
			return fmt.Errorf("%w: it named %q, which is not in this pull", ErrUntrustworthy, w)
		}
	}
	return nil
}

// midSentenceCapitals returns the lowercased capitalised words that are not
// the first word of a sentence, since that capital is the grammar's and says
// nothing.
func midSentenceCapitals(prose string) []string {
	var out []string
	atStart := true
	r := []rune(prose)
	for i := 0; i < len(r); {
		switch {
		case unicode.IsLetter(r[i]) || r[i] == '\'':
			from := i
			for i < len(r) && (unicode.IsLetter(r[i]) || r[i] == '\'') {
				i++
			}
			word := string(r[from:i])
			if !atStart && unicode.IsUpper([]rune(word)[0]) {
				out = append(out, strings.ToLower(word))
			}
			atStart = false
		case r[i] == '.' || r[i] == '!' || r[i] == '?' || r[i] == '\n':
			atStart = true
			i++
		default:
			i++
		}
	}
	return out
}
