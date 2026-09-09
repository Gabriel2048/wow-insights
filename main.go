package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"time"

	"wowinsight/internal/warcraftlogs"
)

//go:embed templates/*.html
var templateFS embed.FS

var templateFuncs = template.FuncMap{
	"duration": formatDuration,
	"compact":  formatCompact,
	"short":    formatShort,
	"datetime": func(t time.Time) string { return t.Format("2 Jan 2006, 15:04 MST") },
}

var templates = template.Must(
	template.New("").Funcs(templateFuncs).ParseFS(templateFS, "templates/*.html"),
)

// formatDuration renders a duration as "2h 14m" or "3m 42s", dropping the
// precision nobody reads.
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if h := int(d.Hours()); h > 0 {
		return fmt.Sprintf("%dh %02dm", h, int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// formatShort renders the small durations of a cast timeline: "2.4s" for
// anything under a minute, "1m 04s" above it.
func formatShort(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	d = d.Round(time.Second)
	return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// formatCompact abbreviates large numbers the way damage meters do: 12.3m
// rather than 12345678.
func formatCompact(v float64) string {
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%.2fb", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%.2fm", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.1fk", v/1e3)
	default:
		return fmt.Sprintf("%.0f", v)
	}
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

// server holds the dependencies shared by the HTTP handlers.
type server struct {
	wcl *warcraftlogs.Client
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	data := pageData{Title: "wowinsight"}

	// The form submits back to "/" with the report URL in the query string, so
	// a bare visit renders an empty form and a submission renders the report.
	if data.URL = r.URL.Query().Get("url"); data.URL != "" {
		code, err := warcraftlogs.ParseReportCode(data.URL)
		if err != nil {
			data.Error = "That does not look like a Warcraft Logs report link. Expected something like https://www.warcraftlogs.com/reports/ExampleReport123"
		} else if report, err := s.wcl.Report(r.Context(), code); err != nil {
			log.Printf("fetch report %s: %v", code, err)
			data.Error = err.Error()
		} else {
			data.Report = report
		}
	}

	if err := templates.ExecuteTemplate(w, "index.html", data); err != nil {
		log.Printf("render index: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

// wclHealth confirms the Warcraft Logs credentials work by spending a single
// point on the cheapest query the API offers.
func (s *server) wclHealth(w http.ResponseWriter, r *http.Request) {
	limit, err := s.wcl.RateLimit(r.Context())
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, warcraftlogs.ErrNoCredentials) {
			status = http.StatusServiceUnavailable
		}
		log.Printf("warcraft logs health: %v", err)
		writeJSON(w, status, map[string]string{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "rateLimit": limit})
}

// fight renders one encounter and, when a player is selected, that player's
// numbers for it.
func (s *server) fight(w http.ResponseWriter, r *http.Request) {
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
		log.Printf("fetch fight %s#%d: %v", code, fightID, err)
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
					log.Printf("fetch timeline %s#%d player %d: %v", code, fightID, id, err)
				} else {
					data.Timeline = timeline
				}
			}
		}
	}

	if err := templates.ExecuteTemplate(w, "fight.html", data); err != nil {
		log.Printf("render fight: %v", err)
	}
}

func hello(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"message": "Hello, World!"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode response: %v", err)
	}
}

func main() {
	if err := loadEnvFile(".env"); err != nil {
		log.Printf("load .env: %v", err)
	}

	s := &server{
		wcl: warcraftlogs.New(
			firstEnv("WARCRAFTLOGS_CLIENT_ID", "ClientId"),
			firstEnv("WARCRAFTLOGS_CLIENT_SECRET", "ClientSecret"),
		),
	}

	http.HandleFunc("GET /{$}", s.index)
	http.HandleFunc("GET /report/{code}/fight/{id}", s.fight)
	http.HandleFunc("GET /hello", hello)
	http.HandleFunc("GET /health/wcl", s.wclHealth)

	addr := ":8080"
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
