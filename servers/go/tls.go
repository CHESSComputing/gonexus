package main

// HTTPS support for the server's own listener (see TLS.md).
//
// Not to be confused with the S3_TLS_* settings, which concern the
// connection *to* an S3 endpoint.

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

const (
	// certRecheckEvery is how often, at most, the certificate files are
	// checked for a renewal (the check happens when a TLS handshake comes in).
	certRecheckEvery = 10 * time.Second
	// certExpiryWarn: warn in the log when the certificate expires sooner.
	certExpiryWarn = 30 * 24 * time.Hour
)

// TLSEnabled reports whether the server should speak HTTPS (a certificate
// and a key are configured) rather than plain HTTP.
func (c *Config) TLSEnabled() bool {
	return c.ServerTLSCertFile != "" && c.ServerTLSKeyFile != ""
}

// validateServerTLS checks that the TLS settings are consistent. It does not
// read the files; buildTLSConfig does that.
func validateServerTLS(cfg *Config) error {
	if (cfg.ServerTLSCertFile == "") != (cfg.ServerTLSKeyFile == "") {
		return fmt.Errorf("HTTPS needs both a certificate and a key: set NEXUS_TLS_CERT_FILE (tls_cert_file) " +
			"and NEXUS_TLS_KEY_FILE (tls_key_file) together, or neither for plain HTTP")
	}
	switch cfg.ServerTLSMinVersion {
	case "", "1.2", "1.3":
	default:
		return fmt.Errorf("invalid NEXUS_TLS_MIN_VERSION / tls_min_version %q (want 1.2 or 1.3)", cfg.ServerTLSMinVersion)
	}
	return nil
}

// buildTLSConfig loads the configured certificate and key and returns the
// tls.Config for the listener. It fails if they cannot be read, do not form
// a matching pair, or are not valid PEM, so a bad setup stops the server at
// startup instead of failing every connection later.
func buildTLSConfig(cfg *Config) (*tls.Config, error) {
	if fi, err := os.Stat(cfg.ServerTLSKeyFile); err == nil && fi.Mode().Perm()&0o007 != 0 {
		log.Printf("warning: TLS key file %s is readable by other users (mode %v); consider chmod 600 or 640", cfg.ServerTLSKeyFile, fi.Mode().Perm())
	}
	r, err := newCertReloader(cfg.ServerTLSCertFile, cfg.ServerTLSKeyFile)
	if err != nil {
		return nil, err
	}
	minVersion := uint16(tls.VersionTLS12)
	if cfg.ServerTLSMinVersion == "1.3" {
		minVersion = tls.VersionTLS13
	}
	return &tls.Config{MinVersion: minVersion, GetCertificate: r.GetCertificate}, nil
}

// certReloader serves a certificate/key pair from disk and picks up a
// renewed pair (e.g. from certbot) without a restart: when a handshake
// arrives and recheckEvery has passed, it compares the files' modification
// times with those it last saw and, if they changed, reloads. A pair that
// cannot be loaded is reported and the previous certificate keeps being
// served, and it is not retried until a file changes again.
type certReloader struct {
	certFile, keyFile string
	recheckEvery      time.Duration

	mu        sync.Mutex
	cert      *tls.Certificate
	certMod   time.Time
	keyMod    time.Time
	lastCheck time.Time
}

func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile, recheckEvery: certRecheckEvery}
	if err := r.reload(); err != nil {
		return nil, err
	}
	log.Printf("TLS: serving %s", describeCertificate(r.cert))
	warnIfExpiring(r.cert)
	return r, nil
}

// modTime returns the modification time of path (following symlinks, as
// certbot and Kubernetes secrets use them), or the zero time on error.
func modTime(path string) time.Time {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// reload reads the pair from disk and, on success, makes it current. Either
// way the files' modification times are remembered.
func (r *certReloader) reload() error {
	certMod, keyMod := modTime(r.certFile), modTime(r.keyFile)
	r.mu.Lock()
	r.certMod, r.keyMod, r.lastCheck = certMod, keyMod, time.Now()
	r.mu.Unlock()

	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("loading TLS certificate %s and key %s: %w", r.certFile, r.keyFile, err)
	}
	if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil {
		cert.Leaf = leaf
	}
	r.mu.Lock()
	r.cert = &cert
	r.mu.Unlock()
	return nil
}

// GetCertificate implements tls.Config.GetCertificate.
func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	due := time.Since(r.lastCheck) >= r.recheckEvery
	if due {
		r.lastCheck = time.Now() // only one handshake does the check
	}
	cert, certMod, keyMod := r.cert, r.certMod, r.keyMod
	r.mu.Unlock()

	if due && (!modTime(r.certFile).Equal(certMod) || !modTime(r.keyFile).Equal(keyMod)) {
		if err := r.reload(); err != nil {
			log.Printf("TLS: certificate files changed but could not be loaded; still serving the previous certificate: %v", err)
		} else {
			r.mu.Lock()
			cert = r.cert
			r.mu.Unlock()
			log.Printf("TLS: reloaded %s", describeCertificate(cert))
			warnIfExpiring(cert)
		}
	}
	return cert, nil
}

func describeCertificate(c *tls.Certificate) string {
	if c == nil || c.Leaf == nil {
		return "certificate (details unavailable)"
	}
	l := c.Leaf
	return fmt.Sprintf("certificate subject=%q dns=%v ips=%v not_after=%s",
		l.Subject.String(), l.DNSNames, l.IPAddresses, l.NotAfter.UTC().Format(time.RFC3339))
}

func warnIfExpiring(c *tls.Certificate) {
	if c == nil || c.Leaf == nil {
		return
	}
	left := time.Until(c.Leaf.NotAfter)
	switch {
	case left <= 0:
		log.Printf("warning: the TLS certificate EXPIRED on %s; clients will reject connections", c.Leaf.NotAfter.UTC().Format(time.RFC3339))
	case left < certExpiryWarn:
		log.Printf("warning: the TLS certificate expires in %d days (%s)", int(left.Hours()/24), c.Leaf.NotAfter.UTC().Format(time.RFC3339))
	}
}
