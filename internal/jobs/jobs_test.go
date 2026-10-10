package jobs

// Offline (AGENTS.md rule 7): loopback HTTP servers, an in-process seeder and tracker, DHT off.

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"github.com/sadatdaniel/download_me_not/internal/engine"
	"github.com/sadatdaniel/download_me_not/internal/store"
)

func payload(n int, seed byte) []byte {
	b := make([]byte, n)
	r := rand.New(rand.NewChaCha8([32]byte{seed}))
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

type slow struct {
	http.ResponseWriter
	d time.Duration
}

func (w slow) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		c := min(len(p), 16<<10)
		k, err := w.ResponseWriter.Write(p[:c])
		n += k
		if err != nil {
			return n, err
		}
		w.ResponseWriter.(http.Flusher).Flush()
		time.Sleep(w.d)
		p = p[c:]
	}
	return n, nil
}

func fileServer(t *testing.T, data []byte, delay time.Duration, served *atomic.Int64) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ww http.ResponseWriter = w
		if delay > 0 {
			ww = slow{w, delay}
		}
		if served != nil && r.Header.Get("Range") != "bytes=0-0" {
			served.Add(1)
		}
		w.Header().Set("ETag", `"x"`)
		http.ServeContent(ww, r, "", time.Time{}, bytes.NewReader(data))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

type env struct {
	m      *Manager
	db     string
	dl     string
	cancel context.CancelFunc
	done   chan error
}

func start(t *testing.T, dbPath, dl string) *env {
	t.Helper()
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	m := New(Options{DB: db, Downloads: dl, Client: http.DefaultClient, Connections: 4, Tick: 50 * time.Millisecond,
		NewEngine: func() (engine.Engine, error) {
			return engine.New(engine.Options{DataDir: dl, ListenHost: "127.0.0.1", NoDHT: true, NoPEX: true})
		}})
	ctx, cancel := context.WithCancel(context.Background())
	e := &env{m: m, db: dbPath, dl: dl, cancel: cancel, done: make(chan error, 1)}
	go func() { e.done <- m.Run(ctx) }()
	t.Cleanup(func() { e.stop(); db.Close() })
	return e
}

func (e *env) stop() {
	if e.cancel != nil {
		e.cancel()
		<-e.done
		e.cancel = nil
	}
}

func waitState(t *testing.T, m *Manager, id, want string, d time.Duration) store.Job {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		j, err := m.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if j.State == want {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s: state %q (error %q), want %q", id, j.State, j.Error, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWebJob(t *testing.T) {
	data := payload(3<<20, 1)
	url := fileServer(t, data, 0, nil)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "film.mkv"), []byte("existing"), 0o644) // must not be overwritten
	e := start(t, filepath.Join(dir, "j.db"), dir)
	j, _, err := e.m.Add(AddRequest{Source: url + "/film.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	j = waitState(t, e.m, j.ID, "done", 10*time.Second)
	if j.Path != "film (1).mkv" || j.Done != int64(len(data)) {
		t.Fatalf("job %+v", j)
	}
	got, _ := os.ReadFile(filepath.Join(dir, j.Path))
	if !bytes.Equal(got, data) {
		t.Fatal("content differs")
	}
	if _, err := os.Stat(filepath.Join(dir, j.Path+".part")); !os.IsNotExist(err) {
		t.Fatal(".part left behind")
	}
	if _, _, err := e.m.Add(AddRequest{Source: "file:///etc/passwd"}); err == nil {
		t.Fatal("file:// accepted")
	}
}

func TestPauseResumeAndCrashRecovery(t *testing.T) {
	data := payload(4<<20, 2)
	var served atomic.Int64
	url := fileServer(t, data, 5*time.Millisecond, &served)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "j.db")
	e := start(t, dbPath, dir)
	j, _, _ := e.m.Add(AddRequest{Source: url + "/big.bin"})
	waitState(t, e.m, j.ID, "downloading", 5*time.Second)
	time.Sleep(300 * time.Millisecond)

	if err := e.m.Pause(j.ID); err != nil {
		t.Fatal(err)
	}
	p := waitState(t, e.m, j.ID, "paused", 5*time.Second)
	if p.Done == 0 || p.Done >= int64(len(data)) {
		t.Fatalf("paused at %d", p.Done)
	}
	e.m.Resume(j.ID)
	waitState(t, e.m, j.ID, "downloading", 5*time.Second)
	time.Sleep(300 * time.Millisecond)

	// "Crash": stop the manager without pausing, start a new one on the same DB.
	e.stop()
	e2 := start(t, dbPath, dir)
	j2 := waitState(t, e2.m, j.ID, "done", 20*time.Second)
	got, _ := os.ReadFile(filepath.Join(dir, j2.Path))
	if !bytes.Equal(got, data) {
		t.Fatal("content differs after pause, resume and restart")
	}
}

