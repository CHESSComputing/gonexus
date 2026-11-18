package main

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testNow = time.Unix(1795010000, 0) // between iat and exp of the sample token

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// makeToken builds a token with the given header alg and claims; sign, if
// non-nil, computes the signature of "header.payload".
func makeToken(t *testing.T, alg string, claims map[string]interface{}, sign func(string) []byte) string {
	t.Helper()
	h, _ := json.Marshal(map[string]string{"alg": alg, "typ": "JWT"})
	c, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signed := b64(h) + "." + b64(c)
	var sig []byte
	if sign != nil {
		sig = sign(signed)
	}
	return signed + "." + b64(sig)
}

func sampleClaims() map[string]interface{} {
	return map[string]interface{}{
		"iss": "Some Authz server",
		"sub": "7269133994d94263b1cfa13769799b07",
		"aud": []string{"d2913082aec04aebb42239fd550bc0ca"},
		"exp": 1795012320,
		"iat": 1795008720,
		"custom_claims": map[string]interface{}{
			"user":        "some-user",
			"scope":       "read",
			"kind":        "client_credentials",
			"application": "FOXDEN frontend",
			"roles":       nil,
			"btrs":        []string{"test-demo", "test-987-b", "test-123-a"},
			"groups":      []string{"group-a", "group-b"},
			"scopes":      []string{"read", "delete", "write"},
		},
	}
}

func withCustom(c map[string]interface{}, k string, v interface{}) map[string]interface{} {
	c["custom_claims"].(map[string]interface{})[k] = v
	return c
}

func unsigned(t *testing.T, claims map[string]interface{}) string {
	return makeToken(t, "none", claims, nil)
}

func hs256(secret string) func(string) []byte {
	return func(s string) []byte {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write([]byte(s))
		return m.Sum(nil)
	}
}

func testEnv() *AuthEnv {
	return &AuthEnv{Now: func() time.Time { return testNow }}
}

