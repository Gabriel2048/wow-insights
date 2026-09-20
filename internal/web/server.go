// Package web is the HTTP layer: the routes, the handlers and the templates.
// It is composed by two binaries — the shipped one at the module root, over
// real credentials, and cmd/dev/serve-recorded, over a recording — and knows
// which it is running under only through the client it is handed.
package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"wowinsight/internal/coach"
	"wowinsight/internal/knowledge"
	"wowinsight/internal/view"
	"wowinsight/internal/warcraftlogs"
)

// logsClient is the slice of the Warcraft Logs API the handlers actually use.
// It is declared here, in the consumer, so that package needs no interface of
// its own and no edit; *warcraftlogs.Client satisfies it structurally. It is
// also the seam a caching decorator hangs on.
type logsClient interface {
	Report(ctx context.Context, code string) (*warcraftlogs.Report, error)
	FightDetail(ctx context.Context, code string, fightID int) (*warcraftlogs.FightDetail, error)
	Timeline(ctx context.Context, code string, fight warcraftlogs.Fight, sourceID int, know knowledge.Knowledge) (*warcraftlogs.Timeline, error)
}

// writer is the slice of the coaching model this package uses. Declared here,
// in the consumer, like logsClient above it: *coach.Writer satisfies it
// structurally and needs no interface of its own.
//
// It is optional in a way logsClient is not. A Server with no writer is the
// whole product minus the prose — every finding still appears, in the words
// the analyser chose — which is why this is a nil-able field and a functional
// option rather than an argument to New.
type writer interface {
	Write(ctx context.Context, in coach.Input) (coach.Findings, error)
}

// Server holds the dependencies shared by the HTTP handlers.
type Server struct {
	wcl logsClient
	tpl *Templates
	log *slog.Logger
	// jobs owns the analyses running in the background. It is built by New
	// so that its root context outlives every request.
	jobs *registry
	// handlerDeadline is a field rather than the constant so a test can
	// shorten it; nothing else sets it.
	handlerDeadline time.Duration
	// coach words the findings, when one is configured. Nil is the ordinary
	// state: no API key, the dev binaries, and every test that is not about
	// the prose.
	coach writer
}

// Option configures a Server. Same shape as the client's, for the same
// reason: Go has no optional parameters.
type Option func(*Server)

// WithWriter gives the Server a model to word the findings with. Without it
// the coaching page shows the analyser's own sentences, which is the default
// and is never wrong — only plainer.
func WithWriter(w writer) Option {
	return func(s *Server) {
		// A nil interface handed in explicitly would satisfy the field and
		// panic on the first call, which is a worse failure than the one it
		// is trying to configure away.
		if w != nil {
			s.coach = w
		}
	}
}

// New wires a Server. wcl is whatever satisfies logsClient — in production
// a *warcraftlogs.Client over the real wire, offline the same type over a
// replay transport, in tests a fake. The logger decides the format: the
// shipped binary hands in JSON shaped for Cloud Logging, the dev binaries
// hand in text.
func New(wcl logsClient, tpl *Templates, logger *slog.Logger, opts ...Option) *Server {
	s := &Server{wcl: wcl, tpl: tpl, log: logger, handlerDeadline: handlerDeadline}
	for _, opt := range opts {
		opt(s)
	}
	s.jobs = newRegistry(s.analyse)
	return s
}

// routes returns the mux the server listens on. It is the concrete type so
// the access log can ask which pattern a path resolves to.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /report/{code}/fight/{id}", s.fight)
	mux.HandleFunc("GET /report/{code}/fight/{id}/analysis", s.analysis)
	mux.HandleFunc("POST /report/{code}/fight/{id}/analysis", s.startAnalysis)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /static/{hash}/{name}", s.tpl.assets.serve)
	return mux
}

// maxURLParam bounds the report link a user submits. A real one is under a
// hundred characters; the parser and the page that echoes it back on failure
// should never see more than this.
const maxURLParam = 512

