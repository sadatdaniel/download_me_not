package jobs

// Offline (AGENTS.md rule 7): loopback HTTP servers, an in-process seeder and tracker, DHT off.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"github.com/sadatdaniel/download_me_not/internal/cookies"
	"github.com/sadatdaniel/download_me_not/internal/engine"
	"github.com/sadatdaniel/download_me_not/internal/store"
	"github.com/sadatdaniel/download_me_not/internal/video"
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

// swarmFixture starts a seeder (anacrolix client, our own content) and a local HTTP tracker
// that points to it. It returns the .torrent bytes and the content.
func swarmFixture(t *testing.T) ([]byte, []byte) {
	t.Helper()
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
	t.Cleanup(func() { seeder.Close() })
	tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer := make([]byte, 6)
		copy(peer, net.ParseIP("127.0.0.1").To4())
		binary.BigEndian.PutUint16(peer[4:], uint16(seeder.LocalPort()))
		b, _ := bencode.Marshal(map[string]any{"interval": 60, "peers": string(peer)})
		w.Write(b)
	}))
	t.Cleanup(tracker.Close)
	info := metainfo.Info{PieceLength: 64 << 10}
	info.BuildFromFilePath(filepath.Join(sdir, "clip.bin"))
	ib, _ := bencode.Marshal(info)
	mi := &metainfo.MetaInfo{InfoBytes: ib, Announce: tracker.URL + "/announce"}
	st, _ := seeder.AddTorrent(mi)
	st.VerifyDataContext(context.Background())
	var buf bytes.Buffer
	mi.Write(&buf)
	return buf.Bytes(), data
}

func TestTorrentJob(t *testing.T) {
	tb, data := swarmFixture(t)
	buf := bytes.NewBuffer(tb)
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

func TestHint(t *testing.T) {
	m := New(Options{})
	for _, c := range []struct {
		sw   engine.Swarm
		st   engine.Stats
		want string
	}{
		{engine.Swarm{}, engine.Stats{}, "Fetching metadata"},
		{engine.Swarm{Pieces: 10}, engine.Stats{Total: 100}, "No peers connected"},
		{engine.Swarm{Pieces: 10, Peers: 3, Availability: 0.6}, engine.Stats{Total: 100}, "No complete copy"},
		{engine.Swarm{Pieces: 10, Peers: 3, Seeders: 1, Availability: 1.2}, engine.Stats{Total: 100, DownRate: 100}, "sending little data"},
		{engine.Swarm{Pieces: 10, Peers: 3, Seeders: 1}, engine.Stats{Total: 100, Completed: 100}, "Complete"},
	} {
		if got := m.hint(c.sw, c.st); !strings.Contains(got, c.want) {
			t.Errorf("%+v: %q, want %q", c.sw, got, c.want)
		}
	}
}

func TestDiskGuardAndPriority(t *testing.T) {
	data := payload(512<<10, 5)
	url := fileServer(t, data, 2*time.Millisecond, nil)
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var free atomic.Int64 // starts at 0: disk "full"
	m := New(Options{DB: db, Downloads: dir, Client: http.DefaultClient, MaxActive: 1, Tick: 20 * time.Millisecond,
		DiskReserve: 1 << 20, FreeSpace: func(string) (int64, error) { return free.Load(), nil }})
	a, _, _ := m.Add(AddRequest{Source: url + "/a.bin"})
	b, _, _ := m.Add(AddRequest{Source: url + "/b.bin"})
	c, _, _ := m.Add(AddRequest{Source: url + "/c.bin", Priority: 5})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- m.Run(ctx) }()
	defer func() { cancel(); <-done }()

	time.Sleep(200 * time.Millisecond)
	if j, _ := m.Get(a.ID); j.State != "queued" || !strings.Contains(j.Error, "disk space") {
		t.Fatalf("disk full but job %q (%q)", j.State, j.Error)
	}
	free.Store(1 << 40)
	// c has the highest priority, so it runs first; a and b wait (one slot).
	waitState(t, m, c.ID, "done", 10*time.Second)
	waitState(t, m, a.ID, "done", 10*time.Second)
	j := waitState(t, m, b.ID, "done", 10*time.Second)
	ja, _ := m.Get(a.ID)
	jc, _ := m.Get(c.ID)
	if !(jc.Updated <= ja.Updated && ja.Updated <= j.Updated) || ja.Error != "" {
		t.Fatalf("order or error wrong: %+v %+v %+v", jc, ja, j)
	}
}

