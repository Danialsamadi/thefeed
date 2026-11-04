// Package web is the panel's UI: HTML, CSS and vanilla JS embedded in the
// binary, no build step, nothing loaded from other sites.
package web

import (
	"embed"
	"net/http"
	"strings"

	"github.com/sartoopjj/thefeed/internal/admin"
)

//go:embed static
var files embed.FS

// Module serves the shell and its assets before sign-in; the page itself
// asks the API which screen to show.
func Module(d *admin.Deps) error {
	d.HandlePublic("GET /{$}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only the panel's own files run or load. Inline style attributes stay
		// allowed for chart bar heights; inline scripts don't.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'")
		http.ServeFileFS(w, r, files, "static/index.html")
	}))
	// Files only: a directory would get the shell without its CSP, or a listing.
	assets := http.FileServerFS(files)
	d.HandlePublic("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		assets.ServeHTTP(w, r)
	}))
	return nil
}
