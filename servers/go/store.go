package main

import "context"

// FileStore is where the server gets NeXus files from. There are two
// implementations: *Store (an S3 bucket, files are downloaded into a local
// cache) and *LocalStore (a directory on the server's filesystem, files are
// opened in place).
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
)
