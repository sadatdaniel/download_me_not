// Package selftest proves an install works end to end using only local fixtures: an in-process
// HTTP server and a loopback torrent swarm with DHT off. Nothing touches the internet
// (AGENTS.md rule 7). Used by `dmn doctor --selftest`, and later as the post-update health check.
package selftest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/anacrolix/torrent/bencode"

	"github.com/sadatdaniel/download_me_not/internal/engine"
	"github.com/sadatdaniel/download_me_not/internal/fsapi"
	"github.com/sadatdaniel/download_me_not/internal/httpdl"
)

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Took   string `json:"took"`
}

// Run executes every check in a temporary folder and removes it afterwards.
func Run(ctx context.Context) []Check {
	dir, err := os.MkdirTemp("", "dmn-selftest-")
	if err != nil {
		return []Check{{Name: "tempdir", Detail: err.Error()}}
	}
	defer os.RemoveAll(dir)
	data := make([]byte, 4<<20)
	rand.Read(data)
	sum := sha256.Sum256(data)
	src := filepath.Join(dir, "src")
	os.MkdirAll(src, 0o755)
	if err := os.WriteFile(filepath.Join(src, "sample.bin"), data, 0o644); err != nil {
		return []Check{{Name: "write", Detail: err.Error()}}
	}

	var out []Check
	run := func(name string, f func() (string, error)) {
		start := time.Now()
		detail, err := f()
		c := Check{Name: name, OK: err == nil, Detail: detail, Took: time.Since(start).Round(time.Millisecond).String()}
		if err != nil {
			c.Detail = err.Error()
		}
		out = append(out, c)
	}

	dl := filepath.Join(dir, "downloads")
	os.MkdirAll(dl, 0o755)
	run("http download (8 connections, resumable)", func() (string, error) {
		srv := httptest.NewServer(http.FileServer(http.Dir(src)))
		defer srv.Close()
		st, err := httpdl.Probe(ctx, http.DefaultClient, srv.URL+"/sample.bin", nil)
		if err != nil {
			return "", err
		}
		d := httpdl.New(st, filepath.Join(dl, "http.bin"), httpdl.Options{Client: http.DefaultClient, Connections: 8, MinSplit: 256 << 10})
		if err := d.Run(ctx); err != nil {
			return "", err
		}
		return verify(filepath.Join(dl, "http.bin"), sum)
	})

	run("streaming with seek (HTTP Range)", func() (string, error) {
		f, err := fsapi.New(dl, []byte("selftest"))
		if err != nil {
			return "", err
		}
		defer f.Close()
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Range", "bytes=1048576-1052671")
		rec := httptest.NewRecorder()
		f.Serve(rec, req, "/http.bin")
		if rec.Code != http.StatusPartialContent || !bytes.Equal(rec.Body.Bytes(), data[1<<20:1<<20+4096]) {
			return "", fmt.Errorf("range read failed: HTTP %d", rec.Code)
		}
		return "206 Partial Content, bytes match", nil
	})

	run("torrent (loopback swarm, DHT off)", func() (string, error) {
		opts := func(d string) engine.Options {
			return engine.Options{DataDir: d, ListenHost: "127.0.0.1", NoDHT: true, NoPEX: true}
		}
		seeder, err := engine.New(opts(src))
		if err != nil {
			return "", err
		}
		defer seeder.Close()
		tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer := make([]byte, 6)
			copy(peer, net.IPv4(127, 0, 0, 1).To4())
			binary.BigEndian.PutUint16(peer[4:], uint16(seeder.Port()))
			b, _ := bencode.Marshal(map[string]any{"interval": 60, "peers": string(peer)})
			w.Write(b)
		}))
		defer tracker.Close()
		tb, err := engine.Create(filepath.Join(src, "sample.bin"), []string{tracker.URL + "/announce"})
		if err != nil {
			return "", err
		}
		st, _, err := seeder.Add(tb)
		if err != nil {
			return "", err
		}
		if err := st.Recheck(ctx); err != nil {
			return "", err
		}
		tdir := filepath.Join(dir, "torrent")
		leecher, err := engine.New(opts(tdir))
		if err != nil {
			return "", err
		}
		defer leecher.Close()
		lt, _, err := leecher.Add(tb)
		if err != nil {
			return "", err
		}
		lt.Start()
		select {
		case <-lt.Done():
		case <-time.After(30 * time.Second):
			return "", fmt.Errorf("incomplete after 30s: %d/%d bytes", lt.Stats().Completed, len(data))
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return verify(filepath.Join(tdir, "sample.bin"), sum)
	})
	return out
}

func verify(path string, want [32]byte) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(h.Sum(nil), want[:]) {
		return "", fmt.Errorf("sha256 mismatch")
	}
	return fmt.Sprintf("%d bytes, sha256 verified", n), nil
}