func TestStreamWhileDownloading(t *testing.T) {
	data := payload(2<<20, 3)
	url := fileServer(t, data, 3*time.Millisecond, nil)
	dir := t.TempDir()
	e := start(t, filepath.Join(dir, "j.db"), dir)
	j, _, _ := e.m.Add(AddRequest{Source: url + "/v.mp4", Mode: "stream"})
	waitState(t, e.m, j.ID, "downloading", 5*time.Second)
	var r io.ReadSeekCloser
	var err error
	for i := 0; i < 50; i++ { // the runner may not have created its downloader yet
		if r, _, err = e.m.Open(context.Background(), j.ID, -1); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.Seek(1<<20, io.SeekStart) // ahead of the download: the reader waits for the bytes
	buf := make([]byte, 4096)
	if _, err := io.ReadFull(r, buf); err != nil || !bytes.Equal(buf, data[1<<20:1<<20+4096]) {
		t.Fatalf("stream read: %v", err)
	}
	if end, _ := r.Seek(0, io.SeekEnd); end != int64(len(data)) {
		t.Fatalf("size %d", end)
	}
}

func TestTorrentJob(t *testing.T) {
	// Seeder: an anacrolix client with our own content. Tracker: a local HTTP handler.
	sdir := t.TempDir()
	data := payload(1<<20+5, 4)
	os.WriteFile(filepath.Join(sdir, "clip.bin"), data, 0o644)
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir, cfg.NoDHT, cfg.DisablePEX, cfg.Seed, cfg.ListenPort = sdir, true, true, true, 0
	cfg.ListenHost = func(string) string { return "127.0.0.1" }
	cfg.DisableIPv6 = true
	seeder, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer seeder.Close()
	tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer := make([]byte, 6)
		copy(peer, net.ParseIP("127.0.0.1").To4())
		binary.BigEndian.PutUint16(peer[4:], uint16(seeder.LocalPort()))
		b, _ := bencode.Marshal(map[string]any{"interval": 60, "peers": string(peer)})
		w.Write(b)
	}))
	defer tracker.Close()
	info := metainfo.Info{PieceLength: 64 << 10}
	info.BuildFromFilePath(filepath.Join(sdir, "clip.bin"))
	ib, _ := bencode.Marshal(info)
	mi := &metainfo.MetaInfo{InfoBytes: ib, Announce: tracker.URL + "/announce"}
	st, _ := seeder.AddTorrent(mi)
	st.VerifyDataContext(context.Background())
	var buf bytes.Buffer
	mi.Write(&buf)

	dir := t.TempDir()
	e := start(t, filepath.Join(dir, "j.db"), dir)
	j, _, err := e.m.Add(AddRequest{Torrent: buf.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	if again, existed, _ := e.m.Add(AddRequest{Torrent: buf.Bytes()}); !existed || again.ID != j.ID {
		t.Fatal("duplicate torrent created a second job")
	}
	j = waitState(t, e.m, j.ID, "done", 20*time.Second)
	got, _ := os.ReadFile(filepath.Join(dir, "clip.bin"))
	if !bytes.Equal(got, data) || j.Name != "clip.bin" {
		t.Fatalf("torrent job %+v", j)
	}
	if err := e.m.Cancel(j.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "clip.bin")); !os.IsNotExist(err) {
		t.Fatal("cancel --delete-files left the file")
	}
}
