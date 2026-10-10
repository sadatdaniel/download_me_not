package fsapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setup(t *testing.T) (*FS, string, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "sub", "a.txt"), []byte("hello world"), 0o644)
	os.WriteFile(filepath.Join(base, "secret.txt"), []byte("outside"), 0o644)
	// Hostile symlinks inside the root.
	os.Symlink(filepath.Join(base, "secret.txt"), filepath.Join(root, "abs-link"))
	os.Symlink("../secret.txt", filepath.Join(root, "rel-link"))
	os.Symlink("..", filepath.Join(root, "parent-link"))
	os.Symlink("/etc", filepath.Join(root, "etc-link"))
	f, err := New(root, []byte("k"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, root, base
}

func TestPathEscape(t *testing.T) {
	f, _, base := setup(t)
	hostile := []string{
		"../secret.txt", "/../secret.txt", "sub/../../secret.txt", "..%2fsecret.txt",
		"abs-link", "rel-link", "parent-link/secret.txt", "etc-link/passwd", "a\x00b",
	}
	for _, p := range hostile {
		if _, err := f.Stat(p); err == nil && !strings.Contains(p, "%2f") {
			t.Errorf("Stat(%q) escaped", p)
		}
		rec := httptest.NewRecorder()
		f.Serve(rec, httptest.NewRequest("GET", "/", nil), p)
		if strings.Contains(rec.Body.String(), "outside") || strings.Contains(rec.Body.String(), "root:") {
			t.Errorf("Serve(%q) leaked outside data", p)
		}
		f.Remove(p)
		f.Move(p, "stolen")
	}
	if _, err := os.Stat(filepath.Join(base, "secret.txt")); err != nil {
		t.Fatal("outside file was removed or moved")
	}
	if err := f.Remove("/"); err == nil {
		t.Fatal("removed root")
	}
	if err := f.Move("sub/a.txt", "../escaped.txt"); err == nil {
		// clean() maps "../escaped.txt" to "escaped.txt" inside the root; it must not land outside.
		if _, err := os.Stat(filepath.Join(base, "escaped.txt")); err == nil {
			t.Fatal("move escaped the root")
		}
	}
}

func TestListMkdirMoveRemove(t *testing.T) {
	f, root, _ := setup(t)
	if err := f.Mkdir("/new/deep"); err != nil {
		t.Fatal(err)
	}
	if err := f.Move("/sub/a.txt", "/new/deep/b.txt"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "outside-change.txt"), nil, 0o644) // added behind the server's back
	es, err := f.List("/")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range es {
		names[e.Name] = true
	}
	if !names["new"] || !names["outside-change.txt"] {
		t.Fatalf("list: %v", names)
	}
	if err := f.Remove("/new"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat("/new"); err == nil {
		t.Fatal("not removed")
	}
	if u, err := f.Usage(); err != nil || u.Total == 0 {
		t.Fatalf("usage %v %v", u, err)
	}
}

func TestServeRangeAndSign(t *testing.T) {
	f, _, _ := setup(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Range", "bytes=6-10")
	rec := httptest.NewRecorder()
	f.Serve(rec, req, "/sub/a.txt")
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusPartialContent || string(body) != "world" {
		t.Fatalf("range: %d %q", rec.Code, body)
	}
	q, _ := f.Sign("/sub/a.txt", time.Minute)
	v, _ := url.ParseQuery(q)
	if !f.Verify("sub/a.txt", v.Get("exp"), v.Get("sig")) {
		t.Fatal("valid signature rejected")
	}
	if f.Verify("/sub/other.txt", v.Get("exp"), v.Get("sig")) {
		t.Fatal("signature accepted for another path")
	}
	old, _ := f.Sign("/sub/a.txt", -time.Minute)
	v, _ = url.ParseQuery(old)
	if f.Verify("/sub/a.txt", v.Get("exp"), v.Get("sig")) {
		t.Fatal("expired signature accepted")
	}
}
