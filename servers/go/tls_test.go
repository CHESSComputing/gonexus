package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTestCert writes a self-signed certificate (valid for 127.0.0.1 and
// localhost) and its key to dir/cert.pem and dir/key.pem, stamps both files
// with modification time mod (so tests do not depend on clock resolution),
// and returns the paths and a pool trusting the certificate.
func writeTestCert(t *testing.T, dir string, serial int64, notAfter time.Time, mod time.Time) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: "gonexus-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{certPath, keyPath} {
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return certPath, keyPath, pool
}

// startTLS serves "ok" over HTTPS on a random local port using tcfg.
func startTLS(t *testing.T, tcfg *tls.Config) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", tcfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

// servedSerial connects without verification and returns the serial number
// of the certificate the server presents.
func servedSerial(t *testing.T, addr string) int64 {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0].SerialNumber.Int64()
}

func TestValidateServerTLS(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"plain http", Config{}, false},
		{"cert and key", Config{ServerTLSCertFile: "c", ServerTLSKeyFile: "k"}, false},
		{"cert only", Config{ServerTLSCertFile: "c"}, true},
		{"key only", Config{ServerTLSKeyFile: "k"}, true},
		{"min 1.3", Config{ServerTLSCertFile: "c", ServerTLSKeyFile: "k", ServerTLSMinVersion: "1.3"}, false},
		{"bad min version", Config{ServerTLSCertFile: "c", ServerTLSKeyFile: "k", ServerTLSMinVersion: "1.1"}, true},
	}
	for _, tc := range cases {
		err := validateServerTLS(&tc.cfg)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
	if (&Config{}).TLSEnabled() || !(&Config{ServerTLSCertFile: "c", ServerTLSKeyFile: "k"}).TLSEnabled() {
		t.Errorf("TLSEnabled is wrong")
	}
}

func TestBuildTLSConfigRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	certA, keyA, _ := writeTestCert(t, dir, 1, now.Add(time.Hour), now)
	dirB := t.TempDir()
	_, keyB, _ := writeTestCert(t, dirB, 2, now.Add(time.Hour), now)

	if _, err := buildTLSConfig(&Config{ServerTLSCertFile: certA, ServerTLSKeyFile: keyA}); err != nil {
		t.Fatalf("valid pair rejected: %v", err)
	}
	if _, err := buildTLSConfig(&Config{ServerTLSCertFile: certA, ServerTLSKeyFile: keyB}); err == nil {
		t.Errorf("mismatched certificate and key should fail")
	}
	if _, err := buildTLSConfig(&Config{ServerTLSCertFile: filepath.Join(dir, "missing.pem"), ServerTLSKeyFile: keyA}); err == nil {
		t.Errorf("missing certificate file should fail")
	}
	junk := filepath.Join(dir, "junk.pem")
	os.WriteFile(junk, []byte("not pem"), 0o600)
	if _, err := buildTLSConfig(&Config{ServerTLSCertFile: junk, ServerTLSKeyFile: keyA}); err == nil {
		t.Errorf("non-PEM certificate should fail")
	}
}

func TestServesHTTPSAndClientsVerify(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, pool := writeTestCert(t, dir, 7, time.Now().Add(time.Hour), time.Now())
	tcfg, err := buildTLSConfig(&Config{ServerTLSCertFile: certPath, ServerTLSKeyFile: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	addr := startTLS(t, tcfg)

	good := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	resp, err := good.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("verified GET failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "ok" || resp.TLS == nil {
		t.Errorf("body %q, resp.TLS %v", body, resp.TLS)
	}

	untrusting := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: x509.NewCertPool()}}}
	if _, err := untrusting.Get("https://" + addr + "/"); err == nil {
		t.Errorf("a client that does not trust the certificate should be refused")
	}
}

func TestMinVersion13(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, _ := writeTestCert(t, dir, 3, time.Now().Add(time.Hour), time.Now())
	tcfg, err := buildTLSConfig(&Config{ServerTLSCertFile: certPath, ServerTLSKeyFile: keyPath, ServerTLSMinVersion: "1.3"})
	if err != nil {
		t.Fatal(err)
	}
	addr := startTLS(t, tcfg)
	if _, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12}); err == nil {
		t.Errorf("a TLS 1.2-only client should be refused when the minimum is 1.3")
	}
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("TLS 1.3 client refused: %v", err)
	}
	conn.Close()
}

func TestCertificateReload(t *testing.T) {
	dir := t.TempDir()
	base := time.Now()
	certPath, keyPath, _ := writeTestCert(t, dir, 1, base.Add(time.Hour), base)

	r, err := newCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	r.recheckEvery = 0 // check on every handshake
	addr := startTLS(t, &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: r.GetCertificate})

	if got := servedSerial(t, addr); got != 1 {
		t.Fatalf("initial serial = %d, want 1", got)
	}

	// A renewed pair is picked up without restarting.
	writeTestCert(t, dir, 2, base.Add(time.Hour), base.Add(1*time.Minute))
	if got := servedSerial(t, addr); got != 2 {
		t.Fatalf("after renewal serial = %d, want 2", got)
	}

	// A broken replacement is ignored; the previous certificate keeps being served.
	os.WriteFile(certPath, []byte("garbage"), 0o600)
	os.Chtimes(certPath, base.Add(2*time.Minute), base.Add(2*time.Minute))
	if got := servedSerial(t, addr); got != 2 {
		t.Fatalf("after a broken replacement serial = %d, want 2 (previous certificate)", got)
	}

	// A later good replacement is picked up again.
	writeTestCert(t, dir, 3, base.Add(time.Hour), base.Add(3*time.Minute))
	if got := servedSerial(t, addr); got != 3 {
		t.Fatalf("after recovery serial = %d, want 3", got)
	}
}
