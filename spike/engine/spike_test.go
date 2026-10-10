package engine

// Capability checks for anacrolix/torrent against scope §5.9.
// Run ONLY in the offline container (AGENTS.md rule 7):
//   docker compose -f compose.dev.yaml run --rm test go test -v ./spike/engine/ | grep -E "EVIDENCE|^(---|ok|FAIL)"
// All peers are in-process on loopback; DHT, PEX, UPnP are off; content is generated here.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/iplist"
	"github.com/anacrolix/torrent/metainfo"
	"golang.org/x/time/rate"
)

const pieceLen = 64 << 10

func evidence(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Logf("EVIDENCE %s: "+format, append([]any{t.Name()}, args...)...)
}

func newClient(t *testing.T, dir string, mod func(*torrent.ClientConfig)) *torrent.Client {
	t.Helper()
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dir
	cfg.ListenHost = func(string) string { return "127.0.0.1" }
	cfg.ListenPort = 0
	cfg.NoDHT = true
	cfg.DisablePEX = true
	cfg.NoDefaultPortForwarding = true
	cfg.DisableIPv6 = true
	cfg.Seed = true
	if mod != nil {
		mod(cfg)
	}
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	return cl
}

// writeRandom creates a file of deterministic random bytes.
func writeRandom(t *testing.T, path string, size int, seed uint64) []byte {
	t.Helper()
	b := make([]byte, size)
	r := rand.New(rand.NewChaCha8([32]byte{byte(seed)}))
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return b
}

func buildMI(t *testing.T, root string) (*metainfo.MetaInfo, metainfo.Info) {
	t.Helper()
	info := metainfo.Info{PieceLength: pieceLen}
	if err := info.BuildFromFilePath(root); err != nil {
		t.Fatal(err)
	}
	ib, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	return &metainfo.MetaInfo{InfoBytes: ib}, info
}

// swarm: a seeder holding one random file of the given size, plus the metainfo.
type swarm struct {
	seeder *torrent.Client
	mi     *metainfo.MetaInfo
	info   metainfo.Info
	data   []byte
	name   string
	dir    string
}

func newSwarm(t *testing.T, size int, seederMod func(*torrent.ClientConfig)) swarm {
	t.Helper()
	dir := t.TempDir()
	name := "content.bin"
	data := writeRandom(t, filepath.Join(dir, name), size, 1)
	mi, info := buildMI(t, filepath.Join(dir, name))
	s := newClient(t, dir, seederMod)
	st, err := s.AddTorrent(mi)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.VerifyDataContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitComplete(t, st, 10*time.Second)
	return swarm{s, mi, info, data, name, dir}
}

func magnet(t *testing.T, mi *metainfo.MetaInfo) string {
	t.Helper()
	m, err := mi.MagnetV2()
	if err != nil {
		t.Fatal(err)
	}
	return m.String()
}

func waitComplete(t *testing.T, tt *torrent.Torrent, d time.Duration) {
	t.Helper()
	select {
	case <-tt.Complete().On():
	case <-time.After(d):
		t.Fatalf("not complete after %v: %d/%d bytes", d, tt.BytesCompleted(), tt.Length())
	}
}

func limiter(bytesPerSec int) *rate.Limiter {
	return rate.NewLimiter(rate.Limit(bytesPerSec), 256<<10)
}

func sameFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(got) != sha256.Sum256(want) {
		t.Fatal("downloaded data differs from source")
	}
}

func TestBasicDownload(t *testing.T) {
	sw := newSwarm(t, 4<<20, nil)
	dir := t.TempDir()
	l := newClient(t, dir, nil)
	lt, _ := l.AddTorrent(sw.mi)
	lt.AddClientPeer(sw.seeder)
	lt.DownloadAll()
	start := time.Now()
	waitComplete(t, lt, 20*time.Second)
	sameFile(t, filepath.Join(dir, sw.name), sw.data)
	evidence(t, "4 MiB over loopback in %v, CGO_ENABLED=%s, data verified", time.Since(start).Round(time.Millisecond), os.Getenv("CGO_ENABLED"))
}

func TestMagnetMetadata(t *testing.T) {
	sw := newSwarm(t, 1<<20, nil)
	l := newClient(t, t.TempDir(), nil)
	lt, err := l.AddMagnet(magnet(t, sw.mi))
	if err != nil {
		t.Fatal(err)
	}
	if lt.Info() != nil {
		t.Fatal("info known before fetching")
	}
	lt.AddClientPeer(sw.seeder)
	select {
	case <-lt.GotInfo():
	case <-time.After(10 * time.Second):
		t.Fatal("no metadata")
	}
	evidence(t, "magnet -> metadata via GotInfo(); %d files; timeout is ours (select on GotInfo)", len(lt.Files()))
}

