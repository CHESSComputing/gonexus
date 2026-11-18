package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func viewLocation(t *testing.T, cfg *Config, query url.Values) string {
	t.Helper()
	s := &server{cfg: cfg}
	req := httptest.NewRequest("GET", "/view?"+query.Encode(), nil)
	rec := httptest.NewRecorder()
	s.handleView(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	return rec.Header().Get("Location")
}

func TestHandleViewRedirectsToUI(t *testing.T) {
	cases := []struct {
		name  string
		cfg   *Config
		query url.Values
		want  string
	}{
		{"key", &Config{}, url.Values{"file": {"chess/results.nxs"}},
			"/ui/#key=chess%2Fresults.nxs"},
		{"leading slash is dropped", &Config{}, url.Values{"file": {"/chess/results.nxs"}},
			"/ui/#key=chess%2Fresults.nxs"},
		{"select a node", &Config{}, url.Values{"file": {"chess/results.nxs"}, "path": {"/saxs_azimuthal/data/I"}},
			"/ui/#key=chess%2Fresults.nxs&path=saxs_azimuthal%2Fdata%2FI"},
		{"absolute path below data_dir becomes the key",
			&Config{DataDir: "/data/nexus"}, url.Values{"file": {"/data/nexus/chess/RE3/x.nxs"}},
			"/ui/#key=chess%2FRE3%2Fx.nxs"},
		{"absolute path outside data_dir is treated as a key",
			&Config{DataDir: "/data/nexus"}, url.Values{"file": {"/other/x.nxs"}},
			"/ui/#key=other%2Fx.nxs"},
		{"data_dir itself is not a file",
			&Config{DataDir: "/data/nexus"}, url.Values{"file": {"/data/nexus"}},
			"/ui/#key=data%2Fnexus"},
		// A path that leaves data_dir is not rewritten into one that stays
		// inside: the ".." is kept, and the store rejects such keys.
		{"dot-dot is left for the store to reject",
			&Config{DataDir: "/data/nexus"}, url.Values{"file": {"/data/nexus/../secret.nxs"}},
			"/ui/#key=data%2Fnexus%2F..%2Fsecret.nxs"},
		{"spaces and unicode are escaped",
			&Config{}, url.Values{"file": {"chess/my file é.nxs"}},
			"/ui/#key=chess%2Fmy+file+%C3%A9.nxs"},
		{"no file goes to the UI home", &Config{}, url.Values{},
			"/ui/"},
		{"blank file goes to the UI home", &Config{}, url.Values{"file": {"   "}},
			"/ui/"},
	}
	for _, tc := range cases {
		if got := viewLocation(t, tc.cfg, tc.query); got != tc.want {
			t.Errorf("%s: Location = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestViewRouteIsRegistered(t *testing.T) {
	s := &server{cfg: &Config{}}
	mux := s.routes()
	for _, path := range []string{"/healthz", "/view?file=a.nxs"} {
		req := httptest.NewRequest("GET", path, nil)
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("%s is not routed", path)
		}
	}
}
