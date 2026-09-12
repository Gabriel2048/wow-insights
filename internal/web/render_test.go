package web

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"wowinsight/internal/view"
)

// render executes one template and fails the test if it errors, returning the
// output for inspection. html/template streams as it goes, so a template that
// fails halfway has already written a partial body — which is exactly the
// failure this suite exists to catch, and why the output is buffered here.
func render(t *testing.T, name string, data any) string {
	t.Helper()
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatalf("ParseTemplates() returned error: %v", err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, name, data); err != nil {
		t.Fatalf("Execute(%q) returned error: %v", name, err)
	}
	return buf.String()
}

// The templates are parsed at process start and never by the compiler, so
// without this a syntax error passes gofmt, vet, the linter, the tests and the
// build, and panics on the first request after deploy.
func TestTemplatesParse(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatalf("ParseTemplates() returned error: %v", err)
	}
	for _, name := range pages {
		if tpl.pages[name] == nil {
			t.Errorf("page %q was not parsed", name)
		}
	}
}

// A renamed field does not break the build: html/template resolves fields at
// execute time. This asserts on real content rather than a nil error, because a
// template that silently rendered nothing would satisfy the error check.
func TestFightPageRenders(t *testing.T) {
	page := render(t, "fight.html", fullFightPage())

	// ZgotmplZ is html/template's refusal marker: it appears when a value
	// reaches a context the escaper cannot prove safe, such as a CSS or URL
	// position. One assertion covers every future style= interpolation.
	if strings.Contains(page, "ZgotmplZ") {
		t.Error("page contains ZgotmplZ, so a value was refused by the contextual escaper")
	}
	if strings.Contains(page, "<no value>") {
		t.Error("page contains <no value>, so a field the template asked for does not exist")
	}

	for _, want := range []string{
		"Testmage",               // the selected player
		"The Coiled Altar",       // the encounter
		"Pyroblast",              // a cast, so the cast track rendered
		"Time Warp",              // the lust lane
		"Rallying Cry",           // the raid cooldown lane
		"Dread Bolt",             // the boss lane
		"Stage One",              // the phase lane
		`data-total-ms="302000"`, // the ruler's x-domain
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

// Every lane is optional. These are the branches a player with no timeline, no
// damage graph or no raid cooldowns takes — the last of which is most of them.
func TestFightPageRendersWithoutTheOptionalLanes(t *testing.T) {
	noTimeline := fullFightPage()
	noTimeline.Timeline = nil
	noTimeline.Player = nil
	noTimeline.SelectedID = 0
	if page := render(t, "fight.html", noTimeline); !strings.Contains(page, "Pick a player above") {
		t.Error("with no player selected the page must invite one to be picked")
	}

	noDPS := fullTimeline()
	noDPS.DPS = nil
	noDPS.Taken = nil
	if page := render(t, "fight.html", pageWith(noDPS)); strings.Contains(page, "ZgotmplZ") {
		t.Error("page with no damage graph contains ZgotmplZ")
	}

	bareTimeline := fullTimeline()
	bareTimeline.RaidCDs = nil
	bareTimeline.Cooldowns = nil
	bareTimeline.Lusts = nil
	bareTimeline.Phases = nil
	page := render(t, "fight.html", pageWith(bareTimeline))
	if !strings.Contains(page, "No Bloodlust, Heroism or Time Warp") {
		t.Error("a pull with no lust must say so rather than render an empty lane")
	}
}

func TestIndexPageRenders(t *testing.T) {
	page := render(t, "index.html", pageData{Report: reportWithTwoPulls()})
	for _, want := range []string{"Fixture raid night", "The Coiled Altar", "Kill", "Wipe"} {
		if !strings.Contains(page, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

// The error is echoed back beside the URL that produced it, so both go through
// the escaper on a path a user controls.
func TestIndexPageShowsAnError(t *testing.T) {
	page := render(t, "index.html", pageData{
		URL:   `https://example.com/"><script>alert(1)</script>`,
		Error: "That does not look like a Warcraft Logs report link.",
	})
	if !strings.Contains(page, "That does not look like a Warcraft Logs report link.") {
		t.Error("the error message was not rendered")
	}
	if strings.Contains(page, "<script>alert(1)</script>") {
		t.Error("a script tag from the submitted URL reached the page unescaped")
	}
}

// optionalScriptLookups lists the ids the inline script resolves that the page
// does not always emit, and why each is tolerable — or, in one case, is not.
//
// The difference between the two kinds is the whole point of this list. A
// lookup the script null-checks degrades quietly; one it dereferences straight
// away throws. Both look identical in the template.
var optionalScriptLookups = map[string]string{
	// Guarded. seriesFrom() opens with `if (!lane || !lane.dataset.values)
	// return null`, so a missing lane disables the hover readout rather than
	// throwing.
	"dpsLane":   "guarded by seriesFrom",
	"takenLane": "guarded by seriesFrom",

	// Every write is behind `if (readout && …)`: a player with no damage
	// series — a healer, or anyone who died at the pull — has no readout,
	// and an unguarded write threw once per animation frame on hover.
	"dpsReadout": "guarded by an if on the element",
}

// script is the fight page's script, read from the embedded static files —
// the same bytes the page links to.
func script(t *testing.T) string {
	t.Helper()
	data, err := fs.ReadFile(staticFS, "static/timeline.js")
	if err != nil {
		t.Fatalf("read timeline.js: %v", err)
	}
	return string(data)
}

// scriptLookups derives the ids the script resolves, so the contract between
// the script and the markup is read from the script rather than transcribed.
// A hardcoded list drifts silently; this cannot.
func scriptLookups(t *testing.T) []string {
	t.Helper()
	var ids []string
	for _, m := range regexp.MustCompile(`getElementById\('([A-Za-z0-9_-]+)'\)`).FindAllStringSubmatch(script(t), -1) {
		ids = append(ids, m[1])
	}
	if len(ids) < 5 {
		t.Fatalf("found %d getElementById calls in timeline.js, want at least 5 (the regex is probably wrong, not the script)", len(ids))
	}
	return ids
}

// The script and the markup it drives are served by the same binary from the
// same commit, so the server must emit every element the script resolves.
func TestFightPageHasTheElementsTheScriptLooksUp(t *testing.T) {
	page := render(t, "fight.html", fullFightPage())
	lookups := scriptLookups(t)
	for _, id := range lookups {
		if !strings.Contains(page, `id="`+id+`"`) {
			t.Errorf("the script resolves #%s but the page emits no element with that id", id)
		}
	}

	// querySelector arguments cannot be derived the same way, because a
	// selector is not an id. This is the short list that must stay in step.
	for _, sel := range []string{
		`class="tick`, `class="bcast"`, `class="cdblock"`, `class="rcdblock"`,
		`class="gap"`, `class="idlerow"`, `class="zoom`,
	} {
		if !strings.Contains(page, sel) {
			t.Errorf("the script queries for %s but the page emits none", sel)
		}
	}

	for _, attr := range []string{"data-duration-ms", "data-total-ms", "data-lead-ms"} {
		if !strings.Contains(page, attr) {
			t.Errorf("the script reads %s but the page does not emit it", attr)
		}
	}
}

// The same derivation on a page with no damage graph, which is where the
// contract is currently broken.
func TestFightPageWithoutDPSIsMissingOnlyTheKnownIDs(t *testing.T) {
	noDPS := fullTimeline()
	noDPS.DPS = nil
	noDPS.Taken = nil
	page := render(t, "fight.html", pageWith(noDPS))

	for _, id := range scriptLookups(t) {
		if strings.Contains(page, `id="`+id+`"`) {
			continue
		}
		if _, known := optionalScriptLookups[id]; known {
			continue
		}
		t.Errorf("the script resolves #%s and a page with no damage graph does not emit it. "+
			"Either guard the lookup, emit the element unconditionally, or add it to "+
			"optionalScriptLookups saying which of those you chose and why", id)
	}
}

// index.html builds fight links from a string literal and main.go registers a
// route pattern from another, with nothing between them. This resolves every
// link the page actually emits against the mux that actually serves it.
func TestIndexLinksMatchARegisteredRoute(t *testing.T) {
	page := render(t, "index.html", pageData{Report: reportWithTwoPulls()})
	links := regexp.MustCompile(`href="(/report/[^"]+)"`).FindAllStringSubmatch(page, -1)
	if len(links) == 0 {
		t.Fatal("the index page emitted no fight links, so this test is asserting nothing")
	}

	mux := newTestServer(t, fakeWCL{}).routes()
	for _, m := range links {
		req := httptest.NewRequest("GET", m[1], nil)
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("index.html links to %s but no route matches it", m[1])
		}
	}
}

// The player picker's option values are what come back as ?player=, and the
// handler parses them with strconv.Atoi.
func TestFightPagePlayerOptionsAreNumericActorIDs(t *testing.T) {
	page := render(t, "fight.html", fullFightPage())
	values := regexp.MustCompile(`<option value="([^"]*)"`).FindAllStringSubmatch(page, -1)
	if len(values) < 3 {
		t.Fatalf("found %d option values, want the placeholder plus one per player", len(values))
	}
	if values[0][1] != "" {
		t.Errorf("the first option value is %q, want empty — that is what makes 'no selection' fall through the handler's raw != \"\" test", values[0][1])
	}
	for _, v := range values[1:] {
		if strings.TrimLeft(v[1], "0123456789") != "" {
			t.Errorf("option value %q is not a number, so strconv.Atoi in the handler will reject it", v[1])
		}
	}
}

// A report title and a character name are typed by other people and arrive
// through the API. They reach the page through html/template, which escapes
// them — this pins that, because switching to text/template or wrapping a value
// in template.HTML would silently remove it.
func TestPlayerSuppliedTextIsEscaped(t *testing.T) {
	detail := fightDetail()
	detail.ReportTitle = `<script>alert("title")</script>`
	detail.Players[0].Name = `<img src=x onerror=alert(1)>`
	player := detail.Players[0]

	page := render(t, "fight.html", fightPageData{
		Detail: detail, Fight: view.Fight{Fight: detail.Fight},
		SelectedID: player.ActorID, Player: &player,
		Timeline: laidOut(fullTimeline()),
	})
	for _, unwanted := range []string{`<script>alert("title")</script>`, `<img src=x onerror=alert(1)>`} {
		if strings.Contains(page, unwanted) {
			t.Errorf("%q reached the page unescaped", unwanted)
		}
	}
}

// The page links to its stylesheet and script by content-hashed paths, and
// those paths serve the files with a cache lifetime that a hash makes safe;
// a stale hash is a 404, not a forever-cached wrong file.
func TestStaticAssetsAreHashedAndCacheable(t *testing.T) {
	page := render(t, "fight.html", fullFightPage())
	links := regexp.MustCompile(`/static/([0-9a-f]{12})/(app\.css|timeline\.js)`).FindAllStringSubmatch(page, -1)
	if len(links) != 2 {
		t.Fatalf("the page links %d hashed assets, want the stylesheet and the script: %v", len(links), links)
	}
	if strings.Contains(page, "<script>") {
		t.Error("the page still carries an inline script; the CSP #7 wants needs none")
	}
	if strings.Contains(page, "<style>") {
		t.Error("the page still carries an inline stylesheet")
	}
	// An inline handler is an inline script to a Content-Security-Policy.
	if m := regexp.MustCompile(`\son[a-z]+="`).FindString(page); m != "" {
		t.Errorf("the page carries an inline event handler (%q); the script must bind it instead", strings.TrimSpace(m))
	}
	for _, m := range links {
		rec := get(t, fakeWCL{}, m[0])
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d", m[0], rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
			t.Errorf("%s: Cache-Control = %q, want immutable", m[0], cc)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/") {
			t.Errorf("%s: Content-Type = %q", m[0], ct)
		}
		stale := strings.Replace(m[0], m[1], "000000000000", 1)
		if rec := get(t, fakeWCL{}, stale); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 for a stale hash", stale, rec.Code)
		}
	}
}

// The index and error pages are narrow centered forms; the fight page is
// full width. Their stylesheet rules are scoped by a class on <body> that
// each page sets — without it, the index's centered flex body applied to the
// fight page and squeezed the timeline into a column (found in review).
// Whether the stylesheet honours the class is the stylesheet's business.
func TestEachPageSetsItsBodyClass(t *testing.T) {
	for page, data := range map[string]any{
		"fight.html": fullFightPage(),
		"index.html": pageData{},
		"error.html": errorPageData{Status: 404, Message: "no"},
	} {
		class := strings.TrimSuffix(page, ".html")
		if !strings.Contains(render(t, page, data), `<body class="`+class+`">`) {
			t.Errorf("%s does not set body.%s", page, class)
		}
	}
}
