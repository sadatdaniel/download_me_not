package video

// Offline: a local page with a video we generate with ffmpeg; yt-dlp's generic extractor finds it.
// Never a real video site (AGENTS.md rule 7). Skips when yt-dlp or ffmpeg is missing.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sadatdaniel/download_me_not/internal/netguard"
)

func fixture(t *testing.T) (Runner, string) {
	t.Helper()
	bin, err := exec.LookPath("yt-dlp")
	if err != nil {
		t.Skip("yt-dlp not installed (use the dev container)")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mp4")
	if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=25",
		"-f", "lavfi", "-i", "sine=duration=2", "-shortest", "-c:v", "libx264", "-c:a", "aac", clip).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/watch", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>Test Clip</title></head><body><video src="/clip.mp4"></video></body></html>`))
	})
	mux.HandleFunc("/clip.mp4", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, clip) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return Runner{Bin: bin}, srv.URL + "/watch"
}

func TestProbeAndDownload(t *testing.T) {
	r, page := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	info, err := r.Probe(ctx, page)
	if err != nil {
		t.Fatal(err)
	}
	if info.Title == "" || len(info.Formats) == 0 {
		t.Fatalf("probe: %+v", info)
	}
	dir := t.TempDir()
	var calls int
	file, err := r.Download(ctx, page, "best", Options{}, dir, func(done, total int64) { calls++ })
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(file); err != nil || fi.Size() == 0 || filepath.Dir(file) != dir {
		t.Fatalf("file %s: %v", file, err)
	}
	if calls == 0 {
		t.Fatal("no progress reported")
	}
}

func TestSafety(t *testing.T) {
	for _, bad := range []string{"--exec=rm -rf /", "-o", "bv*;rm", strings.Repeat("a", 300)} {
		if _, err := Selector(bad); err == nil {
			t.Errorf("format %q accepted", bad)
		}
	}
	for _, ok := range []string{"", "best", "mp3", "137+140", "bv*[height<=720]+ba/b"} {
		if _, err := Selector(ok); err != nil {
			t.Errorf("format %q rejected: %v", ok, err)
		}
	}
	r, _ := fixture(t)
	// A URL that looks like an option must be treated as a URL ("--" before it).
	if _, err := r.Probe(context.Background(), "--version"); err == nil {
		t.Fatal("option-looking URL was executed as an option")
	}
}

// yt-dlp makes its own connections; through netguard's proxy it must not reach internal addresses.
func TestGuardedProxy(t *testing.T) {
	r, page := fixture(t)
	px := httptest.NewServer(netguard.NewProxy())
	defer px.Close()
	r.Proxy = px.URL
	if _, err := r.Probe(context.Background(), page); err == nil {
		t.Fatal("yt-dlp reached a loopback page through the guarded proxy")
	}
}

func TestOptions(t *testing.T) {
	a, err := Options{Subs: []string{"en", "pt-BR"}, Container: "mkv"}.Args()
	if err != nil || strings.Join(a, " ") != "--merge-output-format mkv --write-subs --sub-langs en,pt-BR --convert-subs srt" {
		t.Fatalf("args: %v %v", a, err)
	}
	if a, err := (Options{}).Args(); err != nil || len(a) != 0 {
		t.Fatalf("no options must add nothing: %v %v", a, err)
	}
	for _, bad := range []Options{{Container: "avi"}, {Subs: []string{"en,--exec=x"}}, {Subs: []string{"--exec"}}} {
		if _, err := bad.Args(); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
