package fixture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// The values every recording carries in place of the real ones. They are
// chosen so that a rendered page still reads like a raid, and so that the code
// passes ParseReportCode's sixteen-alphanumerics check.
const (
	FakeCode  = "ExampleReport123"
	FakeOwner = "Testowner"
	FakeTitle = "Recorded raid night"
	FakeRealm = "Testrealm"
	FakePet   = "Testpet"
)

// A rule replaces one real value with one fake one, everywhere.
type rule struct {
	real, fake string
	kind       string // what it is, for a refusal message that must not say what it was
}

// redactor is built from every body a recording holds and then applied to
// each of them.
type redactor struct {
	rules []rule
}

// newRedactor reads the roster out of the recorded bodies and decides the
// pseudonyms. The real names come from the typed, complete places — the
// report's owner and code, and masterData.actors, which the API returns for
// every player in the report — and are then replaced wherever they occur,
// because the tables and death windows are untyped JSON that can carry a name
// in positions nobody has enumerated.
//
// Pseudonyms are assigned deterministically, by sorting the distinct names, so
// that re-recording the same report yields the same fixture. Each carries the
// player's class so a fixture reads honestly: Testmage, Testpriest, Testmage2.
func newRedactor(bodies [][]byte) (*redactor, error) {
	type actor struct{ name, server, class string }
	var (
		actors  []actor
		servers []string
		code    string
		owner   string
	)
	for _, body := range bodies {
		tree, err := decode(body)
		if err != nil {
			return nil, err
		}
		report, _ := lookup(tree, "data", "reportData", "report").(map[string]any)
		if report == nil {
			continue
		}
		if c, _ := report["code"].(string); c != "" {
			code = c
		}
		if o, _ := lookup(report, "owner", "name").(string); o != "" {
			owner = o
		}
		list, _ := lookup(report, "masterData", "actors").([]any)
		for _, item := range list {
			m, _ := item.(map[string]any)
			name, _ := m["name"].(string)
			server, _ := m["server"].(string)
			class, _ := m["subType"].(string)
			if name == "" {
				continue
			}
			if !slices.ContainsFunc(actors, func(a actor) bool { return strings.EqualFold(a.name, name) }) {
				actors = append(actors, actor{name, server, strings.ToLower(class)})
			}
			if server != "" && !slices.ContainsFunc(servers, func(s string) bool { return strings.EqualFold(s, server) }) {
				servers = append(servers, server)
			}
		}
	}
	if code == "" {
		return nil, fmt.Errorf("fixture: no report code in any recorded response, so nothing can be redacted against it")
	}

	r := &redactor{}
	// Longest first, so that a name which is a prefix of another is never
	// replaced inside it. Whole-word matching already prevents that; the order
	// makes it not depend on the matching.
	slices.SortFunc(actors, func(a, b actor) int {
		if d := len(b.name) - len(a.name); d != 0 {
			return d
		}
		return strings.Compare(a.name, b.name)
	})
	perClass := map[string]int{}
	for _, a := range actors {
		class := a.class
		if class == "" {
			class = "player"
		}
		perClass[class]++
		fake := "Test" + class
		if n := perClass[class]; n > 1 {
			fake += strconv.Itoa(n)
		}
		r.rules = append(r.rules, rule{a.name, fake, "a character name"})
	}
	slices.Sort(servers)
	for i, s := range servers {
		fake := FakeRealm
		if i > 0 {
			fake += strconv.Itoa(i + 1)
		}
		r.rules = append(r.rules, rule{s, fake, "a server name"})
		// Cross-realm names appear as Name-Server with the server condensed to
		// one word, so a server with spaces or apostrophes needs that form too.
		if condensed := condense(s); condensed != s {
			r.rules = append(r.rules, rule{condensed, fake, "a server name"})
		}
	}
	r.rules = append(r.rules, rule{code, FakeCode, "the report code"})
	if owner != "" && !slices.ContainsFunc(actors, func(a actor) bool { return strings.EqualFold(a.name, owner) }) {
		r.rules = append(r.rules, rule{owner, FakeOwner, "the report owner"})
	}
	return r, nil
}