// fightPageData is what the fight template renders. Player is nil until one is
// picked from the dropdown.
type fightPageData struct {
	Detail     *warcraftlogs.FightDetail
	Fight      view.Fight
	SelectedID int
	Player     *warcraftlogs.PlayerStats
	// Timeline is the analysis laid out for this page. The handler chooses
	// the axis; today that is the pull's own.
	Timeline *view.Timeline
	// Analysis is what the coaching page shows about the background job for
	// this pull: whether one has been asked for, is running, or is done.
	Analysis analysisState
	// Findings are what the analysis can say the player could have done
	// differently, in whichever words were chosen for them. Empty is a real
	// answer and the page says so: on a competent pull most rules are silent.
	Findings coach.Findings
	// View is which of the two views of a pull this is, "timeline" or
	// "analysis". The tab strip is rendered by both pages from one partial
	// and needs to know which link to mark as current.
	View string
	// Notices are the things that went wrong without stopping the page: the
	// timeline could not be fetched, a player could not be resolved, part of
	// the document did not arrive. An empty area with nothing said is the
	// wrong default for a product whose whole value is the timeline.
	Notices []string
}

// pageData is what the index template renders.
type pageData struct {
	URL    string
	Report *warcraftlogs.Report
	Error  string
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	data := pageData{}

	// The form submits back to "/" with the report URL in the query string, so
	// a bare visit renders an empty form and a submission renders the report.
	// A failure here is the form's result, not an HTTP failure: the page is
	// still the page, with the sentence beside the field.
	if data.URL = r.URL.Query().Get("url"); data.URL != "" {
		if len(data.URL) > maxURLParam {
			data.URL = data.URL[:maxURLParam]
		}
		code, err := warcraftlogs.ParseReportCode(data.URL)
		if err != nil {
			data.Error = "That does not look like a Warcraft Logs report link. Expected something like https://www.warcraftlogs.com/reports/ExampleReport123"
		} else if report, err := s.wcl.Report(r.Context(), code); err != nil {
			p := classify(err)
			s.logProblem(r, p, "fetch report", err, "code", code)
			data.Error = p.message
		} else {
			data.Report = report
		}
	}
	s.render(w, r, http.StatusOK, "index.html", data)
}

// fight renders one encounter and, when a player is selected, that player's
// numbers for it.
func (s *Server) fight(w http.ResponseWriter, r *http.Request) {
	data, ok := s.pullPage(w, r, "timeline")
	if !ok {
		return
	}
	if data.Player != nil {
		timeline, notices := s.playerTimeline(r, data.Detail, *data.Player, data.Fight)
		// Appended, not assigned: the fight itself may already have put a
		// notice here, and the timeline's must not replace it.
		data.Timeline, data.Notices = timeline, append(data.Notices, notices...)
	}
	s.render(w, r, http.StatusOK, "fight.html", data)
}

// analysisState is what the coaching page knows about the work behind it.
// It is a string so the template can branch on it and the access log can say
// it; the zero value is "nothing has been asked for yet", which is the state
// a page is in the first time anyone opens it.
type analysisState string

const (
	analysisIdle    analysisState = ""
	analysisRunning analysisState = "running"
	analysisDone    analysisState = "done"
	analysisFailed  analysisState = "failed"
)

// analysis renders the coaching view of a pull. **It only ever renders.** It
// starts no work, spends no Warcraft Logs points beyond the fight it needs to
// draw the page, and costs no money — which is what makes it safe for a
// browser to poll it every couple of seconds and for a crawler to follow it.
// Starting work is the POST.
func (s *Server) analysis(w http.ResponseWriter, r *http.Request) {
	// The machine-readable form is answered from the registry alone. It must
	// not go anywhere near pullPage: that fetches the fight to draw the page,
	// which is seven points of a shared hourly budget, and a browser polling
	// this every second would spend the whole hour in a few minutes.
	if wantsJSON(r) {
		s.pollState(w, r)
		return
	}

	data, ok := s.pullPage(w, r, "analysis")
	if !ok {
		return
	}
	if data.Player != nil {
		if j, found := s.jobs.lookup(s.jobKeyFor(data)); found {
			data.Analysis = analysisRunning
			if j.finished() {
				data.Analysis, data.Findings = analysisDone, j.result.findings
				data.Notices = append(data.Notices, j.result.notices...)
				if j.err != nil {
					p := classify(j.err)
					data.Analysis = analysisFailed
					data.Notices = append(data.Notices, "The analysis did not finish. "+p.message)
				}
			}
		}
	}
	s.render(w, r, http.StatusOK, "analysis.html", data)
}

