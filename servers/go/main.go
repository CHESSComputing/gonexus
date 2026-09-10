// Command nexus-s3-server is a small HTTP server that exposes NeXus
// (HDF5) files stored in an S3 bucket for browsing over plain REST/JSON,
// using the gonexus library to read the file's tree structure and data.
//
// Since HDF5 files must be read from a real filesystem path, each object
// is transparently downloaded to a local disk cache (keyed by its S3
// ETag) on first request and re-used afterwards.
//
// Endpoints:
//
//	GET /healthz
//	GET /files                                  list available NeXus files
//	GET /tree?key=...                           full tree, text/plain
//	GET /entries?key=...                        top-level NXentry groups
//	GET /group?key=...&path=...                 a group's direct children
//	GET /field?key=...&path=...&offset=&limit=  a field's metadata + data
//
// See ../README.md for configuration (all via environment variables) and
// full endpoint documentation.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/CHESSComputing/gonexus/gonexus"
)

// treeCache avoids re-parsing the same (large) HDF5 file on every request
// by keeping the most recently loaded gonexus tree per local cache path in
// memory. It is intentionally tiny and unbounded-by-count-only; NeXus
// scan files are typically opened repeatedly in bursts (browsing one
// dataset) rather than in huge rotating sets.
type treeCache struct {
	mu    sync.Mutex
	trees map[string]*gonexus.NXgroup
}

func newTreeCache() *treeCache {
	return &treeCache{trees: make(map[string]*gonexus.NXgroup)}
}

func (c *treeCache) get(localPath string) (*gonexus.NXgroup, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if root, ok := c.trees[localPath]; ok {
		return root, nil
	}
	root, err := gonexus.Load(localPath)
	if err != nil {
		return nil, internal("reading nexus file %s: %v", localPath, err)
	}
	c.trees[localPath] = root
	return root, nil
}

// invalidate drops a cached tree, e.g. after Store.Ensure re-downloads a
// changed object.
func (c *treeCache) invalidate(localPath string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.trees, localPath)
}

type server struct {
	cfg   *Config
	store *Store
	trees *treeCache
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	ctx := context.Background()
	store, err := newStore(ctx, cfg)
	if err != nil {
		log.Fatalf("s3 client error: %v", err)
	}

	srv := &server{cfg: cfg, store: store, trees: newTreeCache()}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", srv.handleHealthz)
	mux.HandleFunc("/files", srv.handleFiles)
	mux.HandleFunc("/tree", srv.handleTree)
	mux.HandleFunc("/entries", srv.handleEntries)
	mux.HandleFunc("/group", srv.handleGroup)
	mux.HandleFunc("/field", srv.handleField)

	httpSrv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      logMiddleware(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	go func() {
		log.Printf("nexus-s3-server listening on %s (bucket=%s prefix=%q cache=%s)",
			cfg.Addr, cfg.Bucket, cfg.Prefix, cfg.CacheDir)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Print("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.String(), time.Since(start))
	})
}

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *server) handleFiles(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	files, err := s.store.List(r.Context(), prefix)
	if err != nil {
		writeErr(w, internal("%v", err))
		return
	}
	if files == nil {
		files = []FileInfo{}
	}
	writeJSON(w, 200, map[string]interface{}{
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
	root, _, err := s.load(r, key)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(root.Tree() + "\n"))
}

func (s *server) handleEntries(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	root, _, err := s.load(r, key)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, buildEntries(key, root))
}

func (s *server) handleGroup(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	path := r.URL.Query().Get("path")
	root, _, err := s.load(r, key)
	if err != nil {
		writeErr(w, err)
		return
	}
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
	root, _, err := s.load(r, key)
	if err != nil {
		writeErr(w, err)
		return
	}
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
	writeJSON(w, 200, resp)
}

// load ensures the S3 object for key is cached locally and returns its
// parsed gonexus tree, downloading/parsing it if this is the first time
// it's been requested (or if it changed in S3 since last time).
func (s *server) load(r *http.Request, key string) (*gonexus.NXgroup, string, error) {
	if key == "" {
		return nil, "", badRequest("missing required query parameter 'key'")
	}
	localPath, err := s.store.Ensure(r.Context(), key)
	if err != nil {
		if ae, ok := err.(*apiError); ok {
			return nil, "", ae
		}
		return nil, "", internal("%v", err)
	}
	root, err := s.trees.get(localPath)
	if err != nil {
		return nil, "", err
	}
	return root, localPath, nil
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

func writeErr(w http.ResponseWriter, err error) {
	status := 500
	msg := err.Error()
	if ae, ok := err.(*apiError); ok {
		status = ae.status
		msg = ae.msg
	}
	writeJSON(w, status, map[string]string{"error": msg})
}
