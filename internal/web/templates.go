package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// pages are the templates a handler can render. Each is its own template
// set: the shared layout and partials, cloned, plus the page's own file
// defining "title" and "content". Cloning is what lets two pages both
// define "content" — one set could hold only one.
var pages = []string{"index.html", "fight.html", "error.html"}

// partials are the files every page set starts from.
var partials = []string{"layout.html", "track.html", "casttable.html", "playerstats.html"}

// Templates is the parsed page set.
type Templates struct {
	pages  map[string]*template.Template
	assets assets
}

// ParseTemplates parses the embedded templates into one set per page. It
// returns an error rather than panicking: a broken template must fail the
// process at startup and the test suite before that, not the first request.
func ParseTemplates() (*Templates, error) {
	assets, err := hashAssets(staticFS)
	if err != nil {
		return nil, err
	}
	funcs := templateFuncs()
	funcs["asset"] = assets.url
	base := template.New("").Funcs(funcs)
	for _, name := range partials {
		if _, err := base.ParseFS(templateFS, "templates/"+name); err != nil {
			return nil, err
		}
	}
	t := &Templates{pages: map[string]*template.Template{}, assets: assets}
	for _, name := range pages {
		set, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := set.ParseFS(templateFS, "templates/"+name); err != nil {
			return nil, err
		}
		if set.Lookup("content") == nil {
			return nil, fmt.Errorf("templates/%s defines no content", name)
		}
		t.pages[name] = set
	}
	return t, nil
}

// Execute renders one page — its content inside the layout — into w.
func (t *Templates) Execute(w io.Writer, page string, data any) error {
	set, ok := t.pages[page]
	if !ok {
		return fmt.Errorf("no such page: %s", page)
	}
	return set.ExecuteTemplate(w, "layout", data)
}

// Lookup reports whether a page is known, for the tests that check every
// page parsed.
func (t *Templates) Lookup(page string) bool { return t.pages[page] != nil }

// assets maps each static file to the path it is served at, which carries
// a hash of its content: /static/<hash>/<name>. A changed file is a new
// path, so the old one can be cached forever and the new one is never
// served stale.
type assets map[string]string

func hashAssets(files fs.FS) (assets, error) {
	a := assets{}
	entries, err := fs.ReadDir(files, "static")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		data, err := fs.ReadFile(files, "static/"+e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		a[e.Name()] = "/static/" + hex.EncodeToString(sum[:6]) + "/" + e.Name()
	}
	return a, nil
}

// url is the template func: {{asset "app.css"}}. An unknown asset is a
// template execution error, which the parse-and-render tests catch.
func (a assets) url(name string) (string, error) {
	u, ok := a[name]
	if !ok {
		return "", fmt.Errorf("no static asset named %q", name)
	}
	return u, nil
}

// serve is the handler for /static/{hash}/{name}. The hash in the path is
// checked against the current one: a stale link is a 404 rather than a
// forever-cached wrong file.
func (a assets) serve(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if a[name] != "/static/"+r.PathValue("hash")+"/"+name {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(staticFS, "static/"+name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType(name))
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, name, time.Time{}, strings.NewReader(string(data)))
}

func contentType(name string) string {
	switch path.Ext(name) {
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	}
	return "application/octet-stream"
}