func wantStatus(t *testing.T, err error, want int) {
	t.Helper()
	if want == 0 {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	var ae *apiError
	if !errors.As(err, &ae) {
		t.Fatalf("error = %v, want an apiError with status %d", err, want)
	}
	if ae.status != want {
		t.Fatalf("status = %d (%s), want %d", ae.status, ae.msg, want)
	}
}

func TestViewPayloadValidate(t *testing.T) {
	cases := []struct {
		name   string
		token  string
		status int
	}{
		{"valid token with read scope", unsigned(t, sampleClaims()), 0},
		{"missing token", "", 401},
		{"not a jwt", "abc", 401},
		{"garbage claims", "e30.!!!.x", 401},
		{"expired", unsigned(t, func() map[string]interface{} { c := sampleClaims(); c["exp"] = 1795008800; return c }()), 401},
		{"no exp", unsigned(t, func() map[string]interface{} { c := sampleClaims(); delete(c, "exp"); return c }()), 401},
		{"not yet valid", unsigned(t, func() map[string]interface{} { c := sampleClaims(); c["nbf"] = 1795011000; return c }()), 401},
		{"fractional exp is accepted", unsigned(t, func() map[string]interface{} { c := sampleClaims(); c["exp"] = 1795012320.5; return c }()), 0},
		{"aud as a string", unsigned(t, func() map[string]interface{} { c := sampleClaims(); c["aud"] = "x"; return c }()), 0},
		{"read only in scopes", unsigned(t, withCustom(withCustom(sampleClaims(), "scope", "write"), "scopes", []string{"write", "read"})), 0},
		{"read only in scope", unsigned(t, withCustom(withCustom(sampleClaims(), "scope", "read"), "scopes", nil)), 0},
		{"no read scope", unsigned(t, withCustom(withCustom(sampleClaims(), "scope", "write"), "scopes", []string{"write", "delete"})), 403},
		{"no scopes at all", unsigned(t, withCustom(withCustom(sampleClaims(), "scope", ""), "scopes", nil)), 403},
	}
	for _, tc := range cases {
		p := &ViewPayload{File: "a.nxs", Token: tc.token}
		err := p.Validate(context.Background(), testEnv())
		func() {
			defer func() {
				if t.Failed() {
					t.Logf("case: %s", tc.name)
				}
			}()
			wantStatus(t, err, tc.status)
		}()
	}
}

func TestSignatureHS256(t *testing.T) {
	env := testEnv()
	var err error
	env.Verifier, err = newTokenVerifier("s3cret", "")
	if err != nil {
		t.Fatal(err)
	}
	good := makeToken(t, "HS256", sampleClaims(), hs256("s3cret"))
	wantStatus(t, (&ViewPayload{Token: good}).Validate(context.Background(), env), 0)

	for name, tok := range map[string]string{
		"wrong secret":      makeToken(t, "HS256", sampleClaims(), hs256("other")),
		"alg none":          makeToken(t, "none", sampleClaims(), nil),
		"empty signature":   makeToken(t, "HS256", sampleClaims(), nil),
		"alg RS256 w/o key": makeToken(t, "RS256", sampleClaims(), hs256("s3cret")),
		"tampered claims": func() string {
			parts := strings.Split(good, ".")
			c := sampleClaims()
			c["exp"] = 9999999999
			raw, _ := json.Marshal(c)
			return parts[0] + "." + b64(raw) + "." + parts[2]
		}(),
	} {
		err := (&ViewPayload{Token: tok}).Validate(context.Background(), env)
		if err == nil {
			t.Errorf("%s: token accepted", name)
			continue
		}
		wantStatus(t, err, 401)
	}
}

func TestSignatureRS256(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	path := filepath.Join(t.TempDir(), "pub.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	env := testEnv()
	if env.Verifier, err = newTokenVerifier("", path); err != nil {
		t.Fatal(err)
	}
	sign := func(k *rsa.PrivateKey) func(string) []byte {
		return func(s string) []byte {
			sum := sha256.Sum256([]byte(s))
			sig, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, sum[:])
			if err != nil {
				t.Fatal(err)
			}
			return sig
		}
	}
	wantStatus(t, (&ViewPayload{Token: makeToken(t, "RS256", sampleClaims(), sign(key))}).Validate(context.Background(), env), 0)

	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	wantStatus(t, (&ViewPayload{Token: makeToken(t, "RS256", sampleClaims(), sign(other))}).Validate(context.Background(), env), 401)
	// A token that claims HS256 must not be checked with the public key as an HMAC secret.
	wantStatus(t, (&ViewPayload{Token: makeToken(t, "HS256", sampleClaims(), hs256(string(der)))}).Validate(context.Background(), env), 401)
}

func TestNewTokenVerifierErrors(t *testing.T) {
	if _, err := newTokenVerifier("", filepath.Join(t.TempDir(), "missing.pem")); err == nil {
		t.Error("missing key file accepted")
	}
	bad := filepath.Join(t.TempDir(), "bad.pem")
	_ = os.WriteFile(bad, []byte("not pem"), 0o600)
	if _, err := newTokenVerifier("", bad); err == nil {
		t.Error("bad key file accepted")
	}
	if v, err := newTokenVerifier("", ""); v != nil || err != nil {
		t.Errorf("no keys: got %v, %v; want nil, nil", v, err)
	}
}

func TestFoxdenViewPayloadValidate(t *testing.T) {
	tok := unsigned(t, sampleClaims())
	for _, tc := range []struct {
		name   string
		foxden int
		did    string
		token  string
		status int
	}{
		{"allowed", 200, "/beamline=3a/btr=test-demo/cycle=2026-1/sample=s1", tok, 0},
		{"allowed 204", 204, "d1", tok, 0},
		{"foxden says forbidden", 403, "d1", tok, 403},
		{"foxden says unknown dataset", 404, "d1", tok, 403},
		{"foxden rejects token", 401, "d1", tok, 401},
		{"foxden fails", 500, "d1", tok, 502},
		{"missing did", 200, "  ", tok, 400},
		{"invalid token never reaches foxden", 200, "d1", "junk", 401},
		{"no read scope never reaches foxden", 200, "d1", unsigned(t, withCustom(withCustom(sampleClaims(), "scope", "write"), "scopes", []string{"write"})), 403},
	} {
		var got foxdenRequest
		var auth string
		called := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			_ = json.NewDecoder(r.Body).Decode(&got)
			auth = r.Header.Get("Authorization")
			w.WriteHeader(tc.foxden)
		}))
		env := testEnv()
		env.FoxdenURL = srv.URL
		env.HTTP = srv.Client()
		p := &FoxdenViewPayload{File: "/nfs/a.nxs", Token: tc.token, Did: tc.did}
		err := p.Validate(context.Background(), env)
		srv.Close()
		wantStatus(t, err, tc.status)
		if tc.status == 0 {
			if !called {
				t.Errorf("%s: FOXDEN was not asked", tc.name)
			}
			if auth != "Bearer "+tok {
				t.Errorf("%s: Authorization = %q", tc.name, auth)
			}
			want := foxdenRequest{User: "some-user", Scope: "read", Roles: []string{}, Did: tc.did, File: "/nfs/a.nxs",
				Btrs: []string{"test-demo", "test-987-b", "test-123-a"}}
			wj, _ := json.Marshal(want)
			gj, _ := json.Marshal(got)
			if string(wj) != string(gj) {
				t.Errorf("%s: FOXDEN got %s, want %s", tc.name, gj, wj)
			}
		}
		if tc.name == "invalid token never reaches foxden" || tc.name == "no read scope never reaches foxden" || tc.name == "missing did" {
			if called {
				t.Errorf("%s: FOXDEN was called", tc.name)
			}
		}
	}
}