// pollState says where the work has got to, and costs nothing to ask.
func (s *Server) pollState(w http.ResponseWriter, r *http.Request) {
	state := analysisIdle
	if ref, ok := routeRef(r); ok {
		if j, found := s.jobs.poll(ref); found {
			state = analysisRunning
			if j.finished() {
				state = analysisDone
				if j.err != nil {
					state = analysisFailed
				}
			}
		}
	}
	s.writeJSON(w, r, http.StatusOK, map[string]string{"state": string(state)})
}

// routeRef reads the work's identity out of the URL, with the same
// validation the page itself applies, and without asking anyone anything.
func routeRef(r *http.Request) (actorRef, bool) {
	code, err := warcraftlogs.ParseReportCode(r.PathValue("code"))
	if err != nil {
		return actorRef{}, false
	}
	fightID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		return actorRef{}, false
	}
	actorID, err := strconv.Atoi(r.URL.Query().Get("player"))
	if err != nil {
		return actorRef{}, false
	}
	return actorRef{code: code, fightID: fightID, actorID: actorID}, true
}

// startAnalysis begins the work and sends the browser back to the page that
// shows it. It answers a POST because it spends an upstream budget and, one
// day, money: a GET that did this would be followed by every crawler and
// fired by every refresh.
//
// It redirects rather than rendering so that the result of the POST is a URL
// the browser can reload — the analysis page, which is where the answer will
// appear whether or not the visitor has JavaScript.
func (s *Server) startAnalysis(w http.ResponseWriter, r *http.Request) {
	data, ok := s.pullPage(w, r, "analysis")
	if !ok {
		return
	}
	if data.Player != nil {
		key := s.jobKeyFor(data)
		if _, started, err := s.jobs.start(key, newRequestID(), s.logger(r)); err != nil {
			p := classify(err)
			s.logProblem(r, p, "start analysis", err, "fight", data.Detail.Fight.ID)
			s.fail(w, r, p)
			return
		} else if started {
			s.logger(r).Info("analysis started", "fight", data.Detail.Fight.ID, "player", data.SelectedID)
		}
	}
	http.Redirect(w, r, r.URL.String(), http.StatusSeeOther)
}

// jobKeyFor names the work a page is about. Spec is part of it because the
// analysis is spec-shaped: two timelines of one actor under different tables
// are different answers.
func (s *Server) jobKeyFor(data fightPageData) jobKey {
	know, _ := knowledge.Lookup(data.Player.SpecID())
	return jobKey{
		subject: warcraftlogs.Subject{
			ReportCode: data.Detail.ReportCode,
			FightID:    data.Detail.Fight.ID,
			ActorID:    data.Player.ActorID,
			Spec:       data.Player.SpecID(),
		},
		knowledge: know.Version(),
	}
}

// wantsJSON reports whether the caller asked for the machine-readable form of
// a page. It is content negotiation rather than a second route because the
// two are the same resource: one URL, one cache key, one thing to reason
// about when #7 adds a Content-Security-Policy.
func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

