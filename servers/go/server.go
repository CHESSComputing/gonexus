package main

// The server: build information, the cache of parsed NeXus trees, the server
// definition with its routes, and Server(), which wires everything together
// and runs it. The request handlers are in handlers.go.

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/CHESSComputing/gonexus/gonexus"
)

// gitVersion is stamped at build time: `make build` replaces the VERSION
// placeholder inside the quotes below with the latest git tag (see the
// Makefile). Built any other way it keeps the placeholder text.
var gitVersion = "{{VERSION}}"

// versionPlaceholder is that placeholder, assembled from two pieces so the
// build-time substitution does not rewrite this comparison value too.
const versionPlaceholder = "{{" + "VERSION}}"

// Info describes this build: git tag, Go version and the current time.
func Info() string {
	version := gitVersion
	if version == versionPlaceholder {
		version = "dev" // not built through `make build`
	}
	goVersion := runtime.Version()
	tstamp := time.Now()
	return fmt.Sprintf("git=%s go=%s date=%s", version, goVersion, tstamp)
}

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
	store FileStore
	trees *treeCache
	auth  *AuthEnv // validates /view payloads (see auth.go)

	sessions *viewSessions // file_system_mode only: who may read which file (see session.go)
}

// mode names where files come from: "filesystem" (absolute paths), "local"
// (keys relative to data_dir) or "s3".
func (s *server) mode() string {
	switch {
	case s.cfg.FileSystemMode:
		return "filesystem"
	case s.cfg.DataDir != "":
		return "local"
	}
	return "s3"
}

// routes registers every endpoint on a new mux. The browser UI is served
// under /ui/ (see ui.go).
func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/files", s.guardListing(s.handleFiles))
	mux.HandleFunc("/tree", s.guardData(s.handleTree))
	mux.HandleFunc("/entries", s.guardData(s.handleEntries))
	mux.HandleFunc("/group", s.guardData(s.handleGroup))
	mux.HandleFunc("/field", s.guardData(s.handleField))
	mux.HandleFunc("/view", s.handleView)
	mux.HandleFunc("/viewform", s.handleViewForm)
	registerUI(mux, s.cfg.UIConfigFile)
	return mux
}

// Server builds the file store, the tree cache and the HTTP server from cfg
// and serves until the process is interrupted.
func Server(cfg *Config) {
	var err error
	ctx := context.Background()
	var store FileStore
	source := "s3"
	if cfg.FileSystemMode {
		store, err = newFSStore(cfg)
		if err != nil {
			log.Fatalf("filesystem store error: %v", err)
		}
		source = "filesystem"
		if cfg.DataDir != "" {
			source += ":" + cfg.DataDir
		}
	} else if cfg.DataDir != "" {
		store, err = newLocalStore(cfg)
		if err != nil {
			log.Fatalf("local store error: %v", err)
		}
		source = "local:" + cfg.DataDir
	} else {
		store, err = newStore(ctx, cfg)
		if err != nil {
			log.Fatalf("s3 client error: %v", err)
		}
	}

	lazyMode, err := gonexus.ParseLazyMode(cfg.LazyMode)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	trees := newTreeCache(gonexus.LoadOptions{Mode: lazyMode, Threshold: cfg.LazyThreshold}, cfg.MaxOpenFiles)
	store.SetOnFileChanged(trees.invalidate) // drop parsed trees of replaced/evicted/modified files
	auth, err := newAuthEnv(cfg)
	if err != nil {
		log.Fatalf("auth error: %v", err)
	}
	if auth.Verifier == nil {
		log.Printf("warning: neither token_secret nor token_public_key_file is set, so token signatures are NOT verified and anyone can forge a token for /view (see AUTH.md)")
	}
	srv := &server{cfg: cfg, store: store, trees: trees, auth: auth}
	if cfg.FileSystemMode {
		srv.sessions, err = newViewSessions(time.Duration(cfg.SessionTTLSeconds) * time.Second)
		if err != nil {
			log.Fatalf("session key error: %v", err)
		}
	}

	mux := srv.routes()

	var tlsCfg *tls.Config // nil means plain HTTP
	if cfg.TLSEnabled() {
		tlsCfg, err = buildTLSConfig(cfg)
		if err != nil {
			log.Fatalf("tls error: %v", err)
		}
	}
	scheme := "http"
	if tlsCfg != nil {
		scheme = "https"
	}

	httpSrv := &http.Server{
		Addr:         cfg.Addr,
		TLSConfig:    tlsCfg,
		Handler:      logMiddleware(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: time.Duration(cfg.WriteTimeoutSeconds) * time.Second, // 0 = no limit
	}

	go func() {
		log.Printf("nexus-s3-server listening on %s (%s, source=%s bucket=%s prefix=%q cache=%s lazy=%s threshold=%d max_open=%d view_payload=%s)",
			cfg.Addr, scheme, source, cfg.Bucket, cfg.Prefix, cfg.CacheDir, lazyMode, cfg.LazyThreshold, cfg.MaxOpenFiles, cfg.ViewPayloadType)
		var err error
		if tlsCfg != nil {
			err = httpSrv.ListenAndServeTLS("", "") // the certificate comes from tlsCfg.GetCertificate
		} else {
			err = httpSrv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
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
