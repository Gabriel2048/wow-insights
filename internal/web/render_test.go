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
	"wowinsight/internal/warcraftlogs"
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
		`class="pause"`, `class="pauserow"`, `class="zoom`,
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
	for name, data := range pageFixtures(t) {
		t.Run(name, func(t *testing.T) { assertAssetsAreHashedAndCacheable(t, render(t, name, data)) })
	}
}

func assertAssetsAreHashedAndCacheable(t *testing.T, page string) {
	t.Helper()
	links := regexp.MustCompile(`/static/([0-9a-f]{12})/([A-Za-z0-9._-]+)`).FindAllStringSubmatch(page, -1)
	if len(links) == 0 {
		t.Fatal("the page links no hashed asset, so it has no stylesheet")
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
// pageFixtures is one plausible render for every page in pages. The tests
// below range over the real slice rather than a hand-written copy of it,
// because a copy is a list a new page silently falls out of — which is
// exactly what happened to both of them before analysis.html was added. A
// page with no fixture here fails rather than being skipped.
func pageFixtures(t *testing.T) map[string]any {
	t.Helper()
	fixtures := map[string]any{
		"index.html":    pageData{},
		"fight.html":    fullFightPage(),
		"analysis.html": analysisPage(),
		"error.html":    errorPageData{Status: 404, Message: "no"},
	}
	for _, page := range pages {
		if _, ok := fixtures[page]; !ok {
			t.Fatalf("no fixture for %s: every page in pages needs one, or the page-wide tests do not cover it", page)
		}
	}
	return fixtures
}

// The layout defaults the body class to "fight", so a page that forgets to
// define "page" is styled as the fight page and looks almost right — which is
// the worst kind of wrong.
func TestEachPageSetsItsBodyClass(t *testing.T) {
	for page, data := range pageFixtures(t) {
		class := strings.TrimSuffix(page, ".html")
		if !strings.Contains(render(t, page, data), `<body class="`+class+`">`) {
			t.Errorf("%s does not set body.%s", page, class)
		}
	}
}

// Warcraft Logs rates the pull; the page shows the bracket percentile, which
// compares the player only with parses at their item level. A player it did
// not rate shows nothing at all — a confident "0th percentile" would be a lie,
// and it is what a naive decode of an absent payload produces.
func TestFightPageShowsTheRankingAndOmitsItWhenThereIsNone(t *testing.T) {
	page := render(t, "fight.html", fullFightPage())
	for _, want := range []string{
		"61st",       // the bracket percentile, as an ordinal
		"Percentile", // the tile's label
		"Better than 61% of ranked Fire Mage parses", // the tip says which way is good
		"out of 440 of them",                         // what the percentile is measured against
		"better than 55%",                            // the all-parses figure
		`class="stat tipped" tabindex="0"`,           // reachable by keyboard, not hover alone
	} {
		if !strings.Contains(page, want) {
			t.Errorf("ranked page is missing %q", want)
		}
	}

	data := fullFightPage()
	unranked := *data.Player
	unranked.Ranking = warcraftlogs.Ranking{}
	data.Player = &unranked
	page = render(t, "fight.html", data)
	if strings.Contains(page, "Percentile") {
		t.Error("an unranked player still gets a percentile tile; an absent ranking must show nothing")
	}
	if strings.Contains(page, "0th") {
		t.Error("an unranked player renders as 0th percentile, which is a lie about their parse")
	}
}

// The analysis page is the coaching view of a pull: the same header and stat
// tiles as the timeline page, and none of its weight. It draws no timeline,
// so it must load neither timeline.js nor the Wowhead tooltip script — the
// one piece of third-party code this app executes, and the thing #7's
// Content-Security-Policy has to make room for.
func TestAnalysisPageDrawsNoTimelineAndLoadsNoScript(t *testing.T) {
	page := render(t, "analysis.html", analysisPage())

	for _, unwanted := range []string{"timeline.js", "zamimg.com"} {
		if strings.Contains(page, unwanted) {
			t.Errorf("the analysis page carries %q; it draws no timeline", unwanted)
		}
	}
	// It has a script of its own — the poller — but it must be a hashed
	// asset, never inline, because #7's policy will allow no inline script.
	if !strings.Contains(page, "analysis.js") {
		t.Error("the analysis page does not load its poller")
	}
	if regexp.MustCompile(`<script[^>]*>[^<\s]`).MatchString(page) {
		t.Error("the analysis page carries an inline script")
	}
	for _, want := range []string{
		"Testmage",             // the player, from the shared partial
		"Percentile",           // the ranking tile came with it
		"Nothing analysed yet", // and the page offers to do the work rather than pretending it has
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the analysis page is missing %q", want)
		}
	}
	if strings.Contains(page, "ZgotmplZ") {
		t.Error("page contains ZgotmplZ, so a value was refused by the contextual escaper")
	}
	if strings.Contains(page, "<no value>") {
		t.Error("page contains <no value>, so a field the template asked for does not exist")
	}
}

// Both views of a pull carry the strip, each marks itself, and both links
// keep the selected player — losing them on every move between the two views
// is the whole reason this is a link and not a form.
func TestTheViewStripMarksTheCurrentViewAndKeepsThePlayer(t *testing.T) {
	cases := map[string]struct{ page, current, other string }{
		"fight.html":    {"fight.html", "/report/ExampleReport123/fight/12?player=7", "/report/ExampleReport123/fight/12/analysis?player=7"},
		"analysis.html": {"analysis.html", "/report/ExampleReport123/fight/12/analysis?player=7", "/report/ExampleReport123/fight/12?player=7"},
	}
	data := map[string]any{"fight.html": fullFightPage(), "analysis.html": analysisPage()}
	for name, c := range cases {
		page := render(t, c.page, data[name])
		if !strings.Contains(page, `href="`+c.current+`"`) {
			t.Errorf("%s: the strip does not link this view (%s)", name, c.current)
		}
		if !strings.Contains(page, c.other) {
			t.Errorf("%s: the strip does not link the other view (%s)", name, c.other)
		}
		if n := strings.Count(page, `aria-current="page"`); n != 1 {
			t.Errorf("%s: %d links marked current, want exactly 1", name, n)
		}
	}

	// With nobody selected the links must not carry a dangling ?player=.
	noPlayer := analysisPage()
	noPlayer.Player, noPlayer.SelectedID = nil, 0
	if page := render(t, "analysis.html", noPlayer); strings.Contains(page, "?player=") {
		t.Error("the strip carries ?player= with no player selected")
	}
}

// With nobody picked the analysis page is an invitation, not an empty
// findings box about no one in particular. It shares playerstats.html's
// {{else}} branch with the fight page so the two cannot word it differently.
func TestAnalysisPageWithNoPlayerInvitesRatherThanReportsNothing(t *testing.T) {
	data := analysisPage()
	data.Player, data.SelectedID = nil, 0
	page := render(t, "analysis.html", data)

	if !strings.Contains(page, "Pick a player") {
		t.Error("the analysis page does not invite a player to be picked")
	}
	if strings.Contains(page, "Nothing analysed yet") {
		t.Error("the analysis page offers to analyse nobody; the box needs a player")
	}
	// Still a whole page: the tab strip is how you get back.
	if !strings.Contains(page, "viewtabs") {
		t.Error("the analysis page lost its view strip when no player was selected")
	}
}

// A finding is a claim, the moment it points at, and the arithmetic behind
// it. The timestamp is a link into the timeline at that moment, and the
// form of that link is a contract with timeline.js, which parses it with
// /^#t=(\d+)$/ — so this test is what keeps the two from drifting apart.
func TestFindingsRenderWithLinksTheTimelineScriptCanParse(t *testing.T) {
	page := render(t, "analysis.html", analysisWithFindings())

	for _, want := range []string{
		"First Combustion came late",        // the claim
		"Combustion was first used 38s",     // the sentence with its numbers
		`class="finding major"`,             // severity as a class, for the stripe
		`class="finding minor"`,             // and both severities present
		"first use",                         // the evidence label
		"Each timestamp opens the timeline", // how to use it
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the findings list is missing %q", want)
		}
	}
	if strings.Contains(page, "Nothing to flag") {
		t.Error("the page shows the empty state while carrying findings")
	}

	// Every timestamp link must be one timeline.js will act on, and must
	// carry the player so the timeline opens on the right one.
	links := regexp.MustCompile(`href="([^"]*#t=[^"]*)"`).FindAllStringSubmatch(page, -1)
	if len(links) != 2 {
		t.Fatalf("got %d timestamp links, want one per finding: %v", len(links), links)
	}
	parses := regexp.MustCompile(`^#t=\d+$`)
	for _, l := range links {
		href := l[1]
		hash := href[strings.Index(href, "#"):]
		if !parses.MatchString(hash) {
			t.Errorf("href %q ends in %q, which timeline.js's /^#t=(\\d+)$/ will not match", href, hash)
		}
		if !strings.Contains(href, "player=7") {
			t.Errorf("href %q does not carry the player, so the timeline opens on nobody", href)
		}
		if strings.Contains(href, "/analysis") {
			t.Errorf("href %q points at the analysis page; a finding links into the timeline", href)
		}
	}
}