// pullPage is everything the two views of a pull do identically: validate the
// route, fetch the fight, say what arrived incomplete, and resolve the player
// in the query string. It reports false when it has already answered the
// request, which is the only way a handler above it should end early.
//
// It exists because the two handlers were the same thirty lines twice, and
// the duplication was the kind that drifts: a 400 reworded on one page and
// not the other reads as a different app.
func (s *Server) pullPage(w http.ResponseWriter, r *http.Request, viewName string) (fightPageData, bool) {
	code, err := warcraftlogs.ParseReportCode(r.PathValue("code"))
	if err != nil {
		s.fail(w, r, problem{http.StatusBadRequest, "That is not a Warcraft Logs report code.", slog.LevelInfo})
		return fightPageData{}, false
	}
	fightID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, problem{http.StatusBadRequest, "That is not a fight number.", slog.LevelInfo})
		return fightPageData{}, false
	}

	detail, err := s.wcl.FightDetail(r.Context(), code, fightID)
	if err != nil {
		p := classify(err)
		s.logProblem(r, p, "fetch fight", err, "code", code, "fight", fightID)
		s.fail(w, r, p)
		return fightPageData{}, false
	}

	data := fightPageData{Detail: detail, Fight: view.Fight{Fight: detail.Fight}, View: viewName}
	if len(detail.Incomplete) > 0 {
		s.logger(r).Warn("fight arrived incomplete", "missing", detail.Incomplete)
		data.Notices = append(data.Notices, "Part of this fight was unavailable from Warcraft Logs: "+laneNames(detail.Incomplete)+".")
	}
	if raw := r.URL.Query().Get("player"); raw != "" {
		id, err := strconv.Atoi(raw)
		if err != nil {
			s.logger(r).Info("player is not a number", "player", raw)
			data.Notices = append(data.Notices, "The player in the link is not one this page knows; pick one above.")
		} else if player, ok := detail.Player(id); !ok {
			s.logger(r).Info("player is not in this fight", "player", id)
			data.Notices = append(data.Notices, "That player is not in this fight; pick one above.")
		} else {
			data.SelectedID = id
			data.Player = &player
		}
	}
	return data, true
}

// playerTimeline fetches and lays out one player's timeline for a request,
// and says on the page whatever kept it from being whole. The stats are still
// worth showing without it, so nothing here fails the request.
func (s *Server) playerTimeline(r *http.Request, detail *warcraftlogs.FightDetail, player warcraftlogs.PlayerStats, fight view.Fight) (*view.Timeline, []string) {
	timeline, notices := s.timelineFor(r.Context(), s.logger(r), detail, player)
	if timeline == nil {
		return nil, notices
	}
	// DiedAt is the one thing the timeline query does not fetch and the page
	// needs: the deaths table comes with the fight, so this is where a pause
	// and the death that explains it meet.
	return view.Layout(timeline, view.Options{
		WowheadDifficulty: fight.WowheadDifficulty(),
		DiedAt:            player.DiedAt,
	}), notices
}

// timelineFor is playerTimeline with no request in it: the analysis runs as a
// background job, long after the request that asked for it has been answered
// and its context cancelled, so the work cannot reach for either. It returns
// the domain timeline rather than a laid-out one — a view.Timeline embeds
// this and adds lanes, so storing one would keep both alive, and the job
// stores its result.
func (s *Server) timelineFor(ctx context.Context, log *slog.Logger, detail *warcraftlogs.FightDetail, player warcraftlogs.PlayerStats) (*warcraftlogs.Timeline, []string) {
	var notices []string

	// An unauthored spec still gets its timeline — casts, pauses, phases,
	// boss casts, lust and raid cooldowns are class-agnostic — with the
	// zero tables, which ask the API for no procs or cooldowns. The page
	// says so, or an empty cooldown lane would read as a flawless rotation.
	know, known := knowledge.Lookup(player.SpecID())
	switch {
	case player.Spec == "":
		// The spec comes from the damage and healing tables; a player in
		// neither — dead on the pull, or never engaged — has none, and "no
		// knowledge for Mage" would be false.
		log.Info("player has no spec in the tables", "class", player.Class)
		notices = append(notices, "This player did no damage or healing in this pull, so their specialisation is unknown: procs and personal cooldowns are not shown.")
	case !known:
		log.Info("no knowledge for spec", "class", player.Class, "spec", player.Spec)
		notices = append(notices, "No rotation knowledge for "+player.Title()+" yet: procs and personal cooldowns are not shown.")
	}

	timeline, err := s.wcl.Timeline(ctx, detail.ReportCode, detail.Fight, player.ActorID, know)
	if err != nil {
		p := classify(err)
		if p.status != 0 {
			p.level = min(p.level, slog.LevelWarn)
		}
		log.Log(ctx, p.level, "fetch timeline", "err", err, "code", detail.ReportCode, "fight", detail.Fight.ID, "player", player.ActorID)
		return nil, append(notices, "The cast timeline could not be loaded. "+p.message)
	}
	if len(timeline.Incomplete) > 0 {
		log.Warn("timeline arrived incomplete", "missing", timeline.Incomplete)
		notices = append(notices, "Part of the timeline was unavailable from Warcraft Logs: "+laneNames(timeline.Incomplete)+".")
	}
	if len(timeline.Truncated) > 0 {
		log.Warn("timeline stream cut short", "streams", timeline.Truncated)
		notices = append(notices, "This pull had more events than one page holds; the following lanes end early: "+laneNames(timeline.Truncated)+".")
	}
	return timeline, notices
}

