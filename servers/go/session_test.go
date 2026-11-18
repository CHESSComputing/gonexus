package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fsServer is a file_system_mode server whose store has no files: a request
// that gets 404 passed the session check, one that gets 403 was stopped by it.
func fsServer(t *testing.T) (*server, *http.ServeMux) {
	t.Helper()
	sess, err := newViewSessions(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	env := testEnv()
	s := &server{cfg: &Config{FileSystemMode: true}, auth: env, store: missingStore{}, sessions: sess}
	return s, s.routes()
}

func serve(mux *http.ServeMux, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func getData(path, key string, cookies ...*http.Cookie) *http.Request {
	req := httptest.NewRequest("GET", path+"?key="+url.QueryEscape(key), nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return req
}

// viewCookie posts a valid /view for file, optionally carrying earlier cookies.
func viewCookie(t *testing.T, mux *http.ServeMux, file string, prior ...*http.Cookie) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest("POST", "/view", strings.NewReader(`{"file":"`+file+`"}`))
	req.Header.Set("Authorization", "Bearer "+unsigned(t, sampleClaims()))
	for _, c := range prior {
		req.AddCookie(c)
	}
	rec := serve(mux, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("/view %s: status = %d (%s)", file, rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			if !c.HttpOnly || c.MaxAge != 3600 {
				t.Errorf("cookie flags: %+v", c)
			}
			return c
		}
	}
	t.Fatal("/view set no session cookie")
	return nil
}

func TestFileSystemModeDataNeedsViewSession(t *testing.T) {
	_, mux := fsServer(t)
	file := "/nfs/chess/a.nxs"
	for _, ep := range []string{"/tree", "/entries", "/group", "/field"} {
		if rec := serve(mux, getData(ep, file)); rec.Code != 403 {
			t.Errorf("%s without a session: status = %d, want 403", ep, rec.Code)
		}
	}
	c := viewCookie(t, mux, file)
	for _, ep := range []string{"/tree", "/entries", "/group", "/field"} {
		if rec := serve(mux, getData(ep, file, c)); rec.Code != 404 {
			t.Errorf("%s with a session: status = %d, want 404 (store reached)", ep, rec.Code)
		}
		if rec := serve(mux, getData(ep, "/nfs/chess/other.nxs", c)); rec.Code != 403 {
			t.Errorf("%s for another file: status = %d, want 403", ep, rec.Code)
		}
	}
	// no key: the handler's own 400, nothing revealed
	if rec := serve(mux, httptest.NewRequest("GET", "/tree", nil)); rec.Code != 400 {
		t.Errorf("/tree without key: status = %d, want 400", rec.Code)
	}
}

func TestViewFormGrantsToo(t *testing.T) {
	_, mux := fsServer(t)
	body := url.Values{"file": {"/nfs/a.nxs"}, "token": {unsigned(t, sampleClaims())}}.Encode()
	rec := serve(mux, formRequest("/viewform", body, ""))
	if rec.Code != 303 {
		t.Fatalf("status = %d", rec.Code)
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 {
		t.Fatalf("cookies = %v", cs)
	}
	if rec := serve(mux, getData("/tree", "/nfs/a.nxs", cs[0])); rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestFailedViewGrantsNothing(t *testing.T) {
	_, mux := fsServer(t)
	req := httptest.NewRequest("POST", "/view", strings.NewReader(`{"file":"/nfs/a.nxs","token":"junk"}`))
	rec := serve(mux, req)
	if rec.Code != 401 || len(rec.Result().Cookies()) != 0 {
		t.Errorf("status = %d, cookies = %v", rec.Code, rec.Result().Cookies())
	}
}

func TestSessionCookieIsChecked(t *testing.T) {
	s, mux := fsServer(t)
	file := "/nfs/a.nxs"
	c := viewCookie(t, mux, file)

	// tampered: another file's grant under the old signature
	body, sig, _ := strings.Cut(c.Value, ".")
	forged := &http.Cookie{Name: sessionCookie, Value: body[:len(body)-2] + "AA." + sig}
	if rec := serve(mux, getData("/tree", file, forged)); rec.Code != 403 {
		t.Errorf("tampered cookie: status = %d, want 403", rec.Code)
	}
	for name, v := range map[string]string{"garbage": "x", "no signature": body, "empty": ""} {
		if rec := serve(mux, getData("/tree", file, &http.Cookie{Name: sessionCookie, Value: v})); rec.Code != 403 {
			t.Errorf("%s cookie: status = %d, want 403", name, rec.Code)
		}
	}

	// signed by another server process
	_, mux2 := fsServer(t)
	if rec := serve(mux2, getData("/tree", file, c)); rec.Code != 403 {
		t.Errorf("cookie of another server: status = %d, want 403", rec.Code)
	}

	// expired
	s.sessions.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if rec := serve(mux, getData("/tree", file, c)); rec.Code != 403 {
		t.Errorf("expired cookie: status = %d, want 403", rec.Code)
	}
}

func TestSessionHoldsSeveralFilesAndIsBounded(t *testing.T) {
	_, mux := fsServer(t)
	c := viewCookie(t, mux, "/nfs/a.nxs")
	c = viewCookie(t, mux, "/nfs/b.nxs", c)
	for _, f := range []string{"/nfs/a.nxs", "/nfs/b.nxs"} {
		if rec := serve(mux, getData("/tree", f, c)); rec.Code != 404 {
			t.Errorf("%s: status = %d, want 404", f, rec.Code)
		}
	}
	for i := 0; i < maxGrants+3; i++ {
		c = viewCookie(t, mux, "/nfs/f"+string(rune('a'+i))+".nxs", c)
	}
	// the newest is still allowed, and the cookie did not grow without bound
	if rec := serve(mux, getData("/tree", "/nfs/f"+string(rune('a'+maxGrants+2))+".nxs", c)); rec.Code != 404 {
		t.Errorf("newest file: status = %d, want 404", rec.Code)
	}
	if n := len(c.Value); n > 3000 {
		t.Errorf("cookie is %d bytes", n)
	}
}

func TestFileSystemModeDisablesListing(t *testing.T) {
	_, mux := fsServer(t)
	c := viewCookie(t, mux, "/nfs/a.nxs")
	req := httptest.NewRequest("GET", "/files?prefix=/nfs/", nil)
	req.AddCookie(c)
	if rec := serve(mux, req); rec.Code != 403 {
		t.Errorf("/files: status = %d, want 403", rec.Code)
	}
}

func TestOtherModesAreUnchanged(t *testing.T) {
	s := &server{cfg: &Config{}, store: missingStore{}}
	mux := s.routes()
	if rec := serve(mux, getData("/tree", "a.nxs")); rec.Code != 404 {
		t.Errorf("/tree: status = %d, want 404 (no session needed)", rec.Code)
	}
	if rec := serve(mux, httptest.NewRequest("GET", "/files", nil)); rec.Code != 200 {
		t.Errorf("/files: status = %d, want 200", rec.Code)
	}
}

func TestFileSystemModeWithoutSessionsFailsClosed(t *testing.T) {
	s := &server{cfg: &Config{FileSystemMode: true}, auth: testEnv(), store: missingStore{}}
	if rec := serve(s.routes(), getData("/tree", "/nfs/a.nxs")); rec.Code != 403 {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}
