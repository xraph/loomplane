// Package web is the Forge extension that serves Loomplane's dashboard: the
// React app built from apps/dashboard, served by the same Forge process as
// the API so the browser talks to one origin.
//
// It's a catch-all below every other route. A path that names a file in the
// build serves it; any other path serves index.html so the dashboard's
// client-side routes survive a reload. Paths under the API's prefixes never
// fall back to the dashboard, so a mistyped API URL is a JSON 404, not a
// page of HTML.
package web

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/xraph/forge"
)

// Name is the extension's name.
const Name = "loomplane-web"

const notBuilt = `<!doctype html><meta charset="utf-8"><title>Loomplane</title>
<body style="font-family:system-ui;max-width:40rem;margin:4rem auto;line-height:1.5">
<h1>The dashboard isn't built yet</h1>
<p>Build it with <code>make dashboard</code> (or <code>pnpm --dir apps/dashboard build</code>) and reload,
or run <code>make dev-dashboard</code> for the development server.</p>
<p>The API is up: <a href="/openapi.json">/openapi.json</a>.</p></body>`

// Extension serves the dashboard.
type Extension struct {
	*forge.BaseExtension
	dir         string
	apiPrefixes []string
}

// New serves the dashboard built into dir. apiPrefixes are paths that
// belong to the API (for example /api/ and /authsome/) and never fall back
// to the dashboard.
func New(dir string, apiPrefixes ...string) *Extension {
	return &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "The Loomplane dashboard"),
		dir:           dir,
		apiPrefixes:   apiPrefixes,
	}
}

// Register mounts the catch-all.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	return fapp.Router().Handle("/", e.Handler())
}

// Start reports whether there's a dashboard to serve.
func (e *Extension) Start(context.Context) error {
	if _, err := os.Stat(filepath.Join(e.dir, "index.html")); err != nil {
		e.Logger().Warn("no built dashboard to serve; the API runs without it", forge.F("dir", e.dir))
	}
	e.MarkStarted()
	return nil
}

// Handler is the dashboard's http.Handler.
func (e *Extension) Handler() http.Handler {
	files := os.DirFS(e.dir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, p := range e.apiPrefixes {
			if strings.HasPrefix(r.URL.Path, p) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"not found"}`)) //nolint:errcheck // the client went away; nothing to do
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && isFile(files, name) {
			// Vite fingerprints everything under assets/, so it can be cached
			// for good; anything else is revalidated.
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			http.ServeFileFS(w, r, files, name)
			return
		}
		if !isFile(files, "index.html") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(notBuilt)) //nolint:errcheck // the client went away; nothing to do
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, files, "index.html")
	})
}

func isFile(files fs.FS, name string) bool {
	info, err := fs.Stat(files, name)
	return err == nil && !info.IsDir()
}