// analyse is the background job: everything the coaching page needs, computed
// with no request in scope. It re-fetches the fight rather than being handed
// the one the page already has, because a job outlives the request that
// started it and must not hold a pointer into it — #58's cache is what will
// stop that being paid for twice.
func (s *Server) analyse(ctx context.Context, log *slog.Logger, key jobKey) (result, error) {
	detail, err := s.wcl.FightDetail(ctx, key.subject.ReportCode, key.subject.FightID)
	if err != nil {
		return result{}, err
	}
	player, ok := detail.Player(key.subject.ActorID)
	if !ok {
		return result{}, fmt.Errorf("web: actor %d is not in fight %d", key.subject.ActorID, key.subject.FightID)
	}
	know, _ := knowledge.Lookup(player.SpecID())

	timeline, notices := s.timelineFor(ctx, log, detail, player)
	out := result{notices: notices}
	if timeline == nil {
		return out, nil
	}
	found := warcraftlogs.Findings(timeline, know, player.ActedUntil())
	out.findings = coach.Deterministic(found)
	if s.coach == nil || len(found) == 0 {
		return out, nil
	}

	// From here on nothing may fail the job. The findings are computed and
	// true; the model is only being asked to word them, and a page in the
	// analyser's own sentences is the thing this degrades to rather than an
	// error anybody sees.
	written, err := s.coach.Write(ctx, coach.Input{
		Detail:   detail,
		Player:   player,
		Timeline: timeline,
		Know:     know,
		Findings: found,
	})
	out.findings = written
	if err != nil {
		p := classify(err)
		log.Log(ctx, p.level, "wording the findings", "err", err, "fight", key.subject.FightID)
		out.notices = append(out.notices, p.message)
	}
	return out, nil
}

// laneNames turns the query's field aliases, which is how the client names
// a gap, into what the page calls the lane. An alias with no name here is
// printed as is rather than dropped.
func laneNames(aliases []string) string {
	names := map[string]string{
		"casts": "casts", "lust": "bloodlust", "procs": "procs", "cooldowns": "your cooldowns",
		"raidCDs": "raid cooldowns", "bossCasts": "boss casts", "phases": "phases",
		"damage": "damage done", "taken": "damage taken", "npcs": "enemy names",
		"abilities": "ability names", "actors": "player names", "rankings": "rankings",
	}
	out := make([]string, len(aliases))
	for i, a := range aliases {
		if n, ok := names[a]; ok {
			out[i] = n
		} else {
			out[i] = a
		}
	}
	return strings.Join(out, ", ")
}

// healthz is the probe target: it answers without touching anything, so it
// says "the process is up" and nothing more. There is deliberately no
// endpoint that asks Warcraft Logs anything: as a readiness probe it would
// take the app out of service during an upstream outage, when the right
// behaviour is to stay up and say so, and as a monitor it is redundant with
// the 502 rate in the access log.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}
