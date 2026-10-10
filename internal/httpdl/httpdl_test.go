package httpdl

// Offline: every server is an in-process httptest server on loopback (AGENTS.md rule 7).

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func payload(n int) []byte {
	b := make([]byte, n)
	r := rand.New(rand.NewChaCha8([32]byte{7}))
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

// slowWriter throttles a response so downloads take long enough to split, cancel and resume.
type slowWriter struct {
	http.ResponseWriter
	chunk int
	delay time.Duration
}

func (w slowWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		c := min(len(p), w.chunk)
		m, err := w.ResponseWriter.Write(p[:c])
		n += m
		if err != nil {
			return n, err
		}
		w.ResponseWriter.(http.Flusher).Flush()
		time.Sleep(w.delay)
		p = p[c:]
	}
	return n, nil
}

type server struct {
	*httptest.Server
	requests atomic.Int64
	served   atomic.Int64
}

// newServer serves data with Range support. slow(r) may return a delay per 16 KiB chunk.
func newServer(t *testing.T, data []byte, etag string, slow func(*http.Request) time.Duration) *server {
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		var ww http.ResponseWriter = countWriter{w, &s.served}
		if slow != nil {
			if d := slow(r); d > 0 {
				ww = slowWriter{ww, 16 << 10, d}
			}
		}
		http.ServeContent(ww, r, "file.bin", time.Time{}, bytes.NewReader(data))
	}))
	t.Cleanup(s.Close)
	return s
}

type countWriter struct {
	http.ResponseWriter
	n *atomic.Int64
}

func (w countWriter) Write(p []byte) (int, error) {
	w.n.Add(int64(len(p)))
	return w.ResponseWriter.Write(p)
}
func (w countWriter) Flush() { w.ResponseWriter.(http.Flusher).Flush() }

