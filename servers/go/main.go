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
//	GET /ui/                                    interactive web UI (embedded)
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

// treeCache keeps one parsed gonexus tree per local cache path so that
// repeated requests for a file do not re-read it.
//
// With lazy loading off (the default) a tree holds all of the file's data
// in memory and is kept for the life of the process, as before. With lazy
// "auto" or "all" a tree holds metadata (plus small fields) and keeps the
// HDF5 file open for on-demand reads, so the number of such trees is
// bounded by maxOpen: the least recently used one is closed. A tree that
// is evicted (or invalidated) while a request still uses it is closed when
// the last user releases it.
//
// Loading is single-flight per file: concurrent requests for the same file
// wait for one load, and requests for other files are never blocked by it.
type treeEntry struct {
	root  *gonexus.NXgroup
	err   error
	ready chan struct{} // closed once root/err are set

	// guarded by treeCache.mu
	loaded  bool
	refs    int
	evicted bool
	lastUse time.Time
}

type treeCache struct {
	mu      sync.Mutex
	entries map[string]*treeEntry
	opts    gonexus.LoadOptions
	maxOpen int
}

func newTreeCache(opts gonexus.LoadOptions, maxOpen int) *treeCache {
	return &treeCache{entries: make(map[string]*treeEntry), opts: opts, maxOpen: maxOpen}
}

// acquire returns the tree for localPath, loading it if needed, together
// with a release function the caller must call exactly once when done.
func (c *treeCache) acquire(localPath string) (*gonexus.NXgroup, func(), error) {
	c.mu.Lock()
	e, found := c.entries[localPath]
	if !found {
		e = &treeEntry{ready: make(chan struct{})}
		c.entries[localPath] = e
	}
	e.refs++
	e.lastUse = time.Now()
	c.mu.Unlock()

	if found {
		<-e.ready
	} else {
		root, err := gonexus.LoadWithOptions(localPath, c.opts)
		var toClose []*gonexus.NXgroup
		c.mu.Lock()
		if err != nil {
			e.err = err
			if c.entries[localPath] == e {
				delete(c.entries, localPath)
			}
		} else {
			e.root = root
			e.loaded = true
			toClose = c.evictLocked()
		}
		c.mu.Unlock()
		close(e.ready)
		closeTrees(toClose)
	}

	if e.err != nil {
		return nil, nil, internal("reading nexus file %s: %v", localPath, e.err)
	}
	return e.root, func() { c.release(e) }, nil
}

func (c *treeCache) release(e *treeEntry) {
	c.mu.Lock()
	e.refs--
	e.lastUse = time.Now()
	closeNow := e.evicted && e.refs == 0 && e.root != nil
	c.mu.Unlock()
	if closeNow {
		e.root.Close()
	}
}

// evictLocked closes the least recently used trees beyond maxOpen (lazy
// modes only) and returns those that can be closed right away. c.mu must
// be held; the caller closes the returned trees after unlocking.
func (c *treeCache) evictLocked() []*gonexus.NXgroup {
	if c.opts.Mode == gonexus.LazyOff || c.maxOpen <= 0 {
		return nil
	}
	var toClose []*gonexus.NXgroup
	for {
		open := 0
		var oldest *treeEntry
		oldestPath := ""
		for p, e := range c.entries {
			if !e.loaded {
				continue
			}
			open++
			if oldest == nil || e.lastUse.Before(oldest.lastUse) {
				oldest, oldestPath = e, p
			}
		}
		if open <= c.maxOpen {
			return toClose
		}
		delete(c.entries, oldestPath)
		oldest.evicted = true
		if oldest.refs == 0 {
			toClose = append(toClose, oldest.root)
		}
	}
}

// invalidate drops a cached tree, e.g. after the underlying file changed.
func (c *treeCache) invalidate(localPath string) {
	var toClose *gonexus.NXgroup
	c.mu.Lock()
	if e, ok := c.entries[localPath]; ok {
		delete(c.entries, localPath)
		e.evicted = true
		if e.loaded && e.refs == 0 {
			toClose = e.root
		}
	}
	c.mu.Unlock()
	if toClose != nil {
		toClose.Close()
	}
}

// closeAll closes every cached tree (used at shutdown).
func (c *treeCache) closeAll() {
	c.mu.Lock()
	var roots []*gonexus.NXgroup
	for p, e := range c.entries {
		delete(c.entries, p)
		e.evicted = true
		if e.loaded && e.refs == 0 {
			roots = append(roots, e.root)
		}
	}
	c.mu.Unlock()
	closeTrees(roots)
}

func closeTrees(roots []*gonexus.NXgroup) {
	for _, r := range roots {
		r.Close()
	}
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

	lazyMode, err := gonexus.ParseLazyMode(cfg.LazyMode)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	trees := newTreeCache(gonexus.LoadOptions{Mode: lazyMode, Threshold: cfg.LazyThreshold}, cfg.MaxOpenFiles)
	store.onFileChanged = trees.invalidate // drop parsed trees of replaced/evicted files
	srv := &server{cfg: cfg, store: store, trees: trees}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", srv.handleHealthz)
	mux.HandleFunc("/files", srv.handleFiles)
	mux.HandleFunc("/tree", srv.handleTree)
	mux.HandleFunc("/entries", srv.handleEntries)
	mux.HandleFunc("/group", srv.handleGroup)
	mux.HandleFunc("/field", srv.handleField)
	registerUI(mux) // browser UI at /ui/ (see ui.go)

	httpSrv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      logMiddleware(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: time.Duration(cfg.WriteTimeoutSeconds) * time.Second, // 0 = no limit
	}

	go func() {
		log.Printf("nexus-s3-server listening on %s (bucket=%s prefix=%q cache=%s lazy=%s threshold=%d max_open=%d)",
			cfg.Addr, cfg.Bucket, cfg.Prefix, cfg.CacheDir, lazyMode, cfg.LazyThreshold, cfg.MaxOpenFiles)
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
	trees.closeAll()
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
