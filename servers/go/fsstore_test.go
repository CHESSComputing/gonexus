package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func fsWrite(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fsLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}
}

func statusOf(err error) int {
	if ae, ok := err.(*apiError); ok {
		return ae.status
	}
	return 0
}

// fixture, with base = a temp dir:
//
//	base/allowed/a/scan1.nxs, a/notes.txt, a/sub/deep.h5     (the allowed area)
//	base/allowed/.hidden/h.nxs                               (hidden dir: never listed)
//	base/allowed/link_in   -> allowed/a                      (link that stays inside)
//	base/allowed/escape    -> base/outside                   (directory link leaving the area)
//	base/allowed/escfile.nxs -> base/outside/secret.nxs      (file link leaving the area)
//	base/allowed/loop      -> allowed                        (cycle)
//	base/outside/secret.nxs
//	base/other/o.nxs                                         (a second allowed area in one test)
type fsFixture struct {
	allowed, outside, other string
}

func newFSFixture(t *testing.T) fsFixture {
	t.Helper()
	base := t.TempDir()
	f := fsFixture{
		allowed: filepath.Join(base, "allowed"),
		outside: filepath.Join(base, "outside"),
		other:   filepath.Join(base, "other"),
	}
	fsWrite(t, filepath.Join(f.allowed, "a", "scan1.nxs"))
	fsWrite(t, filepath.Join(f.allowed, "a", "notes.txt"))
	fsWrite(t, filepath.Join(f.allowed, "a", "sub", "deep.h5"))
	fsWrite(t, filepath.Join(f.allowed, ".hidden", "h.nxs"))
	fsWrite(t, filepath.Join(f.outside, "secret.nxs"))
	fsWrite(t, filepath.Join(f.other, "o.nxs"))
	fsLink(t, filepath.Join(f.allowed, "a"), filepath.Join(f.allowed, "link_in"))
	fsLink(t, f.outside, filepath.Join(f.allowed, "escape"))
	fsLink(t, filepath.Join(f.outside, "secret.nxs"), filepath.Join(f.allowed, "escfile.nxs"))
	fsLink(t, f.allowed, filepath.Join(f.allowed, "loop"))
	return f
}

func TestFSStoreAccessDecisions(t *testing.T) {
	f := newFSFixture(t)
	st, err := newFSStore(&Config{FileSystemMode: true, DataDir: f.allowed})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	j := filepath.Join
	cases := []struct {
		name string
		key  string
		want int // 0 = allowed, else the HTTP status of the refusal
	}{
		{"file in the allowed area", j(f.allowed, "a", "scan1.nxs"), 0},
		{".h5 in a subdirectory", j(f.allowed, "a", "sub", "deep.h5"), 0},
		{"through a link that stays inside", j(f.allowed, "link_in", "scan1.nxs"), 0},
		{"through a link that leaves the area", j(f.allowed, "escape", "secret.nxs"), 403},
		{"file link that leaves the area", j(f.allowed, "escfile.nxs"), 403},
		{"path outside the area", j(f.outside, "secret.nxs"), 403},
		{"relative path", "a/scan1.nxs", 400},
		{"empty", "", 400},
		// Not built with filepath.Join: Join cleans the ".." away, and the store
		// would never see it.
		{"dot-dot is refused, not cleaned", f.allowed + "/../outside/secret.nxs", 400},
		{"not a NeXus file name", j(f.allowed, "a", "notes.txt"), 400},
		{"a directory", j(f.allowed, "a"), 400},
		{"missing file", j(f.allowed, "a", "missing.nxs"), 404},
		{"NUL byte", j(f.allowed, "a", "scan1.nxs") + "\x00.txt", 400},
	}
	for _, tc := range cases {
		got, err := st.Ensure(ctx, tc.key)
		if tc.want == 0 {
			if err != nil || got != filepath.Clean(tc.key) {
				t.Errorf("%s: Ensure = %q, %v; want the path itself", tc.name, got, err)
			}
			continue
		}
		if statusOf(err) != tc.want {
			t.Errorf("%s: status = %d (%v), want %d", tc.name, statusOf(err), err, tc.want)
		}
	}
}