func TestDuplicate(t *testing.T) {
	sw := newSwarm(t, 1<<20, nil)
	l := newClient(t, t.TempDir(), nil)
	a, _ := l.AddTorrent(sw.mi)
	b, _ := l.AddMagnet(magnet(t, sw.mi))
	if a != b {
		t.Fatal("duplicate add created a second torrent")
	}
	evidence(t, ".torrent then magnet of same infohash -> same *Torrent; %d torrents in client", len(l.Torrents()))
}

func TestStreamSeek(t *testing.T) {
	const size = 16 << 20
	sw := newSwarm(t, size, func(c *torrent.ClientConfig) { c.UploadRateLimiter = limiter(2 << 20) }) // ~8 s full download
	dir := t.TempDir()
	l := newClient(t, dir, nil)
	lt, _ := l.AddTorrent(sw.mi)
	lt.AddClientPeer(sw.seeder)
	<-lt.GotInfo()
	f := lt.Files()[0]

	// HTTP Range over the torrent reader: what the stream endpoint will do.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rd := f.NewReader()
		defer rd.Close()
		rd.SetReadahead(1 << 20)
		http.ServeContent(w, r, sw.name, time.Time{}, rd)
	}))
	defer srv.Close()

	off := 12 << 20
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Range", "bytes="+strconv.Itoa(off)+"-"+strconv.Itoa(off+65535))
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	took := time.Since(start)
	if resp.StatusCode != 206 || !bytes.Equal(body, sw.data[off:off+65536]) {
		t.Fatalf("status %d, %d bytes, match=%v", resp.StatusCode, len(body), bytes.Equal(body, sw.data[off:off+65536]))
	}
	done := lt.BytesCompleted()
	if done >= size {
		t.Fatal("whole file was already downloaded; seek proves nothing")
	}
	evidence(t, "Range at 12 MiB of 16 MiB answered 206 in %v with %d/%d bytes done (seek moved priority)", took.Round(time.Millisecond), done, size)
}

func TestFirstLastPiecesFirst(t *testing.T) {
	const size = 16 << 20
	sw := newSwarm(t, size, func(c *torrent.ClientConfig) { c.UploadRateLimiter = limiter(1 << 20) })
	l := newClient(t, t.TempDir(), nil)
	lt, _ := l.AddTorrent(sw.mi)
	<-lt.GotInfo()
	n := lt.NumPieces()
	lt.Piece(0).SetPriority(torrent.PiecePriorityNow)
	lt.Piece(n - 1).SetPriority(torrent.PiecePriorityNow)
	lt.DownloadAll()
	lt.AddClientPeer(sw.seeder)
	deadline := time.Now().Add(10 * time.Second)
	for !(lt.PieceState(0).Complete && lt.PieceState(n-1).Complete) {
		if time.Now().After(deadline) {
			t.Fatal("first/last pieces not done")
		}
		time.Sleep(50 * time.Millisecond)
	}
	evidence(t, "first+last piece complete with %d/%d bytes done (Piece.SetPriority(Now))", lt.BytesCompleted(), size)
}

func TestGlobalRateLimit(t *testing.T) {
	sw := newSwarm(t, 3<<20, nil)
	l := newClient(t, t.TempDir(), func(c *torrent.ClientConfig) { c.DownloadRateLimiter = limiter(1 << 20) })
	lt, _ := l.AddTorrent(sw.mi)
	lt.AddClientPeer(sw.seeder)
	lt.DownloadAll()
	start := time.Now()
	waitComplete(t, lt, 20*time.Second)
	took := time.Since(start)
	if took < 1500*time.Millisecond {
		t.Fatalf("3 MiB at 1 MiB/s took only %v", took)
	}
	evidence(t, "3 MiB with DownloadRateLimiter 1 MiB/s took %v", took.Round(time.Millisecond))
}

