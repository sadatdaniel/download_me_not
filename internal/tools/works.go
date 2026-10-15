package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// works proves that a freshly downloaded tool does its job before it replaces the working copy: a release that
// starts but cannot read a page or copy a file would otherwise break every download until someone noticed.
// Everything stays on this machine (a loopback page, local folders).
func works(ctx context.Context, name, exe string) error {
	dir, err := os.MkdirTemp("", "getaload-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	switch name {
	case "yt-dlp":
		return ytdlpWorks(ctx, exe, dir)
	case "rclone":
		return rcloneWorks(ctx, exe, dir)
	case "deno":
		return denoWorks(ctx, exe, dir)
	}
	return fmt.Errorf("no check for %s", name)
}

// run starts exe with a clean environment (no proxy settings, caches in dir) and returns its output.
func run(ctx context.Context, exe, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = []string{"HOME=" + dir, "PATH=/usr/bin:/bin", "NO_COLOR=1", "DENO_DIR=" + filepath.Join(dir, "deno")}
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%v: %.300s", err, errOut.String())
	}
	return out, nil
}

// ytdlpWorks: its generic extractor finds the video on a page served from loopback, as for any site.
func ytdlpWorks(ctx context.Context, exe, dir string) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/clip.mp4" {
			w.Header().Set("Content-Type", "video/mp4")
			w.Write(make([]byte, 1024))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>check</title></head><body><video src="/clip.mp4"></video></body></html>`)
	})}
	go srv.Serve(ln)
	defer srv.Close()
	out, err := run(ctx, exe, dir, "-J", "--no-warnings", "--ignore-config", "--no-cache-dir", "--no-playlist",
		"http://"+ln.Addr().String()+"/page")
	if err != nil {
		return err
	}
	var info map[string]any
	if json.Unmarshal(out, &info) != nil || !strings.Contains(string(out), "clip.mp4") {
		return fmt.Errorf("did not find the video on a test page: %.200s", out)
	}
	return nil
}

// rcloneWorks: copies a file from one local folder to another.
func rcloneWorks(ctx context.Context, exe, dir string) error {
	src, dst, conf := filepath.Join(dir, "a.txt"), filepath.Join(dir, "out", "b.txt"), filepath.Join(dir, "rclone.conf")
	if err := os.WriteFile(src, []byte("getaload check"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(conf, nil, 0o600); err != nil {
		return err
	}
	if _, err := run(ctx, exe, dir, "--config", conf, "copyto", src, dst); err != nil {
		return err
	}
	if b, _ := os.ReadFile(dst); string(b) != "getaload check" {
		return fmt.Errorf("copied file differs: %q", b)
	}
	return nil
}

// denoWorks: runs a line of JavaScript (yt-dlp uses deno for some sites' player code).
func denoWorks(ctx context.Context, exe, dir string) error {
	out, err := run(ctx, exe, dir, "eval", "console.log(6*7)")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != "42" {
		return fmt.Errorf("did not run code: %.200s", out)
	}
	return nil
}

// major is the first number of a version tag: "v1.68.2" → "1".
func major(tag string) string {
	v := strings.TrimPrefix(tag, "v")
	if i := strings.IndexAny(v, ".-"); i >= 0 {
		v = v[:i]
	}
	return v
}
