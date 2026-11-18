package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestViewKey checks how the file of a /view payload becomes a file key.
func TestViewKey(t *testing.T) {
	cases := []struct {
		name string
		cfg  *Config
		file string
		want string
	}{
		{"key", &Config{}, "chess/results.nxs", "chess/results.nxs"},
		{"leading slash is dropped", &Config{}, "/chess/results.nxs", "chess/results.nxs"},
		{"absolute path below data_dir becomes the key",
			&Config{DataDir: "/data/nexus"}, "/data/nexus/chess/RE3/x.nxs", "chess/RE3/x.nxs"},
		{"absolute path outside data_dir is treated as a key",
			&Config{DataDir: "/data/nexus"}, "/other/x.nxs", "other/x.nxs"},
		{"data_dir itself is not a file",
			&Config{DataDir: "/data/nexus"}, "/data/nexus", "data/nexus"},
		// A path that leaves data_dir is not rewritten into one that stays
		// inside: the ".." is kept, and the store rejects such keys.
		{"dot-dot is left for the store to reject",
			&Config{DataDir: "/data/nexus"}, "/data/nexus/../secret.nxs", "data/nexus/../secret.nxs"},
		{"filesystem mode keeps the absolute path",
			&Config{FileSystemMode: true}, "/nfs/chess/x.nxs", "/nfs/chess/x.nxs"},
		{"filesystem mode accepts a file URL",
			&Config{FileSystemMode: true}, "file:///nfs/chess/x.nxs", "/nfs/chess/x.nxs"},
		{"filesystem mode does not rewrite dot-dot",
			&Config{FileSystemMode: true}, "/nfs/../etc/x.nxs", "/nfs/../etc/x.nxs"},
	}
	for _, tc := range cases {
		s := &server{cfg: tc.cfg}
		if got := s.viewKey(tc.file); got != tc.want {
			t.Errorf("%s: viewKey(%q) = %q, want %q", tc.name, tc.file, got, tc.want)
		}
	}
}

func TestViewRouteIsRegistered(t *testing.T) {
	s := &server{cfg: &Config{}}
	mux := s.routes()
	for _, path := range []string{"/healthz", "/view", "/viewform"} {
		req := httptest.NewRequest("POST", path, nil)
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("%s is not routed", path)
		}
	}
}

func TestViewRejectsOtherMethods(t *testing.T) {
	s := &server{cfg: &Config{}}
	for _, m := range []string{"GET", "PUT", "DELETE"} {
		rec := httptest.NewRecorder()
		s.handleView(rec, httptest.NewRequest(m, "/view?file=a.nxs", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /view: status = %d, want 405", m, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "POST" {
			t.Errorf("%s /view: Allow = %q, want POST", m, got)
		}
	}
}

func TestViewUnauthorizedSetsChallenge(t *testing.T) {
	s := &server{cfg: &Config{}, auth: &AuthEnv{}}
	req := httptest.NewRequest("POST", "/view", strings.NewReader(`{"file":"a.nxs"}`))
	rec := httptest.NewRecorder()
	s.handleView(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("401 without a WWW-Authenticate header")
	}
}