func TestBlocklist(t *testing.T) {
	sw := newSwarm(t, 1<<20, nil)
	lo := net.ParseIP("127.0.0.1").To4()
	l := newClient(t, t.TempDir(), func(c *torrent.ClientConfig) {
		c.IPBlocklist = iplist.New([]iplist.Range{{First: lo, Last: lo, Description: "test"}})
	})
	lt, _ := l.AddTorrent(sw.mi)
	lt.AddClientPeer(sw.seeder)
	lt.DownloadAll()
	time.Sleep(2 * time.Second)
	if lt.BytesCompleted() != 0 || len(lt.PeerConns()) != 0 {
		t.Fatalf("blocked peer still connected: %d bytes", lt.BytesCompleted())
	}
	evidence(t, "IPBlocklist with 127.0.0.1 -> 0 peers, 0 bytes after 2s")
}

func TestEncryptionRequired(t *testing.T) {
	req := func(c *torrent.ClientConfig) {
		c.HeaderObfuscationPolicy = torrent.HeaderObfuscationPolicy{Preferred: true, RequirePreferred: true}
	}
	sw := newSwarm(t, 1<<20, req)
	l := newClient(t, t.TempDir(), req)
	lt, _ := l.AddTorrent(sw.mi)
	lt.AddClientPeer(sw.seeder)
	lt.DownloadAll()
	waitComplete(t, lt, 10*time.Second)

	// Mismatch: leecher refuses obfuscation, seeder requires it -> no data.
	l2 := newClient(t, t.TempDir(), func(c *torrent.ClientConfig) {
		c.HeaderObfuscationPolicy = torrent.HeaderObfuscationPolicy{Preferred: false, RequirePreferred: true}
	})
	lt2, _ := l2.AddTorrent(sw.mi)
	lt2.AddClientPeer(sw.seeder)
	lt2.DownloadAll()
	time.Sleep(2 * time.Second)
	evidence(t, "require-encryption on both sides completes; plaintext-only vs required: %d bytes (expect 0)", lt2.BytesCompleted())
}

func TestPauseResume(t *testing.T) {
	sw := newSwarm(t, 8<<20, func(c *torrent.ClientConfig) { c.UploadRateLimiter = limiter(2 << 20) })
	l := newClient(t, t.TempDir(), nil)
	lt, _ := l.AddTorrent(sw.mi)
	lt.AddClientPeer(sw.seeder)
	lt.DownloadAll()
	time.Sleep(time.Second)
	lt.DisallowDataDownload()
	time.Sleep(500 * time.Millisecond) // let in-flight requests land
	a := lt.BytesCompleted()
	time.Sleep(1500 * time.Millisecond)
	b := lt.BytesCompleted()
	if b-a > 2*pieceLen {
		t.Fatalf("still downloading while paused: %d -> %d", a, b)
	}
	lt.AllowDataDownload()
	waitComplete(t, lt, 20*time.Second)
	evidence(t, "DisallowDataDownload held at %d->%d bytes; AllowDataDownload completed", a, b)
}

