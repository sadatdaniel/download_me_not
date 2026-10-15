package tools

// A fake GitHub release server: no internet. The "binaries" are tiny shell scripts.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRelease struct {
	tag   string
	files map[string][]byte
}

func fakeGitHub(t *testing.T, rels map[string]*fakeRelease) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for repo, rel := range rels {
			if r.URL.Path == "/repos/"+repo+"/releases/latest" {
				type asset struct {
					Name string `json:"name"`
					URL  string `json:"browser_download_url"`
				}
				out := struct {
					Tag    string  `json:"tag_name"`
					Assets []asset `json:"assets"`
				}{Tag: rel.tag}
				for n := range rel.files {
					out.Assets = append(out.Assets, asset{n, srv.URL + "/dl/" + repo + "/" + n})
				}
				json.NewEncoder(w).Encode(out)
				return
			}
			if f, ok := strings.CutPrefix(r.URL.Path, "/dl/"+repo+"/"); ok {
				w.Write(rel.files[f])
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func script(version string, ok bool) []byte {
	code := 0
	if !ok {
		code = 1
	}
	return []byte(fmt.Sprintf("#!/bin/sh\necho %s\nexit %d\n", version, code))
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// exitsOK stands in for the real work checks with the fake script tools: they only have to run.
func exitsOK(ctx context.Context, _, exe string) error { return exec.CommandContext(ctx, exe).Run() }

func TestInstallUpdateRollback(t *testing.T) {
	v1, v2, bad := script("2026.01.01", true), script("2026.02.02", true), script("broken", false)
	rel := &fakeRelease{tag: "2026.01.01", files: map[string][]byte{
		"yt-dlp_linux": v1, "SHA2-256SUMS": []byte(sum(v1) + "  yt-dlp_linux\n" + sum([]byte("x")) + "  yt-dlp_macos\n")}}
	gh := fakeGitHub(t, map[string]*fakeRelease{"yt-dlp/yt-dlp": rel})
	no := false
	in := Installer{Client: gh.Client(), API: gh.URL, DataDir: t.TempDir(), Arch: "amd64", Musl: &no, check: exitsOK}
	ctx := context.Background()

	if tag, err := in.Install(ctx, "yt-dlp"); err != nil || tag != "2026.01.01" {
		t.Fatalf("install: %q %v", tag, err)
	}
	if p, ok := Find("yt-dlp", in.DataDir); !ok || filepath.Dir(p) != filepath.Join(in.DataDir, "bin") {
		t.Fatalf("not found after install: %s", p)
	}

	// Corrupted download: checksum mismatch, old version stays.
	rel.tag, rel.files["yt-dlp_linux"] = "2026.02.02", v2
	if _, err := in.Install(ctx, "yt-dlp"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want checksum error, got %v", err)
	}
	// Broken release: passes the checksum but fails the self-test, old version stays.
	rel.files["yt-dlp_linux"], rel.files["SHA2-256SUMS"] = bad, []byte(sum(bad)+"  yt-dlp_linux\n")
	if _, err := in.Install(ctx, "yt-dlp"); err == nil || !strings.Contains(err.Error(), "self-test") {
		t.Fatalf("want self-test error, got %v", err)
	}
	if in.Installed("yt-dlp") != "2026.01.01" {
		t.Fatal("a failed update replaced the tool")
	}
	// Good update, then rollback.
	rel.files["yt-dlp_linux"], rel.files["SHA2-256SUMS"] = v2, []byte(sum(v2)+"  yt-dlp_linux\n")
	if tag, err := in.Install(ctx, "yt-dlp"); err != nil || tag != "2026.02.02" {
		t.Fatalf("update: %q %v", tag, err)
	}
	if err := in.Rollback("yt-dlp"); err != nil || in.Installed("yt-dlp") != "2026.01.01" {
		t.Fatalf("rollback: %v %q", err, in.Installed("yt-dlp"))
	}
	if b, _ := os.ReadFile(filepath.Join(in.DataDir, "bin", "yt-dlp")); !bytes.Equal(b, v1) {
		t.Fatal("rollback did not restore the old binary")
	}
}

func TestZipToolAndPerFileSums(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("deno")
	w.Write(script("deno 2.9.7", true))
	zw.Close()
	z := buf.Bytes()
	name := "deno-x86_64-unknown-linux-gnu.zip"
	gh := fakeGitHub(t, map[string]*fakeRelease{"denoland/deno": {tag: "v2.9.7", files: map[string][]byte{
		name: z, name + ".sha256sum": []byte(sum(z) + "\n")}}})
	no := false
	in := Installer{Client: gh.Client(), API: gh.URL, DataDir: t.TempDir(), Arch: "amd64", Musl: &no, check: exitsOK}
	if tag, err := in.Install(context.Background(), "deno"); err != nil || tag != "v2.9.7" {
		t.Fatalf("%q %v", tag, err)
	}
	yes := true
	in.Musl = &yes
	if _, err := in.Install(context.Background(), "deno"); err == nil {
		t.Fatal("deno has no musl build; should refuse")
	}
	if _, err := in.Install(context.Background(), "ffmpeg"); err == nil {
		t.Fatal("ffmpeg is not managed")
	}
}

func denoZip(version string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("deno")
	w.Write(script("deno "+version, true))
	zw.Close()
	return buf.Bytes()
}

// The daily update waits for a person before a new major version of deno or rclone; asking for it installs it.
func TestUpdateStaysWithinMajor(t *testing.T) {
	name := "deno-x86_64-unknown-linux-gnu.zip"
	rel := &fakeRelease{tag: "v2.9.7", files: map[string][]byte{}}
	set := func(tag string) {
		z := denoZip(tag)
		rel.tag, rel.files[name], rel.files[name+".sha256sum"] = tag, z, []byte(sum(z)+"\n")
	}
	gh := fakeGitHub(t, map[string]*fakeRelease{"denoland/deno": rel})
	no := false
	in := Installer{Client: gh.Client(), API: gh.URL, DataDir: t.TempDir(), Arch: "amd64", Musl: &no, check: exitsOK}
	ctx := context.Background()
	set("v2.9.7")
	in.Install(ctx, "deno")
	set("v2.10.0")
	if tag, err := in.Update(ctx, "deno"); err != nil || tag != "v2.10.0" {
		t.Fatalf("a minor update goes in by itself: %q %v", tag, err)
	}
	set("v3.0.0")
	if _, err := in.Update(ctx, "deno"); err == nil || !strings.Contains(err.Error(), "major") || in.Installed("deno") != "v2.10.0" {
		t.Fatalf("a new major version must wait: %v (installed %s)", err, in.Installed("deno"))
	}
	if tag, err := in.Install(ctx, "deno"); err != nil || tag != "v3.0.0" {
		t.Fatalf("asked for, it installs: %q %v", tag, err)
	}
}

// The real checks, against the real tools where the machine has them (the dev container has yt-dlp). A tool that
// starts but can't do the job (here: prints nothing) fails.
func TestWorkChecks(t *testing.T) {
	ctx := context.Background()
	found := 0
	for _, name := range []string{"yt-dlp", "rclone", "deno"} {
		exe, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		found++
		if err := works(ctx, name, exe); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if found == 0 {
		t.Log("none of yt-dlp, rclone, deno installed here: only the failure case runs")
	}
	broken := filepath.Join(t.TempDir(), "yt-dlp")
	os.WriteFile(broken, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	if err := works(ctx, "yt-dlp", broken); err == nil {
		t.Fatal("a yt-dlp that finds nothing must fail the check")
	}
	if major("v1.68.2") != "1" || major("2026.10.14") != "2026" {
		t.Fatal("major")
	}
}