func get(t *testing.T, url string, conns int, st *State) (string, *Download, error) {
	t.Helper()
	ctx := context.Background()
	if st == nil {
		p, err := Probe(ctx, http.DefaultClient, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		st = &p
	}
	file := filepath.Join(t.TempDir(), "f.part")
	d := New(*st, file, Options{Client: http.DefaultClient, Connections: conns, MinSplit: 64 << 10})
	return file, d, d.Run(ctx)
}

func same(t *testing.T, file string, want []byte) {
	t.Helper()
	got, _ := os.ReadFile(file)
	if !bytes.Equal(got, want) {
		t.Fatalf("content differs: %d vs %d bytes", len(got), len(want))
	}
}

func TestMultiConnection(t *testing.T) {
	data := payload(8<<20 + 17)
	s := newServer(t, data, `"v1"`, nil)
	file, d, err := get(t, s.URL+"/dir/movie.mkv", 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	same(t, file, data)
	if st := d.State(); st.Name != "movie.mkv" || len(st.Segments) < 8 {
		t.Fatalf("name %q, %d segments", st.Name, len(st.Segments))
	}
}

func TestWorkStealing(t *testing.T) {
	data := payload(4 << 20)
	// The connection that starts at byte 0 is very slow; the others must split its remainder.
	s := newServer(t, data, `"v1"`, func(r *http.Request) time.Duration {
		if strings.HasPrefix(r.Header.Get("Range"), "bytes=0-") && r.Header.Get("Range") != "bytes=0-0" {
			return 20 * time.Millisecond
		}
		return 0
	})
	start := time.Now()
	file, d, err := get(t, s.URL, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	same(t, file, data)
	// Without stealing the slow first quarter (1 MiB / 16 KiB * 20 ms) takes ~1.3 s.
	if n := len(d.State().Segments); n <= 4 || time.Since(start) > time.Second {
		t.Fatalf("no effective stealing: %d segments in %v", n, time.Since(start))
	}
}

func TestResume(t *testing.T) {
	data := payload(4 << 20)
	s := newServer(t, data, `"v1"`, func(*http.Request) time.Duration { return 10 * time.Millisecond })
	st, _ := Probe(context.Background(), http.DefaultClient, s.URL, nil)
	file := filepath.Join(t.TempDir(), "f.part")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	d := New(st, file, Options{Client: http.DefaultClient, Connections: 4, MinSplit: 64 << 10})
	if err := d.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline, got %v", err)
	}
	saved := d.State()
	part := d.Completed()
	if part == 0 || part >= int64(len(data)) {
		t.Fatalf("partial = %d", part)
	}
	before := s.served.Load()
	d2 := New(saved, file, Options{Client: http.DefaultClient, Connections: 4, MinSplit: 64 << 10})
	if err := d2.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	same(t, file, data)
	if again := s.served.Load() - before; again > int64(len(data))-part+(1<<20) {
		t.Fatalf("resume re-downloaded too much: %d of %d remaining", again, int64(len(data))-part)
	}
}

func TestChangedFileOnResume(t *testing.T) {
	data := payload(1 << 20)
	s := newServer(t, data, `"v2"`, nil)
	st, _ := Probe(context.Background(), http.DefaultClient, s.URL, nil)
	st.ETag = `"v1"` // what we saw last time
	st.Segments = []Segment{{0, st.Size, 1000}}
	if _, _, err := get(t, s.URL, 1, &st); !errors.Is(err, ErrChanged) {
		t.Fatalf("want ErrChanged, got %v", err)
	}
}

func TestNoRangeSupport(t *testing.T) {
	data := payload(300 << 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(data) // ignores Range, no Content-Length known up front (chunked)
	}))
	defer srv.Close()
	file, d, err := get(t, srv.URL+"/x.bin", 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	same(t, file, data)
	if st := d.State(); st.Ranges || st.Size != int64(len(data)) {
		t.Fatalf("state %+v", st)
	}
}

func TestRetryAndFatal(t *testing.T) {
	data := payload(256 << 10)
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=0-0" && calls.Add(1) <= 2 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	file, _, err := get(t, srv.URL, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	same(t, file, data)

	gone := httptest.NewServer(http.NotFoundHandler())
	defer gone.Close()
	if _, err := Probe(context.Background(), http.DefaultClient, gone.URL, nil); !errors.As(err, &HTTPError{}) {
		t.Fatalf("want HTTPError, got %v", err)
	}
}

func TestHeadersAndName(t *testing.T) {
	data := payload(100 << 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, _ := r.Cookie("session"); c == nil || c.Value != "abc" || r.Referer() != "https://site.example/" {
			http.Error(w, "login required", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="../../evil.zip"`)
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	h := http.Header{"Cookie": {"session=abc"}, "Referer": {"https://site.example/"}}
	st, err := Probe(context.Background(), http.DefaultClient, srv.URL, h)
	if err != nil {
		t.Fatal(err)
	}
	if st.Name != ".._.._evil.zip" {
		t.Fatalf("name not sanitized: %q", st.Name)
	}
	file := filepath.Join(t.TempDir(), "f.part")
	if err := New(st, file, Options{Client: http.DefaultClient, Header: h, Connections: 2}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	same(t, file, data)
}

func TestAvailableWhileStreaming(t *testing.T) {
	data := payload(1 << 20)
	s := newServer(t, data, "", func(*http.Request) time.Duration { return time.Millisecond })
	st, _ := Probe(context.Background(), http.DefaultClient, s.URL, nil)
	d := New(st, filepath.Join(t.TempDir(), "f.part"), Options{Client: http.DefaultClient, Connections: 1})
	done := make(chan error)
	go func() { done <- d.Run(context.Background()) }()
	deadline := time.Now().Add(5 * time.Second)
	for d.Available(0) < 64<<10 {
		if time.Now().After(deadline) {
			t.Fatal("no progress")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if d.Available(int64(len(data))-1) != 0 {
		t.Fatal("end reported available before it was downloaded (stream mode is sequential)")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if d.Available(0) != int64(len(data)) {
		t.Fatal("not fully available")
	}
}
