package main

// View sessions: in file_system_mode the data endpoints (/tree, /entries,
// /group, /field) can only be used by a client that first passed /view or
// /viewform for that file, and /files is switched off (see AUTH.md).
//
// A successful /view or /viewform answers with a cookie that lists the files
// the client may read, each with an expiry, signed with a key that only this
// server process knows (a restart ends all sessions). The browser UI, which
// /view redirects to, sends the cookie with its requests unchanged. A request
// for a file that is not in a valid cookie is refused with 403.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	sessionCookie = "gonexus_view"
	maxGrants     = 10 // most files one cookie can hold; the oldest are dropped
)

// viewSessions issues and checks the cookies.
type viewSessions struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time // time.Now unless a test sets it
}

func newViewSessions(ttl time.Duration) (*viewSessions, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return &viewSessions{secret: secret, ttl: ttl}, nil
}

func (v *viewSessions) time() time.Time {
	if v.now != nil {
		return v.now()
	}
	return time.Now()
}

func (v *viewSessions) mac(data string) []byte {
	m := hmac.New(sha256.New, v.secret)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// grants maps a file key to the Unix time its permission ends.
type grants map[string]int64

// read returns the unexpired grants of the request's cookie; none if the
// cookie is absent, malformed or not signed by this server.
func (v *viewSessions) read(r *http.Request) grants {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	body, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return nil
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, v.mac(body)) {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil
	}
	var g grants
	if json.Unmarshal(raw, &g) != nil {
		return nil
	}
	now := v.time().Unix()
	for k, exp := range g {
		if exp <= now {
			delete(g, k)
		}
	}
	return g
}

// allowed reports whether the request's cookie grants access to key.
func (v *viewSessions) allowed(r *http.Request, key string) bool {
	if v == nil || key == "" {
		return false
	}
	_, ok := v.read(r)[key]
	return ok
}

// grant adds key to the grants the client already holds and sets the cookie.
func (v *viewSessions) grant(w http.ResponseWriter, r *http.Request, key string) {
	g := v.read(r)
	if g == nil {
		g = grants{}
	}
	g[key] = v.time().Add(v.ttl).Unix()
	for len(g) > maxGrants { // drop the ones that end first
		keys := make([]string, 0, len(g))
		for k := range g {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return g[keys[i]] < g[keys[j]] })
		delete(g, keys[0])
	}
	raw, _ := json.Marshal(g)
	body := base64.RawURLEncoding.EncodeToString(raw)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    body + "." + base64.RawURLEncoding.EncodeToString(v.mac(body)),
		Path:     "/",
		MaxAge:   int(v.ttl / time.Second),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

// guardData wraps a handler that takes a file ?key=. In file_system_mode the
// request must carry a view session for that key. (Without a key the handler
// itself answers 400, which reveals nothing.)
func (s *server) guardData(h http.HandlerFunc) http.HandlerFunc {
	if !s.cfg.FileSystemMode {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if key := r.URL.Query().Get("key"); key != "" && !s.sessions.allowed(r, key) {
			writeErr(w, forbidden("in file_system_mode files can only be opened through /view or /viewform"))
			return
		}
		h(w, r)
	}
}

// guardListing switches /files off in file_system_mode: it would list file
// names to anyone, with no way to tie the request to a file.
func (s *server) guardListing(h http.HandlerFunc) http.HandlerFunc {
	if !s.cfg.FileSystemMode {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, forbidden("listing files is disabled in file_system_mode; open a file through /view or /viewform"))
	}
}