func TestFileSelection(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "pack")
	a := writeRandom(t, filepath.Join(root, "a.bin"), 2<<20, 2)
	writeRandom(t, filepath.Join(root, "b.bin"), 2<<20, 3)
	mi, _ := buildMI(t, root)
	s := newClient(t, dir, nil)
	st, _ := s.AddTorrent(mi)
	st.VerifyDataContext(context.Background())
	waitComplete(t, st, 10*time.Second)

	ldir := t.TempDir()
	l := newClient(t, ldir, nil)
	lt, _ := l.AddTorrent(mi)
	<-lt.GotInfo()
	var fa, fb *torrent.File
	for _, f := range lt.Files() {
		if filepath.Base(f.Path()) == "a.bin" {
			fa = f
		} else {
			fb = f
		}
	}
	fb.SetPriority(torrent.PiecePriorityNone)
	fa.Download()
	lt.AddClientPeer(s)
	deadline := time.Now().Add(10 * time.Second)
	for fa.BytesCompleted() < fa.Length() {
		if time.Now().After(deadline) {
			t.Fatal("a.bin not done")
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(time.Second)
	sameFile(t, filepath.Join(ldir, "pack", "a.bin"), a)
	if fb.BytesCompleted() != 0 {
		t.Fatalf("skipped file got %d bytes", fb.BytesCompleted())
	}
	evidence(t, "a.bin downloaded, b.bin (PiecePriorityNone) has 0 bytes")
}

func TestWebSeed(t *testing.T) {
	dir := t.TempDir()
	name := "web.bin"
	data := writeRandom(t, filepath.Join(dir, name), 2<<20, 4)
	mi, _ := buildMI(t, filepath.Join(dir, name))
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer srv.Close()
	ldir := t.TempDir()
	l := newClient(t, ldir, nil)
	lt, _ := l.AddTorrent(mi)
	lt.AddWebSeeds([]string{srv.URL + "/" + name})
	lt.DownloadAll()
	waitComplete(t, lt, 15*time.Second)
	sameFile(t, filepath.Join(ldir, name), data)
	evidence(t, "no peers, BEP19 web seed on local HTTP server -> complete, data verified")
}

func TestRecheck(t *testing.T) {
	sw := newSwarm(t, 2<<20, nil)
	dir := t.TempDir()
	l := newClient(t, dir, nil)
	lt, _ := l.AddTorrent(sw.mi)
	lt.AddClientPeer(sw.seeder)
	lt.DownloadAll()
	waitComplete(t, lt, 10*time.Second)
	f, err := os.OpenFile(filepath.Join(dir, sw.name), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteAt(make([]byte, 1000), 300<<10) // corrupt one piece
	f.Close()
	if err := lt.VerifyDataContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := lt.BytesCompleted()
	if got != int64(len(sw.data))-pieceLen {
		t.Fatalf("after corrupting one piece BytesCompleted=%d", got)
	}
	lt.AddClientPeer(sw.seeder) // seeds drop each other once both are complete
	lt.DownloadAll()
	waitComplete(t, lt, 10*time.Second) // repaired from the seeder
	evidence(t, "VerifyData found the corrupted piece (%d bytes ok) and it was re-downloaded", got)
}

func TestResumeAfterRestart(t *testing.T) {
	sw := newSwarm(t, 8<<20, func(c *torrent.ClientConfig) { c.UploadRateLimiter = limiter(2 << 20) })
	dir := t.TempDir()
	l := newClient(t, dir, nil)
	lt, _ := l.AddTorrent(sw.mi)
	lt.AddClientPeer(sw.seeder)
	lt.DownloadAll()
	time.Sleep(1500 * time.Millisecond)
	before := lt.BytesCompleted()
	l.Close() // simulate crash/restart

	l2 := newClient(t, dir, nil)
	lt2, _ := l2.AddTorrent(sw.mi)
	<-lt2.GotInfo()
	time.Sleep(200 * time.Millisecond)
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	t.Logf("EVIDENCE %s: files after restart %v, BytesCompleted before verify %d", t.Name(), names, lt2.BytesCompleted())
	lt2.VerifyDataContext(context.Background())
	after := lt2.BytesCompleted()
	if after == 0 {
		t.Fatal("progress lost on restart")
	}
	lt2.AddClientPeer(sw.seeder)
	lt2.DownloadAll()
	waitComplete(t, lt2, 20*time.Second)
	sameFile(t, filepath.Join(dir, sw.name), sw.data)
	evidence(t, "had %d bytes, restart: 0 bytes until VerifyData, then %d bytes recovered from .part file without peers; then completed", before, after)
}

func TestPeersAndPieces(t *testing.T) {
	sw := newSwarm(t, 8<<20, func(c *torrent.ClientConfig) { c.UploadRateLimiter = limiter(2 << 20) })
	l := newClient(t, t.TempDir(), nil)
	lt, _ := l.AddTorrent(sw.mi)
	lt.AddClientPeer(sw.seeder)
	lt.DownloadAll()
	time.Sleep(1500 * time.Millisecond)
	pcs := lt.PeerConns()
	if len(pcs) == 0 {
		t.Fatal("no peer conns")
	}
	pc := pcs[0]
	st := lt.Stats()
	evidence(t, "peer addr=%v client=%q; stats: active=%d seeders=%d read=%d; piece runs: %s",
		pc.RemoteAddr, pc.PeerClientName.Load(), st.ActivePeers, st.ConnectedSeeders,
		st.BytesReadData.Int64(), lt.PieceStateRuns().String()[:min(80, len(lt.PieceStateRuns().String()))])
}

func TestUTPOnly(t *testing.T) {
	noTCP := func(c *torrent.ClientConfig) { c.DisableTCP = true }
	sw := newSwarm(t, 2<<20, noTCP)
	dir := t.TempDir()
	l := newClient(t, dir, noTCP)
	lt, _ := l.AddTorrent(sw.mi)
	lt.AddClientPeer(sw.seeder)
	lt.DownloadAll()
	waitComplete(t, lt, 15*time.Second)
	sameFile(t, filepath.Join(dir, sw.name), sw.data)
	evidence(t, "TCP disabled on both sides, completed over uTP with CGO_ENABLED=%s", os.Getenv("CGO_ENABLED"))
}

func TestTrackerAnnounce(t *testing.T) {
	sw := newSwarm(t, 1<<20, nil)
	var announces int
	tr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		announces++
		peer := make([]byte, 6)
		copy(peer, net.ParseIP("127.0.0.1").To4())
		binary.BigEndian.PutUint16(peer[4:], uint16(sw.seeder.LocalPort()))
		b, _ := bencode.Marshal(map[string]any{"interval": 60, "peers": string(peer)})
		w.Write(b)
	}))
	defer tr.Close()
	l := newClient(t, t.TempDir(), nil)
	lt, _ := l.AddTorrent(sw.mi)
	lt.AddTrackers([][]string{{tr.URL + "/announce"}})
	lt.DownloadAll()
	waitComplete(t, lt, 15*time.Second)
	evidence(t, "peer found only via local HTTP tracker (%d announces); AddTrackers/ModifyTrackers exist", announces)
}

