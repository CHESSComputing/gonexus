package main

import (
	"embed"
	"io/fs"
	"net/http"
)

// The web UI is plain static HTML/CSS/JS (no build step, no CDN) embedded
// into the binary, so the server stays a single self-contained executable.
//
//go:embed ui
var uiFS embed.FS

// registerUI mounts the browser UI at /ui/ and redirects / to it.
// It only adds routes; the JSON/text API endpoints are untouched.
func registerUI(mux *http.ServeMux) {
	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		panic(err) // cannot happen: "ui" is embedded above
	}
	mux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.FS(sub))))
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})
}
