package main

// HTTP handlers and the small helpers they share. The server definition,
// routes and start-up are in server.go.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/CHESSComputing/gonexus/gonexus"
)

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *server) handleFiles(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	files, err := s.store.List(r.Context(), prefix)
	if err != nil {
		if ae, ok := err.(*apiError); ok {
			writeErr(w, ae) // keeps its status, e.g. 403 for a directory that may not be listed
		} else {
			writeErr(w, internal("%v", err))
		}
		return
	}
	if files == nil {
		files = []FileInfo{}
	}
	writeJSON(w, 200, map[string]interface{}{
		"mode":   s.mode(),
		"bucket": s.cfg.Bucket,
		"prefix": effectivePrefix(s.cfg.Prefix, prefix),
		"count":  len(files),
		"files":  files,
	})
}

func effectivePrefix(def, override string) string {
	if override != "" {
		return override
	}
	return def
}

func (s *server) handleTree(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	root, release, err := s.load(r, key)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer release()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(root.Tree() + "\n"))
}

func (s *server) handleEntries(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	root, release, err := s.load(r, key)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer release()
	writeJSON(w, 200, buildEntries(key, root))
}

func (s *server) handleGroup(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	path := r.URL.Query().Get("path")
	root, release, err := s.load(r, key)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer release()
	resp, err := buildGroup(key, path, root)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, resp)
}

func (s *server) handleField(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	path := r.URL.Query().Get("path")
	root, release, err := s.load(r, key)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer release()
	offset := queryInt(r, "offset", 0)
	limit := queryInt(r, "limit", s.cfg.DefaultFieldLimit)
	if limit <= 0 || limit > s.cfg.MaxFieldLimit {
		limit = s.cfg.MaxFieldLimit
	}
	resp, err := buildField(key, path, root, offset, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSONCompact(w, 200, resp)
}

// load ensures the S3 object for key is cached locally and returns its
// parsed gonexus tree, loading it if this is the first time it is needed
// (or if it changed in S3 since last time). The caller must call the
// returned release function when it has finished using the tree.
func (s *server) load(r *http.Request, key string) (*gonexus.NXgroup, func(), error) {
	if key == "" {
		return nil, nil, badRequest("missing required query parameter 'key'")
	}
	localPath, err := s.store.Ensure(r.Context(), key)
	if err != nil {
		if ae, ok := err.(*apiError); ok {
			return nil, nil, ae
		}
		return nil, nil, internal("%v", err)
	}
	return s.trees.acquire(localPath)
}

func queryInt(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// writeJSONCompact is writeJSON without indentation. /field responses can
// hold up to field_max_limit numbers, and pretty-printing would roughly
// triple their size on the wire.
func writeJSONCompact(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	status := 500
	msg := err.Error()
	if ae, ok := err.(*apiError); ok {
		status = ae.status
		msg = ae.msg
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

// handleView opens the web UI on a file:
//
//	GET /view?file=<path or key>[&path=<group or field>]
//
// It redirects to the UI (/ui/#key=...&path=...), which then shows the file's
// tree and data. In filesystem mode file is the file's absolute path, such as
// /nfs/chess/x/scan.nxs (a file:// URL is accepted too). Otherwise it is the
// file's key as listed by /files; with a local data_dir an absolute path below
// that directory is accepted as well and turned into the key. The optional
// path selects a group or field to open. Nothing is read here: the file is
// validated, and access decided, when the UI requests its data.
func (s *server) handleView(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	file := strings.TrimSpace(q.Get("file"))
	if file == "" {
		http.Redirect(w, r, "/ui/", http.StatusFound)
		return
	}
	v := url.Values{}
	v.Set("key", s.viewKey(file))
	if p := strings.Trim(strings.TrimSpace(q.Get("path")), "/"); p != "" {
		v.Set("path", p)
	}
	http.Redirect(w, r, "/ui/#"+v.Encode(), http.StatusFound)
}

// viewKey turns the file parameter of /view into a file key. In filesystem
// mode that is the absolute path. Otherwise a key is returned without its
// leading slash. With a local data_dir, an absolute path
// below that directory becomes its path relative to it (which is the key).
// Nothing is read here: the key is validated by the store when the UI uses it.
func (s *server) viewKey(file string) string {
	if s.cfg.FileSystemMode {
		// The key is the absolute path itself. It is passed on unchanged,
		// so that a ".." is rejected by the store instead of being rewritten.
		return strings.TrimPrefix(file, "file://")
	}
	file = filepath.ToSlash(file)
	if s.cfg.DataDir != "" && strings.HasPrefix(file, "/") {
		if root, err := filepath.Abs(s.cfg.DataDir); err == nil {
			rel, err := filepath.Rel(root, filepath.Clean(file))
			if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../") {
				return filepath.ToSlash(rel)
			}
		}
	}
	return strings.TrimLeft(file, "/")
}
