package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// maxWalkDepth bounds directory recursion in List, as a second line of
// defense after cycle detection.
const maxWalkDepth = 64

// LocalStore serves NeXus files straight from a directory tree on the
// server's filesystem (e.g. a data area or an NFS mount), without S3 and
// without copying anything into a cache: gonexus opens the files in place,
// so even multi-GB files are available immediately and take no extra disk.
//
// Keys are slash-separated paths relative to the root directory, exactly as
// they would be in a bucket that mirrors it, so switching between the S3 and
// the local store keeps every key valid. As with S3, the prefix is a plain
// string prefix of the key, not a directory.
//
// Symbolic links - to files and to directories - are followed, including
// links that point outside the root (that is the purpose of using them).
// Cycles are detected and skipped.
type LocalStore struct {
	root   string // absolute path of the root directory
	prefix string

	mu            sync.Mutex
	seen          map[string]fileSig // local path -> signature at last Ensure
	onFileChanged func(localPath string)
}

// fileSig identifies a version of a file well enough to notice rewrites.
type fileSig struct {
	size  int64
	mtime int64 // UnixNano
}

func newLocalStore(cfg *Config) (*LocalStore, error) {
	root, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("data_dir %q: %w", cfg.DataDir, err)
	}
	st, err := os.Stat(root) // follows a symlinked root
	if err != nil {
		return nil, fmt.Errorf("data_dir %q: %w", cfg.DataDir, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("data_dir %q is not a directory", cfg.DataDir)
	}
	return &LocalStore{
		root:   root,
		prefix: strings.TrimLeft(cfg.Prefix, "/"),
		seen:   make(map[string]fileSig),
	}, nil
}

func (s *LocalStore) SetOnFileChanged(fn func(localPath string)) {
	s.mu.Lock()
	s.onFileChanged = fn
	s.mu.Unlock()
}

// List walks the root (following symlinks) and returns every NeXus file
// whose key starts with the prefix. Directories that cannot contain a match
// are not entered, so a narrow prefix on a large tree is cheap.
func (s *LocalStore) List(ctx context.Context, prefixOverride string) ([]FileInfo, error) {
	prefix := s.prefix
	if prefixOverride != "" {
		prefix = strings.TrimLeft(prefixOverride, "/")
	}

	ancestors := make(map[string]bool)
	if real, err := filepath.EvalSymlinks(s.root); err == nil {
		ancestors[real] = true
	}

	var out []FileInfo
	if err := s.walk(ctx, s.root, "", prefix, 0, ancestors, &out); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// walk lists dir. rel is dir's key (slash-separated, "" for the root).
// ancestors holds the resolved paths of the directories on the current
// recursion path; entering one of them again would be a symlink cycle.
func (s *LocalStore) walk(ctx context.Context, dir, rel, prefix string, depth int, ancestors map[string]bool, out *[]FileInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// An unreadable directory must not hide everything else.
		log.Printf("list: cannot read %s: %v", dir, err)
		return nil
	}
	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(dir, name)
		key := name
		if rel != "" {
			key = rel + "/" + name
		}

		info, err := os.Stat(full) // follows symlinks
		if err != nil {
			continue // broken link or vanished entry
		}

		if info.IsDir() {
			if strings.HasPrefix(name, ".") || depth >= maxWalkDepth {
				continue // hidden dirs (.snapshot, .git, ...) are not data
			}
			// Descend only if some key below can match the prefix.
			if !strings.HasPrefix(prefix, key+"/") && !strings.HasPrefix(key+"/", prefix) {
				continue
			}
			real, err := filepath.EvalSymlinks(full)
			if err != nil {
				continue
			}
			if ancestors[real] {
				log.Printf("list: skipping %s: symlink cycle (%s is already being listed)", full, real)
				continue
			}
			ancestors[real] = true
			err = s.walk(ctx, full, key, prefix, depth+1, ancestors, out)
			delete(ancestors, real)
			if err != nil {
				return err
			}
			continue
		}

		if !info.Mode().IsRegular() || !hasNexusSuffix(name) || !strings.HasPrefix(key, prefix) {
			continue
		}
		*out = append(*out, FileInfo{
			Key:          key,
			Size:         info.Size(),
			LastModified: info.ModTime(),
			ETag:         fmt.Sprintf("%x-%x", info.ModTime().UnixNano(), info.Size()),
		})
	}
	return nil
}

// Ensure resolves key to a path under the root and returns it; nothing is
// copied. Only NeXus/HDF5 file names are served. If the file's size or
// modification time changed since the previous call, the onFileChanged
// callback is invoked so a cached parsed tree of the old contents is dropped.
func (s *LocalStore) Ensure(ctx context.Context, key string) (string, error) {
	if key == "" {
		return "", badRequest("missing required query parameter 'key'")
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == ".." {
			return "", badRequest("invalid key %q", key)
		}
	}
	clean := path.Clean("/" + key)
	if !hasNexusSuffix(clean) {
		return "", badRequest("key %q is not a NeXus/HDF5 file (%s)", key, strings.Join(nexusSuffixes, ", "))
	}

	full := filepath.Join(s.root, filepath.FromSlash(clean))
	info, err := os.Stat(full) // follows symlinks
	if err != nil {
		if os.IsNotExist(err) {
			return "", notFound("file %q not found", key)
		}
		return "", internal("accessing %q: %v", key, err)
	}
	if !info.Mode().IsRegular() {
		return "", badRequest("key %q is not a regular file", key)
	}

	sig := fileSig{size: info.Size(), mtime: info.ModTime().UnixNano()}
	s.mu.Lock()
	prev, had := s.seen[full]
	s.seen[full] = sig
	cb := s.onFileChanged
	s.mu.Unlock()
	if had && prev != sig && cb != nil {
		log.Printf("local file changed, dropping cached tree: %s", full)
		cb(full)
	}
	return full, nil
}
