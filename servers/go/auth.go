package main

// Token handling and payload validation for POST /view (see AUTH.md).
//
// /view reads a request body (a "payload") that names the file to open and
// carries an access token. The payload is decoded into a struct that
// implements UserPayload; which struct is used is a configuration choice
// (view_payload, see payloadRegistry below), so other deployments can add their
// own without touching the handler.
//
// Tokens are JWTs issued by an authorization server, with the claims
//
//	{"iss":..., "sub":..., "aud":[...], "exp":..., "iat":...,
//	 "custom_claims": {"user":..., "scope":"read", "kind":..., "application":...,
//	                   "roles":[...], "btrs":[...], "groups":[...], "scopes":["read",...]}}
//
// ViewPayload only checks the token itself; FoxdenViewPayload additionally asks
// FOXDEN whether the user may access a given dataset id (did).

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// ReadScope is the scope a token must carry to read files through /view.
const ReadScope = "read"

// ---------------------------------------------------------------------------
// UserPayload and its implementations
// ---------------------------------------------------------------------------

// UserPayload is the body of a POST /view request. Implement it to support
// another kind of payload and register it with RegisterPayload.
//
// Validate returns nil if the request may proceed and an error otherwise. An
// *apiError (see errors.go) chooses the HTTP status; any other error is
// reported as 403 Forbidden.
type UserPayload interface {
	// GetFile returns the file the caller wants to open.
	GetFile() string
	// GetToken returns the token carried by the payload.
	GetToken() string
	// SetToken sets the token; the handler uses it to pass on a token that
	// arrived in the HTTP header instead of in the body.
	SetToken(token string)
	// Validate decides whether the payload is acceptable.
	Validate(ctx context.Context, env *AuthEnv) error
}

// ViewPayload is the default payload. It validates the token only.
type ViewPayload struct {
	File  string `json:"file"`
	Token string `json:"token"`
}

func (p *ViewPayload) GetFile() string     { return p.File }
func (p *ViewPayload) GetToken() string    { return p.Token }
func (p *ViewPayload) SetToken(tok string) { p.Token = tok }

// Validate accepts the request if the token is valid (signature if a key is
// configured, and not expired) and grants the read scope.
func (p *ViewPayload) Validate(ctx context.Context, env *AuthEnv) error {
	_, err := env.authorizeRead(p.Token)
	return err
}

// FoxdenViewPayload is the payload for deployments that run next to FOXDEN. It
// validates the token like ViewPayload and then asks FOXDEN whether the user
// may access the dataset did.
type FoxdenViewPayload struct {
	File  string `json:"file"`
	Token string `json:"token"`
	Did   string `json:"did"`
}

func (p *FoxdenViewPayload) GetFile() string     { return p.File }
func (p *FoxdenViewPayload) GetToken() string    { return p.Token }
func (p *FoxdenViewPayload) SetToken(tok string) { p.Token = tok }

// foxdenRequest is the body sent to FOXDEN (see foxdenCheck).
type foxdenRequest struct {
	User  string   `json:"user"`
	Scope string   `json:"scope"`
	Roles []string `json:"roles"`
	Btrs  []string `json:"btrs"`
	Did   string   `json:"did"`
	File  string   `json:"file"`
}

// Validate checks the token, then asks FOXDEN about the user's access to did.
func (p *FoxdenViewPayload) Validate(ctx context.Context, env *AuthEnv) error {
	claims, err := env.authorizeRead(p.Token)
	if err != nil {
		return err
	}
	if strings.TrimSpace(p.Did) == "" {
		return badRequest("missing required field 'did'")
	}
	cc := claims.Custom
	return env.foxdenCheck(ctx, p.Token, foxdenRequest{
		User:  cc.User,
		Scope: cc.Scope,
		Roles: nonNil(cc.Roles),
		Btrs:  nonNil(cc.Btrs),
		Did:   p.Did,
		File:  p.File,
	})
}

// payloadRegistry maps the view_payload setting to a constructor of an empty
// payload. Add entries with RegisterPayload, typically from an init function
// in a file of your own (see AUTH.md).
var payloadRegistry = map[string]func() UserPayload{
	"default": func() UserPayload { return &ViewPayload{} },
	"foxden":  func() UserPayload { return &FoxdenViewPayload{} },
}

// RegisterPayload makes a payload type selectable through view_payload.
func RegisterPayload(name string, newPayload func() UserPayload) {
	payloadRegistry[strings.ToLower(name)] = newPayload
}

