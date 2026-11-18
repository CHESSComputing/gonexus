package main

import "context"

// FileStore is where the server gets NeXus files from. There are three
// implementations: *Store (an S3 bucket, files are downloaded into a local
// cache), *LocalStore (a directory on the server's filesystem addressed by
// keys relative to it, files are opened in place) and *FSStore (any path on
// the server's filesystem, addressed by absolute path, optionally restricted
// to allowed directories).
type FileStore interface {
	// List returns the NeXus files whose key starts with the store's
	// prefix, or with prefixOverride when it is non-empty.
	List(ctx context.Context, prefixOverride string) ([]FileInfo, error)

	// Ensure returns a local filesystem path that gonexus can open for the
	// given key. The path is only valid for reading.
	Ensure(ctx context.Context, key string) (string, error)

	// SetOnFileChanged registers a callback that is invoked with a local
	// path whenever a file previously returned by Ensure was replaced,
	// modified or deleted, so cached parsed trees of it can be dropped.
	SetOnFileChanged(fn func(localPath string))
}

var (
	_ FileStore = (*Store)(nil)
	_ FileStore = (*LocalStore)(nil)
	_ FileStore = (*FSStore)(nil)
)
