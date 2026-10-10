package engine

// Offline only (AGENTS.md rule 7): loopback peers, DHT and PEX off, self-generated content.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

func newEngine(t *testing.T, dir string) *engine {
	t.Helper()
	e, err := New(Options{DataDir: dir, ListenHost: "127.0.0.1", NoDHT: true, NoPEX: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e.(*engine)
}

func TestEngine(t *testing.T) {
	sdir := t.TempDir()
	data := make([]byte, 3<<20+123)
	r := rand.New(rand.NewChaCha8([32]byte{9}))
	for i := range data {
		data[i] = byte(r.Uint32())
	}
	os.WriteFile(filepath.Join(sdir, "movie.bin"), data, 0o644)
	info := metainfo.Info{PieceLength: 64 << 10}
	if err := info.BuildFromFilePath(filepath.Join(sdir, "movie.bin")); err != nil {
		t.Fatal(err)
	}
	ib, _ := bencode.Marshal(info)
	var buf bytes.Buffer
	(&metainfo.MetaInfo{InfoBytes: ib}).Write(&buf)

	seeder := newEngine(t, sdir)
	st, _, err := seeder.Add(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Recheck(context.Background()); err != nil {
		t.Fatal(err)
	}

	ldir := t.TempDir()
	leecher := newEngine(t, ldir)
	lt, isNew, err := leecher.Add(buf.Bytes())
	if err != nil || !isNew {
		t.Fatalf("add: %v new=%v", err, isNew)
	}
	if _, again, _ := leecher.Add(buf.Bytes()); again {
		t.Fatal("duplicate reported as new")
	}
	if got, ok := leecher.Get(lt.InfoHash()); !ok || got.InfoHash() != lt.InfoHash() {
		t.Fatal("Get failed")
	}
	lt.(*tor).t.AddClientPeer(seeder.cl)

	// Stream before starting the full download: reading a late offset must work.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := lt.WaitInfo(ctx); err != nil {
		t.Fatal(err)
	}
	rd := lt.Reader(0)
	rd.Seek(2<<20, io.SeekStart)
	chunk := make([]byte, 1000)
	if _, err := io.ReadFull(rd, chunk); err != nil || !bytes.Equal(chunk, data[2<<20:2<<20+1000]) {
		t.Fatalf("stream read: %v", err)
	}
	rd.Close()

	if sw := lt.Swarm(); sw.Peers != 1 || sw.Availability < 1 {
		t.Fatalf("swarm with one seeder: %+v", sw)
	}
	if ps := lt.Peers(); len(ps) != 1 || ps[0].Progress != 1 {
		t.Fatalf("peers: %+v", ps)
	}
	lt.SetTrackers([][]string{{"http://tracker.invalid/announce"}})
	if tr := lt.Trackers(); len(tr) != 1 || tr[0][0] != "http://tracker.invalid/announce" {
		t.Fatalf("trackers: %v", tr)
	}
	lt.Start()
	select {
	case <-lt.Done():
	case <-ctx.Done():
		t.Fatalf("incomplete: %+v", lt.Stats())
	}
	if s := lt.Stats(); s.Total != int64(len(data)) || len(lt.Files()) != 1 || len(lt.Pieces()) == 0 {
		t.Fatalf("stats %+v files %v", s, lt.Files())
	}
	got, _ := os.ReadFile(filepath.Join(ldir, "movie.bin"))
	if !bytes.Equal(got, data) {
		t.Fatal("data mismatch")
	}
	if err := lt.Drop(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ldir, "movie.bin")); !os.IsNotExist(err) {
		t.Fatal("Drop(true) left the file")
	}
}

func TestRejectV2Only(t *testing.T) {
	b, err := os.ReadFile("testdata/v2.torrent") // made by libtorrent from our own content
	if err != nil {
		t.Fatal(err)
	}
	e := newEngine(t, t.TempDir())
	if _, _, err := e.Add(b); !errors.Is(err, ErrV2Only) {
		t.Fatalf("want ErrV2Only, got %v", err)
	}
	if _, _, err := e.Add([]byte("not a torrent")); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestBlocklistSwap(t *testing.T) {
	var bl Blocklist
	if _, ok := bl.Lookup(net.ParseIP("127.0.0.1")); ok || bl.NumRanges() != 0 {
		t.Fatal("empty list blocks")
	}
	n, err := bl.Load(strings.NewReader("test:127.0.0.1-127.0.0.255\n"))
	if err != nil || n != 1 {
		t.Fatalf("load: %d %v", n, err)
	}
	if _, ok := bl.Lookup(net.ParseIP("127.0.0.7")); !ok {
		t.Fatal("not blocked after load")
	}
	bl.Load(strings.NewReader(""))
	if _, ok := bl.Lookup(net.ParseIP("127.0.0.7")); ok {
		t.Fatal("still blocked after swapping in an empty list")
	}
}