// newPayload returns an empty payload of the kind called name ("" is "default").
func newPayload(name string) (UserPayload, error) {
	if name == "" {
		name = "default"
	}
	f, ok := payloadRegistry[strings.ToLower(name)]
	if !ok {
		return nil, fmt.Errorf("unknown view_payload %q (known: %s)", name, strings.Join(payloadNames(), ", "))
	}
	return f(), nil
}

func payloadNames() []string {
	names := make([]string, 0, len(payloadRegistry))
	for n := range payloadRegistry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---------------------------------------------------------------------------
// AuthEnv: what Validate functions may use
// ---------------------------------------------------------------------------

// AuthEnv carries the settings and helpers that Validate functions need. One
// is built at start-up from the configuration.
type AuthEnv struct {
	Verifier  *TokenVerifier // checks token signatures; nil = signatures are not checked
	FoxdenURL string         // endpoint of the FOXDEN access check
	HTTP      *http.Client   // used for requests to FOXDEN
	Now       func() time.Time
}

// newAuthEnv builds the AuthEnv for cfg.
func newAuthEnv(cfg *Config) (*AuthEnv, error) {
	v, err := newTokenVerifier(cfg.TokenSecret, cfg.TokenPublicKeyFile)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(cfg.FoxdenTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &AuthEnv{
		Verifier:  v,
		FoxdenURL: cfg.FoxdenURL,
		HTTP:      &http.Client{Timeout: timeout},
		Now:       time.Now,
	}, nil
}

func (e *AuthEnv) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// ParseToken verifies and decodes a token: well-formed, signature valid (when a
// key is configured) and not expired. It does not look at scopes.
func (e *AuthEnv) ParseToken(token string) (*TokenClaims, error) {
	if strings.TrimSpace(token) == "" {
		return nil, unauthorized("missing token")
	}
	claims, err := e.Verifier.Parse(token)
	if err != nil {
		return nil, unauthorized("invalid token: %v", err)
	}
	now := e.now().Unix()
	if claims.Exp == 0 {
		return nil, unauthorized("invalid token: no expiration time")
	}
	if now >= int64(claims.Exp) {
		return nil, unauthorized("token expired")
	}
	if claims.Nbf != 0 && now < int64(claims.Nbf) {
		return nil, unauthorized("token is not valid yet")
	}
	return claims, nil
}

// authorizeRead is ParseToken plus the requirement that the token grants the
// read scope. It is the check shared by the built-in payloads.
func (e *AuthEnv) authorizeRead(token string) (*TokenClaims, error) {
	claims, err := e.ParseToken(token)
	if err != nil {
		return nil, err
	}
	if !claims.Custom.HasScope(ReadScope) {
		return nil, forbidden("token does not grant the %q scope", ReadScope)
	}
	return claims, nil
}

// foxdenCheck asks FOXDEN whether the request may proceed.
//
// The exchange (adjust it here if your FOXDEN differs): a POST of the JSON
// foxdenRequest to FoxdenURL with the user's token as "Authorization: Bearer".
// Any 2xx answer means access is granted; 401 means FOXDEN rejects the token;
// 403 (or 404, an unknown dataset) means the user may not access the did; any
// other outcome, including no answer, is a 502 and denies access.
func (e *AuthEnv) foxdenCheck(ctx context.Context, token string, in foxdenRequest) error {
	if e.FoxdenURL == "" {
		return internal("foxden_url is not configured")
	}
	body, err := json.Marshal(in)
	if err != nil {
		return internal("encoding FOXDEN request: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.FoxdenURL, bytes.NewReader(body))
	if err != nil {
		return internal("building FOXDEN request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	client := e.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		// err can contain the URL but never the token (it is only in a header).
		log.Printf("FOXDEN access check failed: %v", err)
		return &apiError{status: http.StatusBadGateway, msg: "FOXDEN is not reachable"}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized:
		return unauthorized("FOXDEN rejected the token")
	case resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusNotFound:
		return forbidden("access to dataset %q is not allowed", in.Did)
	}
	log.Printf("FOXDEN access check: unexpected status %d", resp.StatusCode)
	return &apiError{status: http.StatusBadGateway, msg: fmt.Sprintf("FOXDEN access check failed (status %d)", resp.StatusCode)}
}

// ---------------------------------------------------------------------------
// Claims
// ---------------------------------------------------------------------------

// TokenClaims are the claims of a token.
type TokenClaims struct {
	Iss    string       `json:"iss"`
	Sub    string       `json:"sub"`
	Aud    audience     `json:"aud"`
	Exp    numericDate  `json:"exp"`
	Iat    numericDate  `json:"iat"`
	Nbf    numericDate  `json:"nbf"`
	Custom CustomClaims `json:"custom_claims"`
}

// CustomClaims are the application specific claims.
type CustomClaims struct {
	User        string   `json:"user"`
	Scope       string   `json:"scope"`
	Kind        string   `json:"kind"`
	Application string   `json:"application"`
	Roles       []string `json:"roles"` // may be null
	Btrs        []string `json:"btrs"`
	Groups      []string `json:"groups"`
	Scopes      []string `json:"scopes"`
}

// HasScope reports whether the token grants scope, either as its single
// "scope" or in its "scopes" list.
func (c CustomClaims) HasScope(scope string) bool {
	if c.Scope == scope {
		return true
	}
	for _, s := range c.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// numericDate is a JWT NumericDate: seconds since the epoch, possibly written
// as a fractional number.
type numericDate int64

func (n *numericDate) UnmarshalJSON(b []byte) error {
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	*n = numericDate(f)
	return nil
}

// audience is the "aud" claim, which is a string or a list of strings.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = audience(many)
	return nil
}

// ---------------------------------------------------------------------------
// Token parsing and signature verification
// ---------------------------------------------------------------------------

// TokenVerifier decodes tokens and checks their signature. The algorithm is
// decided by the keys that are configured, never by what the token claims, and
// "none" is never accepted. A nil *TokenVerifier (or one without keys) decodes
// tokens WITHOUT checking the signature: anyone could then forge one.
type TokenVerifier struct {
	hmacSecret []byte         // HS256
	rsaKey     *rsa.PublicKey // RS256
}

// newTokenVerifier returns nil if neither key is configured.
func newTokenVerifier(secret, publicKeyFile string) (*TokenVerifier, error) {
	if secret == "" && publicKeyFile == "" {
		return nil, nil
	}
	v := &TokenVerifier{}
	if secret != "" {
		v.hmacSecret = []byte(secret)
	}
	if publicKeyFile != "" {
		data, err := os.ReadFile(publicKeyFile)
		if err != nil {
			return nil, fmt.Errorf("token_public_key_file: %w", err)
		}
		key, err := parseRSAPublicKey(data)
		if err != nil {
			return nil, fmt.Errorf("token_public_key_file %s: %w", publicKeyFile, err)
		}
		v.rsaKey = key
	}
	return v, nil
}

// parseRSAPublicKey reads a PEM public key (PKIX or PKCS#1) or certificate.
func parseRSAPublicKey(data []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("not PEM encoded")
	}
	var pub interface{}
	var err error
	switch block.Type {
	case "CERTIFICATE":
		var cert *x509.Certificate
		if cert, err = x509.ParseCertificate(block.Bytes); err == nil {
			pub = cert.PublicKey
		}
	case "RSA PUBLIC KEY":
		pub, err = x509.ParsePKCS1PublicKey(block.Bytes)
	default:
		pub, err = x509.ParsePKIXPublicKey(block.Bytes)
	}
	if err != nil {
		return nil, err
	}
	key, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("not an RSA public key")
	}
	return key, nil
}

// Parse decodes token and, when keys are configured, verifies its signature.
// It does not check any claim.
func (v *TokenVerifier) Parse(token string) (*TokenClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a JWT")
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if err := decodeSegment(parts[0], &header); err != nil {
		return nil, fmt.Errorf("bad header: %w", err)
	}
	if v != nil {
		if err := v.verify(header.Alg, parts[0]+"."+parts[1], parts[2]); err != nil {
			return nil, err
		}
	}
	claims := &TokenClaims{}
	if err := decodeSegment(parts[1], claims); err != nil {
		return nil, fmt.Errorf("bad claims: %w", err)
	}
	return claims, nil
}

func (v *TokenVerifier) verify(alg, signed, sigSegment string) error {
	sig, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(sigSegment, "="))
	if err != nil {
		return errors.New("bad signature encoding")
	}
	switch alg {
	case "HS256":
		if v.hmacSecret == nil {
			return errors.New("unsupported algorithm HS256")
		}
		mac := hmac.New(sha256.New, v.hmacSecret)
		mac.Write([]byte(signed))
		if !hmac.Equal(sig, mac.Sum(nil)) {
			return errors.New("bad signature")
		}
		return nil
	case "RS256":
		if v.rsaKey == nil {
			return errors.New("unsupported algorithm RS256")
		}
		sum := sha256.Sum256([]byte(signed))
		if err := rsa.VerifyPKCS1v15(v.rsaKey, crypto.SHA256, sum[:], sig); err != nil {
			return errors.New("bad signature")
		}
		return nil
	}
	return fmt.Errorf("unsupported algorithm %q", alg)
}

func decodeSegment(seg string, out interface{}) error {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(seg, "="))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// bearerToken extracts the token of an "Authorization: Bearer <token>" header.
func bearerToken(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}