// A finding the model set aside is still on the page, below the rest, with
// its reason and its arithmetic. Hiding it would leave the player with a
// judgement they cannot check — and the judgement is the part most worth
// checking, because it is the only thing on the page a rule did not decide.
func TestASetAsideFindingKeepsItsEvidenceAndItsReason(t *testing.T) {
	page := render(t, "analysis.html", analysisWithSetAside())

	for _, want := range []string{
		"Set aside",
		"held for the intermission",         // the reason
		`class="finding minor aside"`,       // set back, not removed
		"Combustion drifted later each use", // the claim it was made about
		"uses",                              // its evidence label
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the set-aside section is missing %q", want)
		}
	}
	// The one that was not set aside stays in the main list.
	if !strings.Contains(page, "What the log says") {
		t.Error("the standing findings lost their heading")
	}
	if i, j := strings.Index(page, "What the log says"), strings.Index(page, "Set aside"); i > j {
		t.Error("the set-aside findings are drawn above the ones the player should act on")
	}
}

// Every finding set aside leaves the main list empty, and the page must not
// then read as though the rules were silent — they were not; they were
// answered.
func TestAllFindingsSetAsideStillShowsThem(t *testing.T) {
	page := analysisWithSetAside()
	page.Findings[0].SetAside = true
	page.Findings[0].Why = "You were dead from 4:10, so it was never yours to press."

	out := render(t, "analysis.html", page)
	if strings.Contains(out, "Nothing to flag") {
		t.Error("the page claims nothing came up, while carrying two findings that did")
	}
	if !strings.Contains(out, "Set aside") {
		t.Error("the findings vanished entirely")
	}
}