func TestSeedingFreesSlot(t *testing.T) {
	tb, _ := swarmFixture(t)
	web := fileServer(t, payload(256<<10, 6), 0, nil)
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := New(Options{DB: db, Downloads: dir, Client: http.DefaultClient, MaxActive: 1, Tick: 20 * time.Millisecond,
		NewEngine: func() (engine.Engine, error) {
			return engine.New(engine.Options{DataDir: dir, ListenHost: "127.0.0.1", NoDHT: true, NoPEX: true})
		}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- m.Run(ctx) }()
	defer func() { cancel(); <-done }()
	tj, _, err := m.Add(AddRequest{Torrent: tb, Seed: &SeedPolicy{Policy: "forever"}})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, tj.ID, "seeding", 20*time.Second)
	wj, _, _ := m.Add(AddRequest{Source: web + "/w.bin"})
	waitState(t, m, wj.ID, "done", 10*time.Second) // ran although the only slot holds a seeding torrent
	if files, err := m.Files(tj.ID); err != nil || len(files) != 1 {
		t.Fatalf("files: %v %v", files, err)
	}
	if err := m.SetFilePriority(tj.ID, 0, "skip"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetFilePriority(tj.ID, 5, "skip"); err == nil {
		t.Fatal("bad index accepted")
	}
}

func TestUnpackAfterDownload(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("inside/readme.txt")
	w.Write([]byte("unpacked"))
	zw.Close()
	url := fileServer(t, buf.Bytes(), 0, nil)
	dir := t.TempDir()
	e := start(t, filepath.Join(dir, "j.db"), dir)
	j, _, _ := e.m.Add(AddRequest{Source: url + "/bundle.zip", Post: &Post{Extract: true, DeleteArchive: true}})
	j = waitState(t, e.m, j.ID, "done", 10*time.Second)
	if b, err := os.ReadFile(filepath.Join(dir, "bundle", "inside", "readme.txt")); err != nil || string(b) != "unpacked" || j.Error != "" {
		t.Fatalf("unpack: %q %v (job error %q)", b, err, j.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, "bundle.zip")); !os.IsNotExist(err) {
		t.Fatal("archive not deleted")
	}
}

func TestPublicTrackers(t *testing.T) {
	got := publicTrackers([][]string{
		{"udp://tracker.example:1337/announce", "http://t.example/announce", "udp://tracker.example:1337/announce"},
		{"https://private.example/announce?passkey=abc", "https://user:pw@t.example/announce"},
		{"https://private.example/0123456789abcdef0123456789abcdef/announce", "file:///etc/passwd"},
	})
	if len(got) != 2 || got[0] != "udp://tracker.example:1337/announce" {
		t.Fatalf("%v", got)
	}
}

func TestSwarmMemoryLearns(t *testing.T) {
	tb, _ := swarmFixture(t)
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := New(Options{DB: db, Downloads: dir, Client: http.DefaultClient, Tick: 20 * time.Millisecond, LearnTrackers: true,
		NewEngine: func() (engine.Engine, error) {
			return engine.New(engine.Options{DataDir: dir, ListenHost: "127.0.0.1", NoDHT: true, NoPEX: true})
		}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- m.Run(ctx) }()
	defer func() { cancel(); <-done }()
	j, _, _ := m.Add(AddRequest{Torrent: tb})
	waitState(t, m, j.ID, "done", 20*time.Second)
	top, _ := store.TopTrackers(db, 10)
	if len(top) != 1 || !strings.HasSuffix(top[0].URL, "/announce") || top[0].Success != 1 {
		t.Fatalf("learned: %+v", top)
	}
}

func TestKillSwitch(t *testing.T) {
	tb, _ := swarmFixture(t)
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var down atomic.Bool
	down.Store(true)
	m := New(Options{DB: db, Downloads: dir, Client: http.DefaultClient, Tick: 20 * time.Millisecond,
		TorrentGate: func() string {
			if down.Load() {
				return "kill switch: wg0 is down"
			}
			return ""
		},
		NewEngine: func() (engine.Engine, error) {
			return engine.New(engine.Options{DataDir: dir, ListenHost: "127.0.0.1", NoDHT: true, NoPEX: true})
		}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- m.Run(ctx) }()
	defer func() { cancel(); <-done }()
	j, _, _ := m.Add(AddRequest{Torrent: tb, Mode: "stream"})
	time.Sleep(200 * time.Millisecond)
	if got, _ := m.Get(j.ID); got.State != "queued" || got.Error != "kill switch: wg0 is down" {
		t.Fatalf("gate closed but job %q %q", got.State, got.Error)
	}
	down.Store(false)
	if got := waitState(t, m, j.ID, "done", 20*time.Second); got.Error != "" {
		t.Fatalf("error not cleared: %q", got.Error)
	}
}

// A page URL (not a file) becomes a video job handled by yt-dlp. Offline: local page, ffmpeg clip.
func TestVideoJob(t *testing.T) {
	bin, err := exec.LookPath("yt-dlp")
	if err != nil {
		t.Skip("yt-dlp not installed (use the dev container)")
	}
	dir := t.TempDir()
	clip := filepath.Join(t.TempDir(), "clip.mp4")
	if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=1:size=160x120:rate=10",
		"-c:v", "libx264", clip).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg: %v %s", err, out)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/watch", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><title>Clip</title></head><body><video src="/clip.mp4"></video></body></html>`))
	})
	mux.HandleFunc("/clip.mp4", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, clip) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	db, err := store.Open(filepath.Join(dir, "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := New(Options{DB: db, Downloads: dir, Client: http.DefaultClient, Tick: 20 * time.Millisecond,
		Video: func() *video.Runner { return &video.Runner{Bin: bin} }})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- m.Run(ctx) }()
	defer func() { cancel(); <-done }()

	pr, err := m.Probe(ctx, srv.URL+"/watch", nil)
	if err != nil || pr.Kind != "video" || len(pr.Video.Formats) == 0 {
		t.Fatalf("probe: %+v %v", pr, err)
	}
	j, _, _ := m.Add(AddRequest{Source: srv.URL + "/watch"}) // no format: detected as a page
	j = waitState(t, m, j.ID, "done", time.Minute)
	if j.Kind != "video" || j.Path == "" || j.Done == 0 {
		t.Fatalf("video job %+v", j)
	}
	if rd, _, err := m.Open(ctx, j.ID, -1); err != nil {
		t.Fatal(err)
	} else {
		rd.Close()
	}
	if _, _, err := m.Add(AddRequest{Source: srv.URL + "/watch", Format: "--exec=x"}); err == nil {
		t.Fatal("bad format accepted")
	}
}

func TestCookiesSentOverHTTPS(t *testing.T) {
	data := payload(64<<10, 8)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("sid"); err != nil || c.Value != "s3" || r.UserAgent() != "TestBrowser/1" {
			http.Error(w, "login required", http.StatusForbidden)
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	dir := t.TempDir()
	ck, err := cookies.Open(filepath.Join(dir, "cookies"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	ck.Import("127.0.0.1", strings.NewReader("127.0.0.1\tFALSE\t/\tTRUE\t0\tsid\ts3\n"), "TestBrowser/1")
	db, _ := store.Open(filepath.Join(dir, "j.db"))
	defer db.Close()
	m := New(Options{DB: db, Downloads: dir, Client: srv.Client(), Tick: 20 * time.Millisecond, Cookies: ck})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- m.Run(ctx) }()
	defer func() { cancel(); <-done }()
	j, _, _ := m.Add(AddRequest{Source: srv.URL + "/private.bin"})
	j = waitState(t, m, j.ID, "done", 10*time.Second)
	if strings.Contains(string(j.Meta), "s3") {
		t.Fatal("cookie value stored in the job database")
	}
}