func TestFSStoreUnrestricted(t *testing.T) {
	f := newFSFixture(t)
	st, err := newFSStore(&Config{FileSystemMode: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(f.outside, "secret.nxs"),
		filepath.Join(f.allowed, "escape", "secret.nxs"),
		filepath.Join(f.other, "o.nxs"),
	} {
		if _, err := st.Ensure(context.Background(), p); err != nil {
			t.Errorf("without data_dir %s should be readable: %v", p, err)
		}
	}
}

func TestFSStoreSeveralAllowedDirectories(t *testing.T) {
	f := newFSFixture(t)
	st, err := newFSStore(&Config{FileSystemMode: true, DataDir: f.allowed + string(os.PathListSeparator) + f.other})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := st.Ensure(ctx, filepath.Join(f.other, "o.nxs")); err != nil {
		t.Errorf("second allowed directory refused: %v", err)
	}
	if _, err := st.Ensure(ctx, filepath.Join(f.outside, "secret.nxs")); statusOf(err) != 403 {
		t.Errorf("outside both: %v", err)
	}
	if _, err := newFSStore(&Config{FileSystemMode: true, DataDir: filepath.Join(f.allowed, "no-such-dir")}); err == nil {
		t.Errorf("a data_dir that does not exist should be a configuration error")
	}
	if _, err := newFSStore(&Config{FileSystemMode: true, DataDir: filepath.Join(f.allowed, "a", "scan1.nxs")}); err == nil {
		t.Errorf("a data_dir that is a file should be a configuration error")
	}
}

func fsKeys(files []FileInfo) []string {
	out := []string{}
	for _, f := range files {
		out = append(out, f.Key)
	}
	return out
}

func TestFSStoreList(t *testing.T) {
	f := newFSFixture(t)
	st, err := newFSStore(&Config{FileSystemMode: true, DataDir: f.allowed})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	j := filepath.Join

	// Followed: the link that stays inside. Not listed: hidden dirs, the
	// directory and file links that leave the area, the cycle, non-NeXus files.
	got, err := st.List(ctx, f.allowed+"/")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		j(f.allowed, "a", "scan1.nxs"), j(f.allowed, "a", "sub", "deep.h5"),
		j(f.allowed, "link_in", "scan1.nxs"), j(f.allowed, "link_in", "sub", "deep.h5"),
	}
	if !reflect.DeepEqual(fsKeys(got), want) {
		t.Errorf("List(allowed/) = %v, want %v", fsKeys(got), want)
	}
	for _, fi := range got {
		if fi.Size == 0 || fi.ETag == "" || fi.LastModified.IsZero() {
			t.Errorf("incomplete FileInfo: %+v", fi)
		}
	}

	// A prefix is a string prefix, as with S3 keys.
	got, _ = st.List(ctx, j(f.allowed, "a")+"/")
	if w := []string{j(f.allowed, "a", "scan1.nxs"), j(f.allowed, "a", "sub", "deep.h5")}; !reflect.DeepEqual(fsKeys(got), w) {
		t.Errorf("List(a/) = %v, want %v", fsKeys(got), w)
	}
	got, _ = st.List(ctx, j(f.allowed, "a", "sc"))
	if w := []string{j(f.allowed, "a", "scan1.nxs")}; !reflect.DeepEqual(fsKeys(got), w) {
		t.Errorf("List(a/sc) = %v, want %v", fsKeys(got), w)
	}

	// Typing a path that does not exist (yet) is not an error.
	if got, err := st.List(ctx, j(f.allowed, "nope")+"/"); err != nil || len(got) != 0 {
		t.Errorf("missing directory: %v, %v", fsKeys(got), err)
	}
	// Nothing is listed without a prefix.
	if got, err := st.List(ctx, ""); err != nil || len(got) != 0 {
		t.Errorf("no prefix: %v, %v", fsKeys(got), err)
	}
	// Refusals.
	if _, err := st.List(ctx, f.outside+"/"); statusOf(err) != 403 {
		t.Errorf("outside the allowed area: %v", err)
	}
	if _, err := st.List(ctx, j(f.allowed, "escape")+"/"); statusOf(err) != 403 {
		t.Errorf("directory link leaving the area: %v", err)
	}
	if _, err := st.List(ctx, "a/"); statusOf(err) != 400 {
		t.Errorf("relative prefix: %v", err)
	}
	if _, err := st.List(ctx, f.allowed+"/../outside/"); statusOf(err) != 400 {
		t.Errorf("dot-dot prefix: %v", err)
	}
}

