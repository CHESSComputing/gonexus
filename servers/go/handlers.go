package main

// HTTP handlers and the small helpers they share. The server definition,
// routes and start-up are in server.go.

import (
	"encoding/json"
	"errors"
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

// maxViewBody bounds the size of a /view request body.
const maxViewBody = 64 << 10

// handleView opens the web UI on a file for a caller who proved they may:
//
//	POST /view
//	Authorization: Bearer <token>           (or "token" in the body)
//	{"file": "<path or key>", "token": "...", ...}
//
// The body is decoded into the payload struct selected by view_payload
// (ViewPayload, FoxdenViewPayload or one you registered, see AUTH.md) and
// validated; only then is the caller redirected (303) to the UI, /ui/#key=...,
// which shows the file's tree and data. In filesystem mode file is the file's absolute path, such
// as /nfs/chess/x/scan.nxs (a file:// URL is accepted too). Otherwise it is
// the file's key as listed by /files; with a local data_dir an absolute path
// below that directory is accepted as well and turned into the key.
func (s *server) handleView(w http.ResponseWriter, r *http.Request) {
	s.serveView(w, r, false)
}

// handleViewForm is /view for other services: the same checks and redirect,
// but the payload comes from a web form (application/x-www-form-urlencoded or
// multipart/form-data) whose field names are the payload's JSON names, e.g.
//
//	curl -d file=/nfs/chess/x/scan.nxs -d token=$TOKEN http://host:8081/viewform
//
// The token is taken from the form only, not from a header. Form fields in the
// URL query are ignored, so a token cannot end up in a logged URL by accident.
func (s *server) handleViewForm(w http.ResponseWriter, r *http.Request) {
	s.serveView(w, r, true)
}

// serveView implements /view (JSON body) and /viewform (form body).
func (s *server) serveView(w http.ResponseWriter, r *http.Request, form bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeErr(w, &apiError{status: http.StatusMethodNotAllowed, msg: "use POST with a JSON body"})
		return
	}
	payload, err := s.authorizeView(w, r, form)
	if err != nil {
		var ae *apiError
		if !errors.As(err, &ae) {
			ae = forbidden("%v", err) // a plain error from a custom Validate
		}
		if ae.status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Bearer realm="gonexus"`)
		}
		writeErr(w, ae)
		return
	}

	// Validated: open the web UI on the file. 303 makes a browser follow with
	// a GET even though the request was a POST. Nothing is read here; the UI
	// requests the file's data and any problem with the file shows up there.
	key := s.viewKey(strings.TrimSpace(payload.GetFile()))
	if s.cfg.FileSystemMode {
		if s.sessions == nil {
			writeErr(w, internal("view sessions are not configured"))
			return
		}
		s.sessions.grant(w, r, key) // lets the UI read this file (session.go)
	}
	v := url.Values{}
	v.Set("key", key)
	http.Redirect(w, r, "/ui/#"+v.Encode(), http.StatusSeeOther)
}

// authorizeView reads the payload of a /view request, adds the token from the
// Authorization header and validates it. It returns the payload only if the
// caller may open the file it names.
func (s *server) authorizeView(w http.ResponseWriter, r *http.Request, form bool) (UserPayload, error) {
	if s.auth == nil {
		return nil, internal("access control is not configured")
	}
	payload, err := newPayload(s.cfg.ViewPayloadType)
	if err != nil {
		return nil, internal("%v", err)
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxViewBody)
	if form {
		if err := decodeForm(r, payload); err != nil {
			return nil, badRequest("invalid form payload: %v", err)
		}
	} else {
		if err := json.NewDecoder(r.Body).Decode(payload); err != nil {
			return nil, badRequest("invalid JSON payload: %v", err)
		}
		if tok := bearerToken(r); tok != "" {
			payload.SetToken(tok) // the header wins over a token in the body
		}
	}
	if strings.TrimSpace(payload.GetFile()) == "" {
		return nil, badRequest("missing required field 'file'")
	}
	if err := payload.Validate(r.Context(), s.auth); err != nil {
		return nil, err
	}
	return payload, nil
}

// decodeForm fills payload from the body fields of a form request. Field names
// are the payload's JSON names, so any registered payload works unchanged;
// fields are single strings, and unknown fields are ignored.
func decodeForm(r *http.Request, payload UserPayload) error {
	if err := r.ParseMultipartForm(maxViewBody); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		return err
	}
	fields := make(map[string]string, len(r.PostForm))
	for k, v := range r.PostForm { // PostForm: body only, never the URL query
		if len(v) > 0 {
			fields[k] = v[0]
		}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, payload)
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
