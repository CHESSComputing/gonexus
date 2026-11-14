package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
)

// The web UI is plain static HTML/CSS/JS (no build step, no CDN) embedded
// into the binary, so the server stays a single self-contained executable.
//
//go:embed ui
var uiFS embed.FS

// registerUI mounts the browser UI at /ui/ and redirects / to it.
// It only adds routes; the JSON/text API endpoints are untouched.
//
// configFile, if not empty, is a JSON file served as /ui/config.json in
// place of the built-in one, so an operator can set site-wide look-and-feel
// defaults without rebuilding the server (see UI.md).
func registerUI(mux *http.ServeMux, configFile string) {
	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		panic(err) // cannot happen: "ui" is embedded above
	}

	if configFile != "" {
		if _, err := os.Stat(configFile); err != nil {
			log.Printf("warning: ui_config %s: %v (the built-in UI defaults are used until it exists)", configFile, err)
		}
		mux.HandleFunc("/ui/config.json", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, configFile)
		})
	}

	files := http.StripPrefix("/ui/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/ui/", func(w http.ResponseWriter, r *http.Request) {
		// Embedded files carry no modification time, so browsers would have
		// to guess how long to keep them; make them revalidate instead, so an
		// upgraded server never leaves an old app.js behind in the cache.
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})
}
