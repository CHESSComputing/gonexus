package main

// Filesystem mode (file_system_mode, see FILESYSTEM.md): the "key" of a file
// is its absolute path on the server's filesystem, e.g. /nfs/chess/x/scan.nxs,
// so data that live in many unrelated places can be served without gathering
// them under one directory. Which files may be read is decided by
//
//   - the operating system: the server can only open what its own user may
//     read (a permission problem is reported as 403), and
//   - data_dir, if set: one or more allowed directories; any path outside them
//     is refused. Symlinks are resolved first, so a link inside an allowed
//     directory cannot be used to reach a file outside it.
//
// Only NeXus/HDF5 file names are served in any case.

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Bounds of one /files call (variables so that tests can lower them).
var (
	fsListLimit   = 5000             // most files returned
	fsListVisit   = 200000           // most directory entries examined
	fsListTimeout = 20 * time.Second // time budget
)

// fsRoot is one allowed directory.
type fsRoot struct {
	path string // cleaned absolute path, as configured
	real string // the same with symlinks resolved
}

// FSStore serves NeXus files by absolute path. See the comment at the top of
// this file.
type FSStore struct {
	roots  []fsRoot // allowed directories; empty means every path is allowed
	prefix string   // default directory for /files (optional)

	mu            sync.Mutex
	seen          map[string]fileSig // path -> signature at last Ensure
	onFileChanged func(localPath string)
}

// newFSStore reads the allowed directories from cfg.DataDir: one path, or
// several separated by the OS path-list separator (":" on Unix).
func newFSStore(cfg *Config) (*FSStore, error) {
	s := &FSStore{seen: make(map[string]fileSig)}
	if p := strings.TrimSpace(cfg.Prefix); filepath.IsAbs(p) {
		s.prefix = p // optional default directory for /files
	}

	var shown []string
	for _, d := range filepath.SplitList(cfg.DataDir) {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		abs, err := filepath.Abs(d)
		if err != nil {
			return nil, fmt.Errorf("data_dir %q: %w", d, err)
		}
		abs = filepath.Clean(abs)
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("data_dir %q: %w", d, err)
		}
		st, err := os.Stat(real)
		if err != nil {
			return nil, fmt.Errorf("data_dir %q: %w", d, err)
		}
		if !st.IsDir() {
			return nil, fmt.Errorf("data_dir %q is not a directory", d)
		}
		s.roots = append(s.roots, fsRoot{path: abs, real: real})
		shown = append(shown, abs)
	}

	if len(s.roots) == 0 {
		log.Printf("warning: file_system_mode without data_dir: ANY NeXus/HDF5 file readable by this " +
			"process can be opened by anyone who can reach the server. Set data_dir to the directories " +
			"that may be served (for example /nfs).")
	} else {
		log.Printf("filesystem store: serving NeXus files under %s", strings.Join(shown, ", "))
	}
	return s, nil
}

func (s *FSStore) SetOnFileChanged(fn func(localPath string)) {
	s.mu.Lock()
	s.onFileChanged = fn
	s.mu.Unlock()
}

// under reports whether the clean absolute path p is dir or lies below it.
func under(p, dir string) bool {
	sep := string(filepath.Separator)
	if dir == sep {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+sep)
}

// allowedLexical reports whether the clean absolute path p is inside an
// allowed directory, judging by the path as written.
func (s *FSStore) allowedLexical(p string) bool {
	if len(s.roots) == 0 {
		return true
	}
	for _, r := range s.roots {
		if under(p, r.path) || under(p, r.real) {
			return true
		}
	}
	return false
}

// allowedReal reports whether p, a path with symlinks resolved, is inside an
// allowed directory.
func (s *FSStore) allowedReal(p string) bool {
	if len(s.roots) == 0 {
		return true
	}
	for _, r := range s.roots {
		if under(p, r.real) {
			return true
		}
	}
	return false
}

// checkFSPath rejects what can never be a valid absolute path: empty,
// containing a NUL byte, relative, or containing a ".." element (which is
// refused rather than "cleaned", so it cannot be used to confuse the checks).
func checkFSPath(p string) error {
	if p == "" {
		return badRequest("missing required query parameter 'key'")
	}
	if strings.ContainsRune(p, 0) {
		return badRequest("invalid path")
	}
	if !filepath.IsAbs(p) {
		return badRequest("file must be an absolute path, got %q", p)
	}
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".." {
			return badRequest("invalid path %q: '..' is not allowed", p)
		}
	}
	return nil
}

// fsError maps a filesystem error to an API error.
func fsError(key string, err error) error {
	switch {
	case os.IsNotExist(err):
		return notFound("file %q not found", key)
	case os.IsPermission(err):
		return forbidden("permission denied reading %q", key)
	}
	return internal("accessing %q: %v", key, err)
}

// resolve validates key and returns the cleaned path and its file info.
func (s *FSStore) resolve(key string) (string, os.FileInfo, error) {
	if err := checkFSPath(key); err != nil {
		return "", nil, err
	}
	p := filepath.Clean(key)
	if !hasNexusSuffix(p) {
		return "", nil, badRequest("%q is not a NeXus/HDF5 file (%s)", key, strings.Join(nexusSuffixes, ", "))
	}
	if !s.allowedLexical(p) {
		return "", nil, forbidden("%q is outside the directories this server may read", key)
	}
	info, err := os.Stat(p) // follows symlinks
	if err != nil {
		return "", nil, fsError(key, err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, badRequest("%q is not a regular file", key)
	}
	if len(s.roots) > 0 {
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			return "", nil, fsError(key, err)
		}
		if !s.allowedReal(real) {
			return "", nil, forbidden("%q resolves outside the directories this server may read", key)
		}
	}
	return p, info, nil
}