// apply redacts one body and returns it re-encoded compactly. It then reads
// its own output back and refuses to return anything a real value survived in,
// naming what kind of value it was and never the value itself.
func (r *redactor) apply(body []byte) ([]byte, error) {
	tree, err := decode(body)
	if err != nil {
		return nil, err
	}
	if report, ok := lookup(tree, "data", "reportData", "report").(map[string]any); ok {
		if _, has := report["title"]; has {
			report["title"] = FakeTitle
		}
	}
	tree = r.walk(tree)

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(tree); err != nil {
		return nil, err
	}
	text := strings.TrimSuffix(out.String(), "\n")
	for _, rule := range r.rules {
		if _, n := replaceWord(text, rule.real, ""); n > 0 {
			return nil, fmt.Errorf("fixture: %s survived redaction; refusing to write", rule.kind)
		}
	}
	return []byte(text), nil
}

// walk applies every rule to every string in the tree, and handles the two
// things that are not names but are still someone's: a character GUID is an
// identifier nothing here reads, so it is zeroed; a pet's name is chosen by
// its owner, so it is replaced. Pets are the one place a key is trusted — a
// pet called Echo or Bear cannot be replaced by value without taking the
// spells of the same name with it.
func (r *redactor) walk(v any) any {
	switch v := v.(type) {
	case map[string]any:
		if v["type"] == "Pet" {
			if _, has := v["name"]; has {
				v["name"] = FakePet
			}
		}
		for k, child := range v {
			if k == "guid" {
				v[k] = json.Number("0")
				continue
			}
			v[k] = r.walk(child)
		}
		return v
	case []any:
		for i, child := range v {
			v[i] = r.walk(child)
		}
		return v
	case string:
		for _, rule := range r.rules {
			v, _ = replaceWord(v, rule.real, rule.fake)
		}
		return v
	}
	return v
}

// replaceWord replaces every whole-word, case-insensitive occurrence of real
// in s, and reports how many it found. "Whole word" means not touching a letter
// or digit on either side, so a player named Fire leaves "Fireball" alone but
// is caught in "Fire-Testrealm". It works on runes rather than bytes because
// character names carry accents, and regexp's \b does not.
func replaceWord(s, real, fake string) (string, int) {
	rs, target := []rune(s), []rune(strings.ToLower(real))
	if len(target) == 0 || len(rs) < len(target) {
		return s, 0
	}
	var out []rune
	count := 0
	for i := 0; i < len(rs); {
		if matchAt(rs, target, i) {
			out = append(out, []rune(fake)...)
			i += len(target)
			count++
			continue
		}
		out = append(out, rs[i])
		i++
	}
	if count == 0 {
		return s, 0
	}
	return string(out), count
}

func matchAt(rs, target []rune, i int) bool {
	end := i + len(target)
	if end > len(rs) {
		return false
	}
	for j, t := range target {
		if unicode.ToLower(rs[i+j]) != t {
			return false
		}
	}
	if i > 0 && wordRune(rs[i-1]) {
		return false
	}
	return end == len(rs) || !wordRune(rs[end])
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// condense turns "Twisting Nether" into "TwistingNether" and "Kel'Thuzad" into
// "KelThuzad", the forms a server takes after the hyphen in a cross-realm name.
func condense(server string) string {
	return strings.Map(func(r rune) rune {
		if wordRune(r) {
			return r
		}
		return -1
	}, server)
}

// decode parses a body keeping numbers as their original text, so that a
// timestamp or a total survives the round trip byte for byte.
func decode(body []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, fmt.Errorf("fixture: decode recorded response: %w", err)
	}
	return tree, nil
}

// lookup follows a path of object keys, returning nil the moment one is missing.
func lookup(v any, path ...string) any {
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}