func TestPathSanitization(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "pack")
	writeRandom(t, filepath.Join(root, "ok.bin"), 128<<10, 5)
	_, info := buildMI(t, root)
	info.Files[0].Path = []string{"..", "..", "evil.bin"}
	ib, _ := bencode.Marshal(info)
	mi := &metainfo.MetaInfo{InfoBytes: ib}
	ldir := filepath.Join(t.TempDir(), "a", "b")
	os.MkdirAll(ldir, 0o755)
	l := newClient(t, ldir, nil)
	lt, err := l.AddTorrent(mi)
	if err != nil {
		evidence(t, "AddTorrent rejected '..' path: %v", err)
		return
	}
	<-lt.GotInfo()
	evidence(t, "AddTorrent accepted '..' path; file path reported as %q -> must sanitize in our layer", lt.Files()[0].Path())
}

// v2-only and hybrid torrents made by libtorrent 2.0 from our own content (testdata/gen_v2.py).
// addV2Safe works around a v1.61 panic: AddTorrent on a v2-only torrent leaves the v1 short infohash zero.
// BEP 52 uses the SHA-256 infohash truncated to 20 bytes in its place.
func addV2Safe(cl *torrent.Client, mi *metainfo.MetaInfo) (_ *torrent.Torrent, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	spec, err := torrent.TorrentSpecFromMetaInfoErr(mi)
	if err != nil {
		return nil, err
	}
	if spec.InfoHash.IsZero() && spec.InfoHashV2.Ok {
		spec.InfoHash = *spec.InfoHashV2.Value.ToShort()
	}
	t, _, err := cl.AddTorrentSpec(spec)
	return t, err
}

func TestV2AndHybrid(t *testing.T) {
	for _, name := range []string{"v2", "hybrid"} {
		t.Run(name, func(t *testing.T) {
			mi, err := metainfo.LoadFromFile(filepath.Join("testdata", name+".torrent"))
			if err != nil {
				t.Fatal(err)
			}
			if name == "v2" {
				// Tried: AddTorrent panics (zero v1 infohash); with the truncated v2 hash it panics
				// "v1 and v2 info hashes should not be the same", then Client.Close panics too.
				evidence(t, "GAP: v2-only torrents panic in anacrolix v1.61 AddTorrent (hybrid works, see sibling)")
				return
			}
			sdir := t.TempDir()
			os.CopyFS(filepath.Join(sdir, "pack"), os.DirFS(filepath.Join("testdata", "pack")))
			s := newClient(t, sdir, nil)
			st, err := addV2Safe(s, mi)
			if err != nil {
				evidence(t, "GAP: cannot add libtorrent-made %s torrent: %v", name, err)
				return
			}
			st.VerifyDataContext(context.Background())
			waitComplete(t, st, 10*time.Second)
			ldir := t.TempDir()
			l := newClient(t, ldir, nil)
			lt, err := addV2Safe(l, mi)
			if err != nil {
				t.Fatal(err)
			}
			lt.AddClientPeer(s)
			lt.DownloadAll()
			waitComplete(t, lt, 15*time.Second)
			for _, f := range []string{"a.bin", "b.bin"} {
				want, _ := os.ReadFile(filepath.Join("testdata", "pack", f))
				sameFile(t, filepath.Join(ldir, "pack", f), want)
			}
			info, _ := mi.UnmarshalInfo()
			evidence(t, "libtorrent-made %s torrent (HasV1=%v HasV2=%v) seeded and downloaded, data verified", name, info.HasV1(), info.HasV2())
		})
	}
}
