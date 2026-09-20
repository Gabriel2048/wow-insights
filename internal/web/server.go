// Package web is the HTTP layer: the routes, the handlers and the templates.
// It is composed by two binaries — the shipped one at the module root, over
// real credentials, and cmd/dev/serve-recorded, over a recording — and knows
// which it is running under only through the client it is handed.
package web

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

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

// Server holds the dependencies shared by the HTTP handlers.
type Server struct {
	wcl logsClient
	tpl *Templates
	log *slog.Logger
	// handlerDeadline is a field rather than the constant so a test can
	// shorten it; nothing else sets it.
	handlerDeadline time.Duration
}

// New wires a Server. wcl is whatever satisfies logsClient — in production
// a *warcraftlogs.Client over the real wire, offline the same type over a
// replay transport, in tests a fake. The logger decides the format: the
// shipped binary hands in JSON shaped for Cloud Logging, the dev binaries
// hand in text.
func New(wcl logsClient, tpl *Templates, logger *slog.Logger) *Server {
	return &Server{wcl: wcl, tpl: tpl, log: logger, handlerDeadline: handlerDeadline}
}

// routes returns the mux the server listens on. It is the concrete type so
// the access log can ask which pattern a path resolves to.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /report/{code}/fight/{id}", s.fight)
	mux.HandleFunc("GET /report/{code}/fight/{id}/analysis", s.analysis)
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
	// Findings are what the analysis can say the player could have done
	// differently. Empty is a real answer and the page says so: on a
	// competent pull most rules are silent.
	Findings []warcraftlogs.Finding
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

// analysis renders the coaching view of the same pull. It is a sibling of the
// fight page rather than a panel on it: the timeline page is already the
// largest thing this app serves, and the two are read one after the other
// rather than together.
//
// It fetches the timeline, which #54 said it would not. That was true while
// the page had nothing to say: it drew no timeline, so paying for the most
// expensive query the app makes would have been paying for nothing. The
// findings are computed from the player's casts, so the page now needs
// exactly the analysis it is named for. The cost of moving between the two
// views is what #58's cache exists to remove.
func (s *Server) analysis(w http.ResponseWriter, r *http.Request) {
	data, ok := s.pullPage(w, r, "analysis")
	if !ok {
		return
	}
	if data.Player != nil {
		know, _ := knowledge.Lookup(data.Player.SpecID())
		timeline, notices := s.playerTimeline(r, data.Detail, *data.Player, data.Fight)
		data.Notices = append(data.Notices, notices...)
		if timeline != nil {
			data.Findings = warcraftlogs.Findings(timeline.Timeline, know, data.Player.ActedUntil())
		}
	}
	s.render(w, r, http.StatusOK, "analysis.html", data)
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

// playerTimeline fetches and lays out one player's timeline, and says on the
// page whatever kept it from being whole. The stats are still worth showing
// without it, so nothing here fails the request: a timeline that could not
// be loaded is a sentence where it would have been, logged as a warning,
// rather than an empty area that looks like a player who cast nothing.
func (s *Server) playerTimeline(r *http.Request, detail *warcraftlogs.FightDetail, player warcraftlogs.PlayerStats, fight view.Fight) (*view.Timeline, []string) {
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
		s.logger(r).Info("player has no spec in the tables", "class", player.Class)
		notices = append(notices, "This player did no damage or healing in this pull, so their specialisation is unknown: procs and personal cooldowns are not shown.")
	case !known:
		s.logger(r).Info("no knowledge for spec", "class", player.Class, "spec", player.Spec)
		notices = append(notices, "No rotation knowledge for "+player.Title()+" yet: procs and personal cooldowns are not shown.")
	}

	timeline, err := s.wcl.Timeline(r.Context(), detail.ReportCode, detail.Fight, player.ActorID, know)
	if err != nil {
		p := classify(err)
		if p.status != 0 {
			p.level = min(p.level, slog.LevelWarn)
		}
		s.logProblem(r, p, "fetch timeline", err, "code", detail.ReportCode, "fight", detail.Fight.ID, "player", player.ActorID)
		return nil, append(notices, "The cast timeline could not be loaded. "+p.message)
	}
	if len(timeline.Incomplete) > 0 {
		s.logger(r).Warn("timeline arrived incomplete", "missing", timeline.Incomplete)
		notices = append(notices, "Part of the timeline was unavailable from Warcraft Logs: "+laneNames(timeline.Incomplete)+".")
	}
	if len(timeline.Truncated) > 0 {
		s.logger(r).Warn("timeline stream cut short", "streams", timeline.Truncated)
		notices = append(notices, "This pull had more events than one page holds; the following lanes end early: "+laneNames(timeline.Truncated)+".")
	}
	return view.Layout(timeline, view.Options{WowheadDifficulty: fight.WowheadDifficulty()}), notices
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
