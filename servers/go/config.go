// config.go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds everything the server needs. Values can come from a
// JSON/YAML config file (-config flag or CONFIG_FILE env var), from
// environment variables, or from built-in defaults, in that order of
// precedence. AWS credentials themselves are NOT read here: the AWS
// SDK's default credential chain (env vars, shared config/credentials
// file, EC2/ECS role, etc.) handles that on its own.
type Config struct {
	// S3
	Bucket         string // s3_bucket / S3_BUCKET (required)
	Prefix         string // s3_prefix / S3_PREFIX (optional, default "")
	Region         string // s3_region / S3_REGION / AWS_REGION (default "us-east-1")
	Endpoint       string // s3_endpoint / S3_ENDPOINT (optional; MinIO / non-AWS S3)
	ForcePathStyle bool   // s3_force_path_style / S3_FORCE_PATH_STYLE

	// Server
	Port string // port / PORT (default 8080)
	Addr string // derived: ":" + Port

	// Local disk cache for downloaded NeXus files, since HDF5 requires a
	// real file on disk - it cannot be read from an in-memory S3 stream.
	CacheDir string // cache_dir / NEXUS_CACHE_DIR (default: $TMPDIR/nexus-cache)

	// Safety cap on how many array elements /field will return in one
	// response unless the caller passes an explicit larger ?limit=.
	DefaultFieldLimit int // field_default_limit / FIELD_DEFAULT_LIMIT (default 10000)
	MaxFieldLimit     int // field_max_limit / FIELD_MAX_LIMIT (default 1000000)

	// aws settings
	AccessKey string // aws_access_key / AWS_ACCESS_KEY_ID (optional)
	SecretKey string // aws_secret / AWS_SECRET_ACCESS_KEY (optional)
}

// fileConfig mirrors Config but with pointer fields so we can tell
// "explicitly set in the file" apart from "zero value". Only non-nil
// fields override the env-var/default layer.
type fileConfig struct {
	Bucket            *string `json:"s3_bucket"            yaml:"s3_bucket"`
	Prefix            *string `json:"s3_prefix"            yaml:"s3_prefix"`
	Region            *string `json:"s3_region"            yaml:"s3_region"`
	Endpoint          *string `json:"s3_endpoint"          yaml:"s3_endpoint"`
	ForcePathStyle    *bool   `json:"s3_force_path_style"  yaml:"s3_force_path_style"`
	Port              *string `json:"port"                 yaml:"port"`
	CacheDir          *string `json:"cache_dir"             yaml:"cache_dir"`
	DefaultFieldLimit *int    `json:"field_default_limit"  yaml:"field_default_limit"`
	MaxFieldLimit     *int    `json:"field_max_limit"      yaml:"field_max_limit"`
	AccessKey         *string `json:"aws_access_key" yaml:"aws_access_key"`
	SecretKey         *string `json:"aws_secret"     yaml:"aws_secret"`
}

func loadConfig() (*Config, error) {
	// 1. Env vars + defaults (the fallback layer).
	cfg := &Config{
		Bucket:            os.Getenv("S3_BUCKET"),
		Prefix:            os.Getenv("S3_PREFIX"),
		Region:            firstNonEmpty(os.Getenv("S3_REGION"), os.Getenv("AWS_REGION"), "us-east-1"),
		Endpoint:          os.Getenv("S3_ENDPOINT"),
		ForcePathStyle:    envBool("S3_FORCE_PATH_STYLE", false),
		Port:              firstNonEmpty(os.Getenv("PORT"), "8080"),
		CacheDir:          firstNonEmpty(os.Getenv("NEXUS_CACHE_DIR"), filepath.Join(os.TempDir(), "nexus-cache")),
		DefaultFieldLimit: envInt("FIELD_DEFAULT_LIMIT", 10_000),
		MaxFieldLimit:     envInt("FIELD_MAX_LIMIT", 1_000_000),
		AccessKey:         os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretKey:         os.Getenv("AWS_SECRET_ACCESS_KEY"),
	}

	// 2. Config file (JSON or YAML), if any, overrides the above.
	path := configFilePath()
	if path != "" {
		fc, err := readFileConfig(path)
		if err != nil {
			return nil, fmt.Errorf("loading config file %s: %w", path, err)
		}
		applyFileConfig(cfg, fc)
	}

	cfg.Addr = ":" + cfg.Port

	if cfg.AccessKey != "" {
		os.Setenv("AWS_ACCESS_KEY_ID", cfg.AccessKey)
	}
	if cfg.SecretKey != "" {
		os.Setenv("AWS_SECRET_ACCESS_KEY", cfg.SecretKey)
	}

	if cfg.Bucket == "" {
		return nil, errRequiredEnv("S3_BUCKET (or s3_bucket in config file)")
	}
	if err := os.MkdirAll(cfg.CacheDir, 0o755); err != nil {
		return nil, err
	}
	return cfg, nil
}

// configFilePath resolves the config file location: -config flag takes
// priority over CONFIG_FILE env var. Returns "" if neither is set.
func configFilePath() string {
	var flagPath string
	if !flag.Parsed() {
		flag.StringVar(&flagPath, "config", "", "path to config file (JSON or YAML)")
		flag.Parse()
	}
	return firstNonEmpty(flagPath, os.Getenv("CONFIG_FILE"))
}

func readFileConfig(path string) (*fileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	fc := &fileConfig{}
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".json":
		err = json.Unmarshal(data, fc)
	case ".yaml", ".yml":
		err = yaml.Unmarshal(data, fc)
	default:
		// Unknown/no extension: try JSON first, fall back to YAML.
		if jsonErr := json.Unmarshal(data, fc); jsonErr != nil {
			err = yaml.Unmarshal(data, fc)
		}
	}
	if err != nil {
		return nil, err
	}
	return fc, nil
}

func applyFileConfig(cfg *Config, fc *fileConfig) {
	if fc.Bucket != nil {
		cfg.Bucket = *fc.Bucket
	}
	if fc.Prefix != nil {
		cfg.Prefix = *fc.Prefix
	}
	if fc.Region != nil {
		cfg.Region = *fc.Region
	}
	if fc.Endpoint != nil {
		cfg.Endpoint = *fc.Endpoint
	}
	if fc.ForcePathStyle != nil {
		cfg.ForcePathStyle = *fc.ForcePathStyle
	}
	if fc.Port != nil {
		cfg.Port = *fc.Port
	}
	if fc.CacheDir != nil {
		cfg.CacheDir = *fc.CacheDir
	}
	if fc.DefaultFieldLimit != nil {
		cfg.DefaultFieldLimit = *fc.DefaultFieldLimit
	}
	if fc.MaxFieldLimit != nil {
		cfg.MaxFieldLimit = *fc.MaxFieldLimit
	}
	if fc.AccessKey != nil {
		cfg.AccessKey = *fc.AccessKey
	}
	if fc.SecretKey != nil {
		cfg.SecretKey = *fc.SecretKey
	}
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