func TestFoxdenUnreachableOrUnconfigured(t *testing.T) {
	tok := unsigned(t, sampleClaims())
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	env := testEnv()
	env.FoxdenURL = url
	wantStatus(t, (&FoxdenViewPayload{File: "a", Token: tok, Did: "d"}).Validate(context.Background(), env), 502)

	env.FoxdenURL = ""
	wantStatus(t, (&FoxdenViewPayload{File: "a", Token: tok, Did: "d"}).Validate(context.Background(), env), 500)
}

func TestPayloadRegistry(t *testing.T) {
	if p, err := newPayload(""); err != nil || p == nil {
		t.Fatalf("default payload: %v", err)
	} else if _, ok := p.(*ViewPayload); !ok {
		t.Errorf("default payload is %T", p)
	}
	if p, err := newPayload("FOXDEN"); err != nil {
		t.Fatal(err)
	} else if _, ok := p.(*FoxdenViewPayload); !ok {
		t.Errorf("foxden payload is %T", p)
	}
	if _, err := newPayload("nope"); err == nil || !strings.Contains(err.Error(), "default") {
		t.Errorf("unknown payload: err = %v", err)
	}
}

func TestValidateViewAuthConfig(t *testing.T) {
	if err := validateViewAuth(&Config{ViewPayloadType: "foxden"}); err == nil {
		t.Error("foxden without foxden_url accepted")
	}
	if err := validateViewAuth(&Config{ViewPayloadType: "foxden", FoxdenURL: "http://f"}); err != nil {
		t.Error(err)
	}
	if err := validateViewAuth(&Config{ViewPayloadType: "bogus"}); err == nil {
		t.Error("unknown payload accepted")
	}
	cfg := &Config{}
	if err := validateViewAuth(cfg); err != nil || cfg.ViewPayloadType != "default" || cfg.FoxdenTimeoutSeconds != 10 {
		t.Errorf("defaults: %v %+v", err, cfg)
	}
}

// customPayload is the example from AUTH.md.
type customPayload struct {
	File  string `json:"file"`
	Token string `json:"token"`
	Group string `json:"group"`
}

func (p *customPayload) GetFile() string     { return p.File }
func (p *customPayload) GetToken() string    { return p.Token }
func (p *customPayload) SetToken(tok string) { p.Token = tok }
func (p *customPayload) Validate(ctx context.Context, env *AuthEnv) error {
	claims, err := env.authorizeRead(p.Token)
	if err != nil {
		return err
	}
	for _, g := range claims.Custom.Groups {
		if g == p.Group {
			return nil
		}
	}
	return errors.New("not a member of group " + p.Group) // plain error: reported as 403
}

// --- handler level: payload decoding, header tokens, status mapping --------

