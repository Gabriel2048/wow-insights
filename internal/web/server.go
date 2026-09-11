// Package web is the HTTP layer: the routes, the handlers and the templates.
// It is composed by two binaries — the shipped one at the module root, over
// real credentials, and cmd/dev/serve-recorded, over a recording — and knows
// which it is running under only through the client it is handed.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"

	"wowinsight/internal/warcraftlogs"
)

//go:embed templates/*.html
var templateFS embed.FS

// ParseTemplates parses the embedded template set. It returns an error rather
// than panicking: the previous template.Must ran at package init, which go
// build never executes and no test could reach, so a broken template passed the
// whole gate and panicked on the first request after deploy.
func ParseTemplates() (*template.Template, error) {
	return template.New("").Funcs(templateFuncs()).ParseFS(templateFS, "templates/*.html")
}

// logsClient is the slice of the Warcraft Logs API the handlers actually use.
// It is declared here, in the consumer, so that package needs no interface of
// its own and no edit; *warcraftlogs.Client satisfies it structurally. It is
// also the seam a caching decorator hangs on.
type logsClient interface {
	Report(ctx context.Context, code string) (*warcraftlogs.Report, error)
	RateLimit(ctx context.Context) (warcraftlogs.RateLimit, error)
	FightDetail(ctx context.Context, code string, fightID int) (*warcraftlogs.FightDetail, error)
	Timeline(ctx context.Context, code string, fight warcraftlogs.Fight, sourceID int) (*warcraftlogs.Timeline, error)
}

// Server holds the dependencies shared by the HTTP handlers.
type Server struct {
	wcl logsClient
	tpl *template.Template
	log *slog.Logger
}

// New wires a Server. wcl is whatever satisfies logsClient — in production
// a *warcraftlogs.Client over the real wire, offline the same type over a
// replay transport, in tests a fake. The logger decides the format: the
// shipped binary hands in JSON shaped for Cloud Logging, the dev binaries
// hand in text.
func New(wcl logsClient, tpl *template.Template, logger *slog.Logger) *Server {
	return &Server{wcl: counted{wcl}, tpl: tpl, log: logger}
}

// upstreamFailed logs a failed call to Warcraft Logs at the right severity.
// A user who navigated away cancels the request context, and the client
// reports that as an error like any other — but it is not one, and logging
// it as one would drown the failures that matter in the ones that are not.
func (s *Server) upstreamFailed(r *http.Request, level slog.Level, msg string, err error, attrs ...any) {
	if errors.Is(err, context.Canceled) {
		s.logger(r).Info("client went away during "+msg, attrs...)
		return
	}
	s.logger(r).Log(r.Context(), level, msg, append(attrs, "err", err)...)
}

// Routes returns the mux the server listens on. It returns the concrete type
// rather than http.Handler because a caller needs Handler() to ask which
// pattern a path resolves to.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /report/{code}/fight/{id}", s.fight)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /health/wcl", s.wclHealth)
	return mux
}

// fightPageData is what the fight template renders. Player is nil until one is
// picked from the dropdown.
type fightPageData struct {
	Detail     *warcraftlogs.FightDetail
	Fight      warcraftlogs.Fight
	SelectedID int
	Player     *warcraftlogs.PlayerStats
	Timeline   *warcraftlogs.Timeline
}

// pageData is what the index template renders.
type pageData struct {
	Title  string
	URL    string
	Report *warcraftlogs.Report
	Error  string
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	data := pageData{Title: "wowinsight"}

	// The form submits back to "/" with the report URL in the query string, so
	// a bare visit renders an empty form and a submission renders the report.
	if data.URL = r.URL.Query().Get("url"); data.URL != "" {
		code, err := warcraftlogs.ParseReportCode(data.URL)
		if err != nil {
			data.Error = "That does not look like a Warcraft Logs report link. Expected something like https://www.warcraftlogs.com/reports/ExampleReport123"
		} else if report, err := s.wcl.Report(r.Context(), code); err != nil {
			s.upstreamFailed(r, slog.LevelError, "fetch report", err, "code", code)
			data.Error = err.Error()
		} else {
			data.Report = report
		}
	}

	if err := s.tpl.ExecuteTemplate(w, "index.html", data); err != nil {
		s.logger(r).Error("render index", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

// wclHealth confirms the Warcraft Logs credentials work by spending a single
// point on the cheapest query the API offers.
func (s *Server) wclHealth(w http.ResponseWriter, r *http.Request) {
	limit, err := s.wcl.RateLimit(r.Context())
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, warcraftlogs.ErrNoCredentials) {
			status = http.StatusServiceUnavailable
		}
		s.upstreamFailed(r, slog.LevelError, "warcraft logs health", err)
		s.writeJSON(w, r, status, map[string]string{"status": "error", "error": err.Error()})
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"status": "ok", "rateLimit": limit})
}

// fight renders one encounter and, when a player is selected, that player's
// numbers for it.
func (s *Server) fight(w http.ResponseWriter, r *http.Request) {
	code, err := warcraftlogs.ParseReportCode(r.PathValue("code"))
	if err != nil {
		http.Error(w, "invalid report code", http.StatusBadRequest)
		return
	}
	fightID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid fight id", http.StatusBadRequest)
		return
	}

	detail, err := s.wcl.FightDetail(r.Context(), code, fightID)
	if err != nil {
		s.upstreamFailed(r, slog.LevelError, "fetch fight", err, "code", code, "fight", fightID)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	data := fightPageData{Detail: detail, Fight: detail.Fight}
	if raw := r.URL.Query().Get("player"); raw != "" {
		if id, err := strconv.Atoi(raw); err == nil {
			if player, ok := detail.Player(id); ok {
				data.SelectedID = id
				data.Player = &player

				timeline, err := s.wcl.Timeline(r.Context(), code, detail.Fight, id)
				if err != nil {
					// The stats above are still worth showing without it, and
					// the page renders 200 — so this is a warning, not an error.
					s.upstreamFailed(r, slog.LevelWarn, "fetch timeline", err, "code", code, "fight", fightID, "player", id)
				} else {
					data.Timeline = timeline
				}
			}
		}
	}

	if err := s.tpl.ExecuteTemplate(w, "fight.html", data); err != nil {
		s.logger(r).Error("render fight", "err", err)
	}
}

// healthz is the probe target: it answers without touching anything, so it
// says "the process is up" and nothing more. /health/wcl is the one that
// spends a point to say whether the credentials work.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.logger(r).Error("encode response", "err", err)
	}
}
