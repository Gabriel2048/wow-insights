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
	// FakeGuild replaces a guild's name. Only a rankings page carries one:
	// the subject's own report never names a guild.
	FakeGuild = "Testguild"
)

// A rule replaces one real value with one fake one, everywhere.
type rule struct {
	real, fake string
	kind       string // what it is, for a refusal message that must not say what it was
	// others marks a value learned only from a rankings page: another
	// player's name, realm or guild. It is applied to the bodies that are
	// about those players and nowhere else. A live page held a realm whose
	// name is also in an enchant's, and guilds named for ordinary words that
	// are also in spell and zone names; applied everywhere, they rewrote the
	// subject's own pull in places that never named anyone.
	others bool
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
	type actor struct {
		name, server, class string
		// peer marks a player found only on a rankings page. Peers are
		// numbered after the subject's roster, so adding a comparison to a
		// recording never renames anyone already in it.
		peer bool
	}
	var (
		actors  []actor
		servers []string
		// peerServers are realms found only on a rankings page, numbered
		// after the subject's for the same reason peers are.
		peerServers []string
		guilds      []string
		codes       []string // the subject's own report, and any it links to
		peers       []string // other players' reports, from a rankings page
		owner       string
	)
	addActor := func(name, server, class string, peer bool) {
		if name == "" {
			return
		}
		i := slices.IndexFunc(actors, func(a actor) bool { return strings.EqualFold(a.name, name) })
		switch {
		case i < 0:
			actors = append(actors, actor{name, server, strings.ToLower(class), peer})
		case !peer:
			// Seen on a rankings page first, then found in the subject's own
			// roster — a raid-mate who is also a top performer. They are the
			// subject's, so they are numbered with the subject's.
			actors[i].peer = false
		}
		if server == "" {
			return
		}
		list := &servers
		if peer {
			list = &peerServers
		}
		if !slices.ContainsFunc(servers, func(s string) bool { return strings.EqualFold(s, server) }) &&
			!slices.ContainsFunc(*list, func(s string) bool { return strings.EqualFold(s, server) }) {
			*list = append(*list, server)
		}
	}
	// The recorder holds its exchanges in a map, so the order they arrive in
	// is not the order they were recorded in. Sorting makes every rule below
	// a function of the content alone: without it, which report code became
	// ExampleReport123 in a two-report recording was decided by map iteration
	// and differed between runs, so one recording could be clean and the next
	// one leak.
	bodies = slices.SortedFunc(slices.Values(bodies), bytes.Compare)
	for _, body := range bodies {
		tree, err := decode(body)
		if err != nil {
			return nil, err
		}
		// A rankings page is other people: up to a hundred of them, each with
		// a name, a server, a guild and the report their pull is in. Every one
		// becomes a rule, so the same player is the same pseudonym here and in
		// their own pull, which is recorded alongside.
		rows, _ := lookup(tree, "data", "worldData", "encounter", "characterRankings", "rankings").([]any)
		for _, item := range rows {
			m, _ := item.(map[string]any)
			name, _ := m["name"].(string)
			class, _ := m["class"].(string)
			server, _ := lookup(m, "server", "name").(string)
			addActor(name, server, class, true)
			if g, _ := lookup(m, "guild", "name").(string); g != "" && !slices.ContainsFunc(guilds, func(x string) bool { return strings.EqualFold(x, g) }) {
				guilds = append(guilds, g)
			}
			if c, _ := lookup(m, "report", "code").(string); c != "" && !slices.Contains(peers, c) {
				peers = append(peers, c)
			}
		}

		report, _ := lookup(tree, "data", "reportData", "report").(map[string]any)
		if report == nil {
			continue
		}
		if c, _ := report["code"].(string); c != "" && !slices.Contains(codes, c) {
			codes = append(codes, c)
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
			addActor(name, server, class, false)
		}
	}
	if len(codes)+len(peers) == 0 {
		return nil, fmt.Errorf("fixture: no report code in any recorded response, so nothing can be redacted against it")
	}
	// The subject's reports are numbered first, so the subject is always
	// ExampleReport123 however the peers' codes happen to sort — every test
	// and the offline server ask for it by that name. Peers follow, in their
	// own sorted order, which keeps the numbering a function of the content.
	slices.Sort(codes)
	slices.Sort(peers)
	for _, c := range peers {
		if !slices.Contains(codes, c) {
			codes = append(codes, c)
		}
	}

	r := &redactor{}
	// The subject's roster first, then anyone found only on a rankings page;
	// within each, longest first, so that a name which is a prefix of another
	// is never replaced inside it. Whole-word matching already prevents that;
	// the order makes it not depend on the matching.
	//
	// Subject first is what keeps a recording consistent across runs. The
	// kill is recorded with a comparison and the wipe without one, and if the
	// peers were numbered in among the roster, the player would be Testmage in
	// one file and Testmage7 in the other.
	slices.SortFunc(actors, func(a, b actor) int {
		if a.peer != b.peer {
			if a.peer {
				return 1
			}
			return -1
		}
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
		r.rules = append(r.rules, rule{a.name, fake, "a character name", a.peer})
	}
	// The subject's realms first, then realms only peers came from. The same
	// hazard as the roster, and the test that pins it found this one second:
	// a peer's realm sorting ahead of the subject's renamed the subject's.
	slices.Sort(servers)
	slices.Sort(peerServers)
	subjects := len(servers)
	for _, s := range peerServers {
		if !slices.ContainsFunc(servers, func(x string) bool { return strings.EqualFold(x, s) }) {
			servers = append(servers, s)
		}
	}
	for i, s := range servers {
		fake := FakeRealm
		if i > 0 {
			fake += strconv.Itoa(i + 1)
		}
		others := i >= subjects
		r.rules = append(r.rules, rule{s, fake, "a server name", others})
		// Cross-realm names appear as Name-Server with the server condensed to
		// one word, so a server with spaces or apostrophes needs that form too.
		if condensed := condense(s); condensed != s {
			r.rules = append(r.rules, rule{condensed, fake, "a server name", others})
		}
	}
	slices.Sort(guilds)
	for i, g := range guilds {
		fake := FakeGuild
		if i > 0 {
			fake += strconv.Itoa(i + 1)
		}
		r.rules = append(r.rules, rule{g, fake, "a guild name", true})
	}
	// Every code gets its own pseudonym, deterministically. A recording that
	// reaches into a second report — which is what comparing a player against
	// someone else's log needs — used to name only one of them.
	for i, c := range codes {
		fake := FakeCode
		if i > 0 {
			fake = fmt.Sprintf("ExampleReport%d", 123+i)
		}
		// Codes are applied everywhere: they are random, so one cannot also
		// be a word, and the subject's report links to others by code.
		r.rules = append(r.rules, rule{c, fake, "a report code", false})
	}
	if owner != "" && !slices.ContainsFunc(actors, func(a actor) bool { return strings.EqualFold(a.name, owner) }) {
		r.rules = append(r.rules, rule{owner, FakeOwner, "the report owner", false})
	}
	// Applied longest first, across every kind. Which pseudonym a value gets
	// is settled above; this is only the order they are applied in, and it
	// matters as soon as one real value contains another. A rankings page
	// holds dozens of guilds, and on the top page of one boss a guild's name
	// contained a realm's and another contained a player's — so the realm rule
	// ran first, left "The Testrealm Order" behind, and the guild rule never
	// matched again. The recorder refused to write it, as it should; this is
	// what makes it writable. Ties break on the text so the order is a
	// function of the content alone.
	slices.SortStableFunc(r.rules, func(a, b rule) int {
		if d := len([]rune(b.real)) - len([]rune(a.real)); d != 0 {
			return d
		}
		return strings.Compare(a.real, b.real)
	})
	return r, nil
}

// scopedTo returns the redactor for the recording kept under k. A rankings
// page and a peer's pull are about other players and get every rule; the
// subject's own report gets only the rules its own roster produced, so that
// what is replaced in it is somebody in it.
//
// The refusal after redacting is scoped the same way, and that is safe: a
// value learned only from a rankings page is, by construction, nobody in the
// subject's report — a raid-mate who also ranks is the subject's, and gets a
// rule that applies everywhere.
func (r *redactor) scopedTo(k string) *redactor {
	if aboutOthers(k) {
		return r
	}
	own := &redactor{}
	for _, rule := range r.rules {
		if !rule.others {
			own.rules = append(own.rules, rule)
		}
	}
	return own
}

// aboutOthers reports whether the recording kept under k is other players'.
func aboutOthers(k string) bool {
	op, _, _ := strings.Cut(k, "-")
	return op == "rankings" || op == "peer"
}

// fileFor turns a recorded key into the file it is written as: redacted by
// the same rules as the bodies, then checked for anything real that survived,
// then made a file name. The replay makes the same name from a request that
// already carries the pseudonyms.
func (r *redactor) fileFor(k string) (string, error) {
	redacted := k
	for _, rule := range r.rules {
		redacted, _ = replaceWord(redacted, rule.real, rule.fake)
	}
	for _, rule := range r.rules {
		if _, n := replaceWord(redacted, rule.real, rule.fake); n > 0 {
			return "", fmt.Errorf("fixture: a file name would carry %s; refusing to write", rule.kind)
		}
	}
	name := filename(redacted)
	if name == "" {
		return "", fmt.Errorf("fixture: a recording has no usable file name")
	}
	return name + ".json", nil
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
	// The budget snapshot is the one value that differs on every recording
	// and means nothing offline; pinned, so a re-record touches only what
	// changed, and a snapshot taken near the guard cannot make the replay
	// refuse every page for an hour.
	if data, ok := tree.(map[string]any)["data"].(map[string]any); ok {
		if _, has := data["rateLimitData"]; has {
			data["rateLimitData"] = map[string]any{
				"limitPerHour": json.Number("3600"), "pointsResetIn": json.Number("3600"), "pointsSpentThisHour": json.Number("0"),
			}
		}
	}

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
	// The check above can only fail on a value there was already a rule for,
	// which is the wrong way round: a payload nobody taught this package to
	// read produces no rules, so nothing can survive, so it passes. verify
	// asks the opposite question — is every field that carries a person
	// holding a pseudonym, and is every shape here one we know how to scrub.
	if err := r.verify(tree); err != nil {
		return nil, err
	}
	return []byte(text), nil
}

// walk applies every rule to every string in the tree, and handles the two
// things that are not names but are still someone's: a character GUID is an
// identifier nothing here reads, so it is zeroed; a pet's name is chosen by
// its owner, so it is replaced — under a pet actor's name and under the
// petName a table entry carries. Pets are the one place a key is trusted: a
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
			switch k {
			case "guid":
				v[k] = json.Number("0")
				continue
			case "petName":
				v[k] = FakePet
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
