package main

import (
	"os"
	"path/filepath"
	"strconv"
)

// Config holds everything the server needs, all sourced from environment
// variables so the binary is trivial to run in a container. AWS
// credentials themselves are NOT read here: the AWS SDK's default
// credential chain (env vars, shared config/credentials file, EC2/ECS
// role, etc.) handles that on its own.
type Config struct {
	// S3
	Bucket         string // S3_BUCKET (required)
	Prefix         string // S3_PREFIX (optional, default "")
	Region         string // S3_REGION / AWS_REGION (default "us-east-1")
	Endpoint       string // S3_ENDPOINT (optional; MinIO / non-AWS S3)
	ForcePathStyle bool   // S3_FORCE_PATH_STYLE (needed by most non-AWS S3, e.g. MinIO)

	// Server
	Addr string // HOST:PORT to listen on, built from PORT (default 8080)

	// Local disk cache for downloaded NeXus files, since HDF5 requires a
	// real file on disk - it cannot be read from an in-memory S3 stream.
	CacheDir string // NEXUS_CACHE_DIR (default: $TMPDIR/nexus-cache)

	// Safety cap on how many array elements /field will return in one
	// response unless the caller passes an explicit larger ?limit=.
	DefaultFieldLimit int // FIELD_DEFAULT_LIMIT (default 10000)
	MaxFieldLimit     int // FIELD_MAX_LIMIT (default 1000000)
}

func loadConfig() (*Config, error) {
	cfg := &Config{
		Bucket:            os.Getenv("S3_BUCKET"),
		Prefix:            os.Getenv("S3_PREFIX"),
		Region:            firstNonEmpty(os.Getenv("S3_REGION"), os.Getenv("AWS_REGION"), "us-east-1"),
		Endpoint:          os.Getenv("S3_ENDPOINT"),
		ForcePathStyle:    envBool("S3_FORCE_PATH_STYLE", false),
		CacheDir:          firstNonEmpty(os.Getenv("NEXUS_CACHE_DIR"), filepath.Join(os.TempDir(), "nexus-cache")),
		DefaultFieldLimit: envInt("FIELD_DEFAULT_LIMIT", 10_000),
		MaxFieldLimit:     envInt("FIELD_MAX_LIMIT", 1_000_000),
	}

	port := firstNonEmpty(os.Getenv("PORT"), "8080")
	cfg.Addr = ":" + port

	if cfg.Bucket == "" {
		return nil, errRequiredEnv("S3_BUCKET")
	}
	if err := os.MkdirAll(cfg.CacheDir, 0o755); err != nil {
		return nil, err
	}
	return cfg, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