func TestFSStoreListIsBounded(t *testing.T) {
	f := newFSFixture(t)
	st, _ := newFSStore(&Config{FileSystemMode: true, DataDir: f.allowed})
	old := fsListLimit
	fsListLimit = 2
	defer func() { fsListLimit = old }()
	got, err := st.List(context.Background(), f.allowed+"/")
	if err != nil || len(got) != 2 {
		t.Errorf("limit 2: got %d files, %v", len(got), err)
	}
}

func TestFSStoreDefaultListingDirectory(t *testing.T) {
	f := newFSFixture(t)
	st, _ := newFSStore(&Config{FileSystemMode: true, DataDir: f.allowed, Prefix: filepath.Join(f.allowed, "a") + "/"})
	got, err := st.List(context.Background(), "")
	if err != nil || len(got) != 2 {
		t.Errorf("default directory from the prefix setting: %v, %v", fsKeys(got), err)
	}
	// A relative default (an S3-style prefix such as chess/) is ignored.
	st, _ = newFSStore(&Config{FileSystemMode: true, DataDir: f.allowed, Prefix: "chess/"})
	if got, err := st.List(context.Background(), ""); err != nil || len(got) != 0 {
		t.Errorf("relative default prefix should be ignored: %v, %v", fsKeys(got), err)
	}
}

func TestFSStoreChangeNotification(t *testing.T) {
	f := newFSFixture(t)
	st, _ := newFSStore(&Config{FileSystemMode: true, DataDir: f.allowed})
	var changed []string
	st.SetOnFileChanged(func(p string) { changed = append(changed, p) })
	p := filepath.Join(f.allowed, "a", "scan1.nxs")
	for i := 0; i < 2; i++ {
		if _, err := st.Ensure(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	if len(changed) != 0 {
		t.Fatalf("unchanged file reported as changed: %v", changed)
	}
	if err := os.WriteFile(p, []byte("now a longer content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Ensure(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(changed, []string{p}) {
		t.Fatalf("changed = %v, want [%s]", changed, p)
	}
}

func TestFSModeViewKeepsTheAbsolutePath(t *testing.T) {
	s := &server{cfg: &Config{FileSystemMode: true, DataDir: "/nfs"}}
	cases := []struct{ file, want string }{
		{"/nfs/chess/x/scan.nxs", "/nfs/chess/x/scan.nxs"},
		{"file:///nfs/chess/x/scan.nxs", "/nfs/chess/x/scan.nxs"},
		{"/nfs/a b/c#1.nxs", "/nfs/a b/c#1.nxs"},
		{"/nfs/x/../y.nxs", "/nfs/x/../y.nxs"},
	}
	for _, tc := range cases {
		if got := s.viewKey(tc.file); got != tc.want {
			t.Errorf("%q: key = %q, want %q", tc.file, got, tc.want)
		}
	}
}

func TestFSModeHandlers(t *testing.T) {
	f := newFSFixture(t)
	cfg := &Config{FileSystemMode: true, DataDir: f.allowed}
	st, err := newFSStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{cfg: cfg, store: st}
	if s.mode() != "filesystem" {
		t.Errorf("mode = %q", s.mode())
	}

	get := func(prefix string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/files?"+url.Values{"prefix": {prefix}}.Encode(), nil)
		rec := httptest.NewRecorder()
		s.handleFiles(rec, req)
		return rec
	}
	rec := get(filepath.Join(f.allowed, "a") + "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Mode  string     `json:"mode"`
		Count int        `json:"count"`
		Files []FileInfo `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Mode != "filesystem" || body.Count != 2 {
		t.Errorf("body = %+v", body)
	}
	if rec := get(f.outside + "/"); rec.Code != http.StatusForbidden {
		t.Errorf("outside the allowed area: status %d, want 403", rec.Code)
	}
	if rec := get("relative/"); rec.Code != http.StatusBadRequest {
		t.Errorf("relative prefix: status %d, want 400", rec.Code)
	}
}
