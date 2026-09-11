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
	"log"
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
	log *log.Logger
}

// New wires a Server. wcl is whatever satisfies logsClient — in production
// a *warcraftlogs.Client over the real wire, offline the same type over a
// replay transport, in tests a fake.
func New(wcl logsClient, tpl *template.Template, logger *log.Logger) *Server {
	return &Server{wcl: wcl, tpl: tpl, log: logger}
}

// Routes returns the mux the server listens on. It returns the concrete type
// rather than http.Handler because a caller needs Handler() to ask which
// pattern a path resolves to.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /report/{code}/fight/{id}", s.fight)
	mux.HandleFunc("GET /healthz", healthz)
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
			s.log.Printf("fetch report %s: %v", code, err)
			data.Error = err.Error()
		} else {
			data.Report = report
		}
	}

	if err := s.tpl.ExecuteTemplate(w, "index.html", data); err != nil {
		s.log.Printf("render index: %v", err)
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
		s.log.Printf("warcraft logs health: %v", err)
		writeJSON(w, status, map[string]string{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "rateLimit": limit})
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
		s.log.Printf("fetch fight %s#%d: %v", code, fightID, err)
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
					// The stats above are still worth showing without it.
					s.log.Printf("fetch timeline %s#%d player %d: %v", code, fightID, id, err)
				} else {
					data.Timeline = timeline
				}
			}
		}
	}

	if err := s.tpl.ExecuteTemplate(w, "fight.html", data); err != nil {
		s.log.Printf("render fight: %v", err)
	}
}

// healthz is the probe target: it answers without touching anything, so it
// says "the process is up" and nothing more. /health/wcl is the one that
// spends a point to say whether the credentials work.
func healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode response: %v", err)
	}
}
