package rclone

// Uses rclone's "local" backend as the destination: same code path, no cloud account needed.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCopyAndMove(t *testing.T) {
	bin, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone not installed (use the dev container)")
	}
	src := t.TempDir()
	data := bytes.Repeat([]byte("download_me_not "), 64<<10) // 1 MiB
	os.WriteFile(filepath.Join(src, "movie.bin"), data, 0o644)
	os.MkdirAll(filepath.Join(src, "season", "e1"), 0o755)
	os.WriteFile(filepath.Join(src, "season", "e1", "ep.bin"), data[:1000], 0o644)
	dst := t.TempDir()
	r := Runner{Bin: bin}
	rem := Remote{Name: "drive", Params: map[string]string{"type": "local"}}

	var last int64
	tok, err := r.Copy(context.Background(), rem, filepath.Join(src, "movie.bin"), filepath.Join(dst, "Movies", "movie.bin"), false,
		func(done, total int64) { last = done })
	if err != nil || tok != "" {
		t.Fatalf("copy: %v %q", err, tok)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "Movies", "movie.bin")); !bytes.Equal(b, data) || last == 0 {
		t.Fatalf("copied %d bytes, progress %d", len(b), last)
	}
	if _, err := os.Stat(filepath.Join(src, "movie.bin")); err != nil {
		t.Fatal("copy removed the source")
	}
	// A folder, moved: the local copy goes only after rclone verified the upload.
	if _, err := r.Copy(context.Background(), rem, filepath.Join(src, "season"), filepath.Join(dst, "TV", "season"), true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "TV", "season", "e1", "ep.bin")); err != nil {
		t.Fatal("folder not copied")
	}
	if _, err := os.Stat(filepath.Join(src, "season", "e1", "ep.bin")); !os.IsNotExist(err) {
		t.Fatal("move left the local file")
	}
	if _, err := r.Copy(context.Background(), Remote{Name: "x", Params: map[string]string{"type": "local", "evil": "a\n[other]"}},
		filepath.Join(src, "movie.bin"), dst, false, nil); err == nil {
		t.Fatal("config injection accepted")
	}
}