func viewRequest(body, bearer string) *http.Request {
	req := httptest.NewRequest("POST", "/view", strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req
}

// missingStore is a file store without files. Validation redirects without
// touching the store, so the handler must not need one.
type missingStore struct{}

func (missingStore) List(context.Context, string) ([]FileInfo, error) { return nil, nil }
func (missingStore) Ensure(context.Context, string) (string, error) {
	return "", notFound("no such file")
}
func (missingStore) SetOnFileChanged(func(string)) {}

// A request that validates is redirected to the UI.
func TestHandleViewFlow(t *testing.T) {
	tok := unsigned(t, sampleClaims())
	noRead := unsigned(t, withCustom(withCustom(sampleClaims(), "scope", "write"), "scopes", []string{"write"}))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	mkServer := func(kind string) *server {
		env := testEnv()
		env.FoxdenURL = srv.URL
		env.HTTP = srv.Client()
		return &server{cfg: &Config{ViewPayloadType: kind}, auth: env, store: missingStore{}}
	}
	RegisterPayload("test-custom", func() UserPayload { return &customPayload{} })

	cases := []struct {
		name string
		kind string
		body string
		hdr  string
		want int // 303 = validated, redirected to the UI
	}{
		{"token in header", "default", `{"file":"a.nxs"}`, tok, 303},
		{"token in body", "default", `{"file":"a.nxs","token":"` + tok + `"}`, "", 303},
		{"header wins over body", "default", `{"file":"a.nxs","token":"junk"}`, tok, 303},
		{"header wins, bad header", "default", `{"file":"a.nxs","token":"` + tok + `"}`, "junk", 401},
		{"no token", "default", `{"file":"a.nxs"}`, "", 401},
		{"no read scope", "default", `{"file":"a.nxs"}`, noRead, 403},
		{"bad json", "default", `{`, tok, 400},
		{"missing file", "default", `{"token":"` + tok + `"}`, "", 400},
		{"foxden ok", "foxden", `{"file":"a.nxs","did":"d1"}`, tok, 303},
		{"foxden without did", "foxden", `{"file":"a.nxs"}`, tok, 400},
		{"default ignores did", "default", `{"file":"a.nxs","did":"d1"}`, tok, 303},
		{"custom ok", "test-custom", `{"file":"a.nxs","group":"group-b"}`, tok, 303},
		{"custom plain error is 403", "test-custom", `{"file":"a.nxs","group":"zzz"}`, tok, 403},
		{"oversized body", "default", `{"file":"` + strings.Repeat("a", maxViewBody) + `"}`, tok, 400},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		mkServer(tc.kind).handleView(rec, viewRequest(tc.body, tc.hdr))
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body.String())
		}
		if rec.Code == 303 {
			if loc := rec.Header().Get("Location"); loc != "/ui/#key=a.nxs" {
				t.Errorf("%s: Location = %q, want /ui/#key=a.nxs", tc.name, loc)
			}
		}
	}
}

func TestHandleViewWithoutAuthEnvFailsClosed(t *testing.T) {
	s := &server{cfg: &Config{}}
	rec := httptest.NewRecorder()
	s.handleView(rec, viewRequest(`{"file":"a.nxs"}`, unsigned(t, sampleClaims())))
	if rec.Code != 500 {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func formRequest(path, body, bearer string) *http.Request {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req
}

func TestHandleViewForm(t *testing.T) {
	tok := unsigned(t, sampleClaims())
	noRead := unsigned(t, withCustom(withCustom(sampleClaims(), "scope", "write"), "scopes", []string{"write"}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	mk := func(kind string) *server {
		env := testEnv()
		env.FoxdenURL, env.HTTP = srv.URL, srv.Client()
		return &server{cfg: &Config{ViewPayloadType: kind}, auth: env, store: missingStore{}}
	}
	RegisterPayload("test-custom", func() UserPayload { return &customPayload{} })

	cases := []struct {
		name, kind, path, body, hdr string
		want                        int // 303 = validated, redirected to the UI
	}{
		{"valid", "default", "/viewform", "file=a.nxs&token=" + tok, "", 303},
		{"extra fields ignored", "default", "/viewform", "file=a.nxs&token=" + tok + "&x=1", "", 303},
		{"header token is not used", "default", "/viewform", "file=a.nxs", tok, 401},
		{"token in the URL is not used", "default", "/viewform?token=" + tok, "file=a.nxs", "", 401},
		{"no read scope", "default", "/viewform", "file=a.nxs&token=" + noRead, "", 403},
		{"missing file", "default", "/viewform", "token=" + tok, "", 400},
		{"foxden ok", "foxden", "/viewform", "file=a.nxs&did=d1&token=" + tok, "", 303},
		{"foxden without did", "foxden", "/viewform", "file=a.nxs&token=" + tok, "", 400},
		{"custom payload", "test-custom", "/viewform", "file=a.nxs&group=group-a&token=" + tok, "", 303},
		{"custom payload denied", "test-custom", "/viewform", "file=a.nxs&group=zzz&token=" + tok, "", 403},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		mk(tc.kind).handleViewForm(rec, formRequest(tc.path, tc.body, tc.hdr))
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body.String())
		}
		if rec.Code == 303 {
			if loc := rec.Header().Get("Location"); loc != "/ui/#key=a.nxs" {
				t.Errorf("%s: Location = %q, want /ui/#key=a.nxs", tc.name, loc)
			}
		}
	}

	// multipart/form-data works as well
	var b strings.Builder
	mw := multipart.NewWriter(&b)
	_ = mw.WriteField("file", "a.nxs")
	_ = mw.WriteField("token", tok)
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/viewform", strings.NewReader(b.String()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	mk("default").handleViewForm(rec, req)
	if rec.Code != 303 {
		t.Errorf("multipart: status = %d, want 303 (%s)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mk("default").handleViewForm(rec, httptest.NewRequest("GET", "/viewform", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status = %d, want 405", rec.Code)
	}
}
