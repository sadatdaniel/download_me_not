package engine

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"golang.org/x/time/rate"
)

// Reading pieces that are already on disk must not be throttled by the download limit or crawl in small reads:
// players stream finished parts through Reader. Offline: one engine, self-generated data, no peers.
func TestReaderSpeedOnCompleteData(t *testing.T) {
	for _, limited := range []bool{false, true} {
		dir := t.TempDir()
		data := bytes.Repeat([]byte("0123456789abcdef"), 40<<20/16)
		os.WriteFile(filepath.Join(dir, "v.bin"), data, 0o644)
		info := metainfo.Info{PieceLength: 256 << 10}
		if err := info.BuildFromFilePath(filepath.Join(dir, "v.bin")); err != nil {
			t.Fatal(err)
		}
		ib, _ := bencode.Marshal(info)
		var mi bytes.Buffer
		(&metainfo.MetaInfo{InfoBytes: ib}).Write(&mi)
		o := Options{DataDir: dir, ListenHost: "127.0.0.1", NoDHT: true, NoPEX: true}
		if limited {
			o.DownLimiter = rate.NewLimiter(450<<10, 256<<10)
		}
		e, err := New(o)
		if err != nil {
			t.Fatal(err)
		}
		tr, _, err := e.Add(mi.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if err := tr.Recheck(context.Background()); err != nil {
			t.Fatal(err)
		}
		rd := tr.Reader(0)
		start := time.Now()
		n, err := io.CopyBuffer(io.Discard, rd, make([]byte, 32<<10)) // like http.ServeContent
		el := time.Since(start)
		rd.Close()
		e.Close()
		if err != nil || n != int64(len(data)) {
			t.Fatalf("read %d of %d: %v", n, len(data), err)
		}
		mbps := float64(n) / 1e6 / el.Seconds()
		t.Logf("limited=%v: 40 MB in %v (%.0f MB/s)", limited, el.Round(time.Millisecond), mbps)
		if mbps < 20 {
			t.Errorf("limited=%v: reading complete data runs at %.1f MB/s; it must not be throttled", limited, mbps)
		}
	}
}

// Stream mode must download in playback order: with the window moving forward, what has arrived after a few
// seconds is (almost) one run from the start, not pieces scattered over the file. Offline loopback swarm.
func TestStreamWindowIsSequential(t *testing.T) {
	sdir := t.TempDir()
	data := make([]byte, 24<<20)
	for i := range data {
		data[i] = byte(i * 7 % 251)
	}
	os.WriteFile(filepath.Join(sdir, "v.bin"), data, 0o644)
	info := metainfo.Info{PieceLength: 64 << 10}
	if err := info.BuildFromFilePath(filepath.Join(sdir, "v.bin")); err != nil {
		t.Fatal(err)
	}
	ib, _ := bencode.Marshal(info)
	var mi bytes.Buffer
	(&metainfo.MetaInfo{InfoBytes: ib}).Write(&mi)
	seeder := newEngine(t, sdir)
	st, _, _ := seeder.Add(mi.Bytes())
	if err := st.Recheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	le, err := New(Options{DataDir: t.TempDir(), ListenHost: "127.0.0.1", NoDHT: true, NoPEX: true,
		DownLimiter: rate.NewLimiter(2<<20, 256<<10)})
	if err != nil {
		t.Fatal(err)
	}
	defer le.Close()
	lt, _, _ := le.Add(mi.Bytes())
	lt.(*tor).t.AddClientPeer(seeder.cl)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := lt.WaitInfo(ctx); err != nil {
		t.Fatal(err)
	}
	lt.Pause() // same order as the job runner: priorities first, then data
	lt.Start()
	lt.PrioritizeEnds(0)
	lt.StreamWindow(0, 2<<20)
	lt.Resume()
	for range 6 { // the runner moves the window every 2 s
		time.Sleep(500 * time.Millisecond)
		lt.StreamWindow(0, 2<<20)
	}
	// Pieces completed far beyond the window mean bandwidth went elsewhere than the playhead. A few strays are
	// expected: the last piece (players read the index there) and requests in flight before the first window.
	tt := lt.(*tor).t
	first, total, beyond := -1, 0, 0
	for i := range tt.NumPieces() {
		c := tt.Piece(i).State().Complete
		if !c && first < 0 {
			first = i
		}
		if c {
			total++
			if first >= 0 && i >= first+32 { // 2 MiB window / 64 KiB pieces
				beyond++
			}
		}
	}
	t.Logf("%d pieces complete, first missing %d, %d beyond the window", total, first, beyond)
	if total < 10 || beyond > 2+total/20 {
		t.Fatalf("not sequential: %d of %d complete pieces are beyond the window", beyond, total)
	}
}
