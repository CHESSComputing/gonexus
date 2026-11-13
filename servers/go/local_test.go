package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}
}

func keys(fs []FileInfo) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Key)
	}
	return out
}

// fixture:
//
//	root/chess/results.nxs
//	root/chess/notes.txt                      (ignored: not a NeXus file)
//	root/chess/a_link.nxs -> external/RE3/a.nxs        (symlinked file)
//	root/chess/RE3        -> external/RE3              (symlinked directory)
//	root/chess/broken.nxs -> /nonexistent               (broken link)
//	external/RE3/a.nxs, external/RE3/sub/b.h5
//	external/RE3/.hidden/c.nxs                (hidden dir: ignored)
//	external/RE3/sub/loop -> root/chess       (cycle back to an ancestor)
func fixture(t *testing.T) (*LocalStore, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "data")
	ext := filepath.Join(base, "external", "RE3")

	writeFile(t, filepath.Join(root, "chess", "results.nxs"), "x")
	writeFile(t, filepath.Join(root, "chess", "notes.txt"), "x")
	writeFile(t, filepath.Join(ext, "a.nxs"), "aaaa")
	writeFile(t, filepath.Join(ext, "sub", "b.h5"), "b")
	writeFile(t, filepath.Join(ext, ".hidden", "c.nxs"), "c")
	symlink(t, ext, filepath.Join(root, "chess", "RE3"))
	symlink(t, filepath.Join(ext, "a.nxs"), filepath.Join(root, "chess", "a_link.nxs"))
	symlink(t, "/nonexistent/file.nxs", filepath.Join(root, "chess", "broken.nxs"))
	symlink(t, filepath.Join(root, "chess"), filepath.Join(ext, "sub", "loop"))

	st, err := newLocalStore(&Config{DataDir: root, Prefix: "chess/"})
	if err != nil {
		t.Fatal(err)
	}
	return st, root
}

func TestLocalStoreListFollowsSymlinks(t *testing.T) {
	st, _ := fixture(t)
	ctx := context.Background()

	got, err := st.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"chess/RE3/a.nxs", "chess/RE3/sub/b.h5", "chess/a_link.nxs", "chess/results.nxs"}
	if !reflect.DeepEqual(keys(got), want) {
		t.Fatalf("List() keys = %v, want %v", keys(got), want)
	}
	for _, f := range got {
		if f.Size == 0 || f.ETag == "" || f.LastModified.IsZero() {
			t.Errorf("incomplete FileInfo: %+v", f)
		}
	}

	// the prefix is a string prefix, as in S3
	got, _ = st.List(ctx, "chess/RE3/")
	if want := []string{"chess/RE3/a.nxs", "chess/RE3/sub/b.h5"}; !reflect.DeepEqual(keys(got), want) {
		t.Errorf("prefix chess/RE3/: %v, want %v", keys(got), want)
	}
	got, _ = st.List(ctx, "chess/res")
	if want := []string{"chess/results.nxs"}; !reflect.DeepEqual(keys(got), want) {
		t.Errorf("prefix chess/res: %v, want %v", keys(got), want)
	}
	got, _ = st.List(ctx, "other/")
	if len(got) != 0 {
		t.Errorf("prefix other/: %v, want none", keys(got))
	}
}

func TestLocalStoreEnsure(t *testing.T) {
	st, root := fixture(t)
	ctx := context.Background()

	// through a symlinked directory and a symlinked file
	for _, key := range []string{"chess/RE3/a.nxs", "chess/RE3/sub/b.h5", "chess/a_link.nxs", "chess/results.nxs"} {
		p, err := st.Ensure(ctx, key)
		if err != nil {
			t.Fatalf("Ensure(%q): %v", key, err)
		}
		if want := filepath.Join(root, filepath.FromSlash(key)); p != want {
			t.Errorf("Ensure(%q) = %q, want %q", key, p, want)
		}
	}
	for _, key := range []string{"", "../x.nxs", "chess/../../x.nxs", "chess/notes.txt", "chess/RE3", "chess/missing.nxs", "chess/broken.nxs"} {
		if _, err := st.Ensure(ctx, key); err == nil {
			t.Errorf("Ensure(%q) should fail", key)
		}
	}
}

func TestLocalStoreChangeNotification(t *testing.T) {
	st, root := fixture(t)
	ctx := context.Background()
	var changed []string
	st.SetOnFileChanged(func(p string) { changed = append(changed, p) })

	key := "chess/results.nxs"
	full := filepath.Join(root, key)
	for i := 0; i < 2; i++ {
		if _, err := st.Ensure(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	if len(changed) != 0 {
		t.Fatalf("unchanged file reported as changed: %v", changed)
	}
	writeFile(t, full, "now a longer content") // different size
	if _, err := st.Ensure(ctx, key); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(changed, []string{full}) {
		t.Fatalf("changed = %v, want [%s]", changed, full)
	}
}
