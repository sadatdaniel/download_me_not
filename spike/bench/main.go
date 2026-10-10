// Command bench compares internal/httpdl with aria2c on a local server that throttles each
// connection, the way many CDNs do. aria2c (GPL) is only run as an external yardstick, never shipped.
// Loopback only (AGENTS.md rule 7). Run in the Go container after `apt-get install -y aria2`:
//
//	go run ./spike/bench -size 128 -rate 4 -slow 0.25
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/sadatdaniel/download_me_not/internal/httpdl"
)

type throttled struct {
	http.ResponseWriter
	bps int
}

func (w throttled) Write(p []byte) (int, error) {
	const chunk = 32 << 10
	n := 0
	for len(p) > 0 {
		c := min(len(p), chunk)
		k, err := w.ResponseWriter.Write(p[:c])
		n += k
		if err != nil {
			return n, err
		}
		time.Sleep(time.Duration(float64(c) / float64(w.bps) * float64(time.Second)))
		p = p[c:]
	}
	return n, nil
}

func main() {
	size := flag.Int("size", 128, "file size in MiB")
	rate := flag.Float64("rate", 4, "per-connection limit in MiB/s")
	slow := flag.Float64("slow", 0.25, "share of connections that get a 10x slower route")
	conns := flag.Int("n", 16, "connections")
	flag.Parse()

	data := make([]byte, *size<<20)
	rand.Read(data)
	sum := sha256.Sum256(data)
	var reqs atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bps := int(*rate * (1 << 20))
		// Deterministic "bad routes": every k-th request is 10x slower.
		if *slow > 0 && reqs.Add(1)%int64(1 / *slow) == 0 {
			bps /= 10
		}
		w.Header().Set("ETag", `"bench"`)
		http.ServeContent(throttled{w, bps}, r, "", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	dir, _ := os.MkdirTemp("", "bench")
	defer os.RemoveAll(dir)
	url := srv.URL + "/file.bin"

	fmt.Printf("file %d MiB, %.1f MiB/s per connection, %.0f%% slow routes, %d connections\n", *size, *rate, *slow*100, *conns)
	fmt.Printf("ideal (all connections at full speed): %.1fs\n\n", float64(*size) / (*rate * float64(*conns)))

	run := func(name string, f func(out string) error) {
		out := filepath.Join(dir, name)
		start := time.Now()
		if err := f(out); err != nil {
			fmt.Printf("%-24s error: %v\n", name, err)
			return
		}
		took := time.Since(start)
		b, _ := os.ReadFile(out)
		ok := sha256.Sum256(b) == sum
		fmt.Printf("%-24s %6.2fs  %6.1f MiB/s  verified=%v\n", name, took.Seconds(), float64(*size)/took.Seconds(), ok)
	}
	run("httpdl", func(out string) error {
		st, err := httpdl.Probe(context.Background(), http.DefaultClient, url, nil)
		if err != nil {
			return err
		}
		return httpdl.New(st, out, httpdl.Options{Client: http.DefaultClient, Connections: *conns}).Run(context.Background())
	})
	run("httpdl (1 connection)", func(out string) error {
		st, _ := httpdl.Probe(context.Background(), http.DefaultClient, url, nil)
		return httpdl.New(st, out, httpdl.Options{Client: http.DefaultClient, Connections: 1}).Run(context.Background())
	})
	if _, err := exec.LookPath("aria2c"); err != nil {
		fmt.Println("aria2c not installed: skipped")
		return
	}
	run("aria2c -x16 -s16 -k1M", func(out string) error {
		cmd := exec.Command("aria2c", "-q", "-x", fmt.Sprint(*conns), "-s", fmt.Sprint(*conns), "-k", "1M",
			"--file-allocation=none", "-d", filepath.Dir(out), "-o", filepath.Base(out), url)
		cmd.Stderr = io.Discard
		return cmd.Run()
	})
}
