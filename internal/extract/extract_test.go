package extract

// Self-made archives only (stdlib writers). 7z and rar decoding is covered by mholt/archives upstream.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeZip(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, _ := zw.Create(name)
		w.Write(data)
	}
	zw.Close()
	os.WriteFile(path, buf.Bytes(), 0o644)
}

func TestZipAndTraversal(t *testing.T) {
	base := t.TempDir()
	dl := filepath.Join(base, "dl")
	os.Mkdir(dl, 0o755)
	arc := filepath.Join(dl, "pack.zip")
	writeZip(t, arc, map[string][]byte{
		"a.txt":            []byte("hello"),
		"sub/b.txt":        []byte("world"),
		"../../escape.txt": []byte("evil"),
		"/abs/evil.txt":    []byte("evil"),
	})
	res, err := Extract(context.Background(), arc, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Dir != filepath.Join(dl, "pack") || res.Files != 4 {
		t.Fatalf("%+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(res.Dir, "sub", "b.txt")); string(b) != "world" {
		t.Fatal("content")
	}
	// "../../escape.txt" is anchored inside the target folder, never above it.
	if _, err := os.Stat(filepath.Join(base, "escape.txt")); err == nil {
		t.Fatal("escaped the folder")
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "escape.txt")); err != nil {
		t.Fatal("anchored entry missing")
	}
	// A second extraction gets its own folder.
	if res2, err := Extract(context.Background(), arc, Limits{}); err != nil || res2.Dir != filepath.Join(dl, "pack (1)") {
		t.Fatalf("%+v %v", res2, err)
	}
}

func TestBombAndSymlink(t *testing.T) {
	dir := t.TempDir()
	bomb := filepath.Join(dir, "bomb.zip")
	writeZip(t, bomb, map[string][]byte{"zeros.bin": make([]byte, 20<<20)}) // ~20 KiB compressed
	if _, err := Extract(context.Background(), bomb, Limits{MaxRatio: 100}); !errors.Is(err, ErrTooBig) {
		t.Fatalf("want ErrTooBig, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bomb")); !os.IsNotExist(err) {
		t.Fatal("partial folder left behind")
	}

	// tar.gz with a symlink pointing outside: the link is skipped, the file extracted.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	tw.WriteHeader(&tar.Header{Name: "ok.txt", Mode: 0o644, Size: 2})
	tw.Write([]byte("ok"))
	tw.Close()
	gz.Close()
	arc := filepath.Join(dir, "x.tar.gz")
	os.WriteFile(arc, buf.Bytes(), 0o644)
	res, err := Extract(context.Background(), arc, Limits{})
	if err != nil || res.Files != 1 || res.Dir != filepath.Join(dir, "x") {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Lstat(filepath.Join(res.Dir, "link")); err == nil {
		t.Fatal("symlink extracted")
	}

	plain := filepath.Join(dir, "notes.txt")
	os.WriteFile(plain, []byte("just text"), 0o644)
	if _, err := Extract(context.Background(), plain, Limits{}); !errors.Is(err, ErrNotArchive) {
		t.Fatalf("want ErrNotArchive, got %v", err)
	}
	if !IsArchive("Movie.Part.TAR.GZ") || IsArchive("movie.mkv") {
		t.Fatal("IsArchive")
	}
}