// Ensure returns the path itself: files are opened in place. If the file's
// size or modification time changed since the previous call, the
// onFileChanged callback is invoked so a cached parsed tree is dropped.
func (s *FSStore) Ensure(ctx context.Context, key string) (string, error) {
	p, info, err := s.resolve(key)
	if err != nil {
		return "", err
	}
	sig := fileSig{size: info.Size(), mtime: info.ModTime().UnixNano()}
	s.mu.Lock()
	prev, had := s.seen[p]
	s.seen[p] = sig
	cb := s.onFileChanged
	s.mu.Unlock()
	if had && prev != sig && cb != nil {
		log.Printf("file changed, dropping cached tree: %s", p)
		cb(p)
	}
	return p, nil
}

// List lists NeXus files whose absolute path starts with prefix (a plain
// string prefix, as with S3 keys; "/nfs/chess/" lists that directory tree and
// "/nfs/chess/sc" also matches /nfs/chess/scan1/...). With no prefix nothing
// is listed: the tree of a shared filesystem is far too large to walk
// blindly. The walk is bounded by fsListLimit, fsListVisit and fsListTimeout,
// stays inside the allowed directories, and a partial result is returned if a
// bound is hit.
func (s *FSStore) List(ctx context.Context, prefixOverride string) ([]FileInfo, error) {
	prefix := strings.TrimSpace(prefixOverride)
	if prefix == "" {
		prefix = s.prefix
	}
	if prefix == "" {
		return nil, nil
	}
	if err := checkFSPath(prefix); err != nil {
		return nil, err
	}

	// Walk from the directory the prefix names or, when it ends in the
	// middle of a name, from that name's parent.
	norm := filepath.Clean(prefix)
	start := norm
	if strings.HasSuffix(prefix, "/") {
		if norm != "/" {
			norm += "/"
		}
	} else {
		start = filepath.Dir(norm)
	}

	if !s.allowedLexical(start) {
		return nil, forbidden("%q is outside the directories this server may read", prefix)
	}
	real, err := filepath.EvalSymlinks(start)
	if err != nil {
		return nil, nil // does not exist (yet): the caller may still be typing the path
	}
	if !s.allowedReal(real) {
		return nil, forbidden("%q resolves outside the directories this server may read", prefix)
	}
	if st, err := os.Stat(real); err != nil || !st.IsDir() {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, fsListTimeout)
	defer cancel()
	w := &fsWalker{s: s, ctx: ctx, prefix: norm}
	w.walk(start, 0, map[string]bool{real: true})
	if w.capped {
		log.Printf("list: %q stopped early (%d files found, %d entries examined); use a longer prefix", prefix, len(w.out), w.visited)
	}
	sort.Slice(w.out, func(i, j int) bool { return w.out[i].Key < w.out[j].Key })
	return w.out, nil
}

// fsWalker is the state of one List call.
type fsWalker struct {
	s       *FSStore
	ctx     context.Context
	prefix  string
	out     []FileInfo
	visited int
	capped  bool
}

// walk lists dir, following symlinks. ancestors holds the resolved paths of
// the directories on the current recursion path (cycle detection).
func (w *fsWalker) walk(dir string, depth int, ancestors map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // unreadable directory: skip it, keep listing the rest
	}
	for _, e := range entries {
		if w.capped || w.ctx.Err() != nil {
			w.capped = true
			return
		}
		w.visited++
		if w.visited > fsListVisit {
			w.capped = true
			return
		}
		name := e.Name()
		full := filepath.Join(dir, name)

		info, err := os.Stat(full) // follows symlinks
		if err != nil {
			continue // broken link or vanished entry
		}

		if info.IsDir() {
			if strings.HasPrefix(name, ".") || depth >= maxWalkDepth {
				continue // hidden dirs (.snapshot, ...) are not data
			}
			// Enter only directories that can contain a match.
			if !strings.HasPrefix(w.prefix, full+"/") && !strings.HasPrefix(full+"/", w.prefix) {
				continue
			}
			real, err := filepath.EvalSymlinks(full)
			if err != nil || !w.s.allowedReal(real) || ancestors[real] {
				continue // unreadable, outside the allowed directories, or a cycle
			}
			ancestors[real] = true
			w.walk(full, depth+1, ancestors)
			delete(ancestors, real)
			continue
		}

		if !info.Mode().IsRegular() || !hasNexusSuffix(name) || !strings.HasPrefix(full, w.prefix) {
			continue
		}
		if e.Type()&os.ModeSymlink != 0 && len(w.s.roots) > 0 {
			real, err := filepath.EvalSymlinks(full)
			if err != nil || !w.s.allowedReal(real) {
				continue // a link leading outside the allowed directories
			}
		}
		if len(w.out) >= fsListLimit {
			w.capped = true
			return
		}
		w.out = append(w.out, FileInfo{
			Key:          full,
			Size:         info.Size(),
			LastModified: info.ModTime(),
			ETag:         fmt.Sprintf("%x-%x", info.ModTime().UnixNano(), info.Size()),
		})
	}
}
