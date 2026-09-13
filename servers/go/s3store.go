package main

import (
	"context"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// nexusSuffixes lists the file extensions treated as NeXus/HDF5 data when
// listing a bucket.
var nexusSuffixes = []string{".nxs", ".h5", ".hdf5"}

// FileInfo is what /files reports for each S3 object.
type FileInfo struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
	ETag         string    `json:"etag,omitempty"`
}

// Store wraps an S3 client and a local on-disk cache directory. NeXus/HDF5
// files must live on a real filesystem to be opened (gonexus, like h5py,
// reads via the HDF5 C library, not an io.Reader), so every object is
// downloaded to CacheDir on first access and re-used, keyed by the S3
// ETag, on subsequent requests.
type Store struct {
	client   *s3.Client
	bucket   string
	prefix   string
	cacheDir string

	mu       sync.Mutex // guards keyLocks
	keyLocks map[string]*sync.Mutex
}

func newStore(ctx context.Context, cfg *Config) (*Store, error) {
	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}

	httpClient, err := buildHTTPClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("configuring TLS for S3 endpoint: %w", err)
	}
	if httpClient != nil {
		loadOpts = append(loadOpts, awsconfig.WithHTTPClient(httpClient))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.ForcePathStyle
	})

	return &Store{
		client:   client,
		bucket:   cfg.Bucket,
		prefix:   cfg.Prefix,
		cacheDir: cfg.CacheDir,
		keyLocks: make(map[string]*sync.Mutex),
	}, nil
}

// buildHTTPClient returns a custom *http.Client when the config asks for
// non-default TLS handling against S3_ENDPOINT (a self-signed cert, as
// with a local VersityGW or MinIO instance), or nil to let the AWS SDK
// use its own default client (the normal case for real AWS S3).
func buildHTTPClient(cfg *Config) (*http.Client, error) {
	if !cfg.TLSInsecureSkipVerify && cfg.TLSCACertFile == "" {
		return nil, nil
	}

	tlsCfg := &tls.Config{}

	if cfg.TLSInsecureSkipVerify {
		log.Printf("warning: S3_TLS_INSECURE_SKIP_VERIFY is set - TLS certificate verification is DISABLED for %s. Only use this against a trusted local/dev endpoint.", cfg.Endpoint)
		tlsCfg.InsecureSkipVerify = true
	} else if cfg.TLSCACertFile != "" {
		pem, err := os.ReadFile(cfg.TLSCACertFile)
		if err != nil {
			return nil, fmt.Errorf("reading S3_TLS_CA_FILE %s: %w", cfg.TLSCACertFile, err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in S3_TLS_CA_FILE %s", cfg.TLSCACertFile)
		}
		tlsCfg.RootCAs = pool
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsCfg
	return &http.Client{Transport: transport}, nil
}

// List returns every object under bucket/prefixOverride (falling back to
// the store's configured default prefix when prefixOverride is empty)
// whose key ends in a recognized NeXus/HDF5 suffix.
func (s *Store) List(ctx context.Context, prefixOverride string) ([]FileInfo, error) {
	prefix := s.prefix
	if prefixOverride != "" {
		prefix = prefixOverride
	}

	var out []FileInfo
	var continuation *string
	for {
		resp, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: continuation,
		})
		if err != nil {
			return nil, fmt.Errorf("listing s3://%s/%s: %w", s.bucket, prefix, err)
		}
		for _, obj := range resp.Contents {
			key := aws.ToString(obj.Key)
			if !hasNexusSuffix(key) {
				continue
			}
			fi := FileInfo{Key: key}
			if obj.Size != nil {
				fi.Size = *obj.Size
			}
			if obj.LastModified != nil {
				fi.LastModified = *obj.LastModified
			}
			if obj.ETag != nil {
				fi.ETag = strings.Trim(*obj.ETag, `"`)
			}
			out = append(out, fi)
		}
		if resp.IsTruncated == nil || !*resp.IsTruncated {
			break
		}
		continuation = resp.NextContinuationToken
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func hasNexusSuffix(key string) bool {
	lower := strings.ToLower(key)
	for _, suf := range nexusSuffixes {
		if strings.HasSuffix(lower, suf) {
			return true
		}
	}
	return false
}

// Ensure makes sure the S3 object at key is present locally and returns
// its local path, downloading it (or re-downloading it, if the object's
// ETag changed since it was last cached) as needed. Concurrent requests
// for the same key are serialized so the object is only downloaded once.
func (s *Store) Ensure(ctx context.Context, key string) (string, error) {
	if key == "" {
		return "", badRequest("missing required query parameter 'key'")
	}

	lock := s.lockFor(key)
	lock.Lock()
	defer lock.Unlock()

	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", notFound("object s3://%s/%s not found: %v", s.bucket, key, err)
	}
	etag := strings.Trim(aws.ToString(head.ETag), `"`)

	localPath := s.localPath(key)
	etagPath := localPath + ".etag"

	if cached, err := os.ReadFile(etagPath); err == nil && string(cached) == etag {
		if _, err := os.Stat(localPath); err == nil {
			return localPath, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return "", internal("creating cache directory: %v", err)
	}

	log.Printf("downloading s3://%s/%s -> %s", s.bucket, key, localPath)
	getResp, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", internal("downloading s3://%s/%s: %v", s.bucket, key, err)
	}
	defer getResp.Body.Close()

	tmp, err := os.CreateTemp(filepath.Dir(localPath), ".download-*")
	if err != nil {
		return "", internal("creating temp file: %v", err)
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, getResp.Body); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", internal("writing downloaded object: %v", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", internal("closing downloaded file: %v", err)
	}
	if err := os.Rename(tmpName, localPath); err != nil {
		os.Remove(tmpName)
		return "", internal("finalizing cached file: %v", err)
	}
	if err := os.WriteFile(etagPath, []byte(etag), 0o644); err != nil {
		// Non-fatal: the file is usable, just won't cache-hit as cleanly
		// next time.
		log.Printf("warning: could not write etag sidecar for %s: %v", key, err)
	}
	return localPath, nil
}

// localPath maps an S3 key to a stable, collision-free path under
// CacheDir. Keys can contain '/', which is fine on disk, but we also add
// a short hash prefix so keys that only differ by characters that are
// unsafe on some filesystems still can't collide.
func (s *Store) localPath(key string) string {
	sum := sha1.Sum([]byte(key))
	h := hex.EncodeToString(sum[:])[:8]
	safe := strings.ReplaceAll(key, "..", "_")
	return filepath.Join(s.cacheDir, h, safe)
}

func (s *Store) lockFor(key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.keyLocks[key]
	if !ok {
		l = &sync.Mutex{}
		s.keyLocks[key] = l
	}
	return l
}