// A pause is only meaningful against the global cooldown it was measured
// against, so the page prints that cooldown next to every one of them and says
// where the estimate came from. This is what replaces the threshold slider:
// the slider let a sceptical reader probe the model by moving it, and what is
// left instead is the model showing its own working.
func TestThePauseLaneShowsTheModelItRestsOn(t *testing.T) {
	page := render(t, "fight.html", fullFightPage())

	for _, want := range []string{
		`class="pause"`,    // the lane
		`class="pauserow"`, // and the same pause in the cast list
		"1.1s",             // the global cooldown it was measured against
		"88",               // and how many cast bars said so
		"Global cooldown",  // the stat
		"to the end of the pull",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page does not show %q, so a reader cannot check the model", want)
		}
	}
	// The slider and its disclaimer are gone, and so is the claim they made.
	for _, gone := range []string{`id="idleMs"`, "Idle still includes the GCD", "Threshold under"} {
		if strings.Contains(page, gone) {
			t.Errorf("the page still carries %q from the threshold slider", gone)
		}
	}
}

// "Nothing could be measured" and "you were never idle" are different answers
// and the page must not render the second when it means the first. A spec
// nobody has authored has no base cast times, so it has no global cooldown,
// so a pauses lane drawn for it would be fiction — measured, 116 pauses
// totalling 271 s of a 431 s fight.
func TestAnUnmodelledPullSaysSoRatherThanDrawingAnEmptyLane(t *testing.T) {
	page := render(t, "fight.html", pageWith(unmodelledTimeline()))

	if strings.Contains(page, `class="pause"`) || strings.Contains(page, `class="pauserow"`) {
		t.Error("pauses are drawn for a pull whose global cooldown could not be modelled")
	}
	for _, want := range []string{"Pauses are not shown for this pull", "measured from your own cast bars"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page does not say why there are no pauses; it lacks %q", want)
		}
	}
	// And the rest of the timeline still renders — an unmodelled global
	// cooldown costs one lane, not the page.
	for _, want := range []string{"Cast timeline", `class="castbar`, `class="phase`} {
		if !strings.Contains(page, want) {
			t.Errorf("the timeline lost %q along with its pauses", want)
		}
	}
}

// The largest unexplained band on a wipe is the player being dead. The page
// says so, in the lane's tooltip and in the cast table, naming the moment —
// otherwise it reports a corpse as idle and reads as a reproach.
func TestAPauseThePlayerWasDeadForIsLabelledOnThePage(t *testing.T) {
	page := render(t, "fight.html", pageWhereThePlayerDied())

	if !strings.Contains(page, "dead from 4:50") {
		t.Error("the page does not say the trailing pause was the player being dead")
	}
	// The pause before the death keeps its own explanation, or none.
	if strings.Count(page, "dead from") > 2 {
		t.Errorf("every pause is blamed on the death: %d mentions", strings.Count(page, "dead from"))
	}
	// And a pull with nobody dead says nothing about it.
	if alive := render(t, "fight.html", fullFightPage()); strings.Contains(alive, "dead from") {
		t.Error("a pull with no death still reports one")
	}
}
