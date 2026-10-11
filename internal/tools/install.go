package tools

// Managed installs and updates of helper tools from their official GitHub releases (scope §5.8).
// One path for all tools: download, verify SHA-256 against the project's published checksums,
// run a self-test, keep the previous copy, then switch. A bad update never replaces a working tool,
// and Rollback restores the previous one.

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type source struct {
	repo    string
	asset   func(arch string, musl bool) string // regexp for the release asset, "" = unsupported here
	sums    func(asset string) string           // name of the checksum asset
	inner   string                              // binary name inside a zip; "" = the asset is the binary
	selfArg string                              // argument for the self-test
}

var sources = map[string]source{
	"yt-dlp": {repo: "yt-dlp/yt-dlp", selfArg: "--version", sums: func(string) string { return "SHA2-256SUMS" },
		asset: func(arch string, musl bool) string {
			m := map[string]string{"amd64": "yt-dlp_linux", "arm64": "yt-dlp_linux_aarch64", "arm": "yt-dlp_linux_armv7l"}
			if musl {
				m = map[string]string{"amd64": "yt-dlp_musllinux", "arm64": "yt-dlp_musllinux_aarch64"}
			}
			if a := m[arch]; a != "" {
				return "^" + a + "$"
			}
			return ""
		}},
	"rclone": {repo: "rclone/rclone", selfArg: "version", inner: "rclone", sums: func(string) string { return "SHA256SUMS" },
		asset: func(arch string, _ bool) string { // static Go binary: works on glibc and musl
			if a := map[string]string{"amd64": "amd64", "arm64": "arm64", "arm": "arm-v7"}[arch]; a != "" {
				return `^rclone-v[0-9.]+-linux-` + a + `\.zip$`
			}
			return ""
		}},
	"deno": {repo: "denoland/deno", selfArg: "--version", inner: "deno", sums: func(a string) string { return a + ".sha256sum" },
		asset: func(arch string, musl bool) string { // glibc builds only
			if a := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[arch]; a != "" && !musl {
				return `^deno-` + a + `-unknown-linux-gnu\.zip$`
			}
			return ""
		}},
}

// Installer fetches tools. API is the GitHub API base (tests use a fake server).
type Installer struct {
	Client  *http.Client
	API     string // default https://api.github.com
	DataDir string
	Arch    string // default runtime.GOARCH
	Musl    *bool  // default: detect
}

func musl() bool {
	m, _ := filepath.Glob("/lib/ld-musl-*.so.1")
	return len(m) > 0
}

type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (in Installer) get(ctx context.Context, url string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := in.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if int64(len(b)) > max {
		return nil, fmt.Errorf("GET %s: larger than %d bytes", url, max)
	}
	return b, err
}

// Installed returns the version tag recorded at install time ("" if not managed).
func (in Installer) Installed(name string) string {
	b, _ := os.ReadFile(filepath.Join(in.DataDir, "bin", name+".version"))
	return strings.TrimSpace(string(b))
}

// Install installs or updates name to the latest release. It returns the new tag, or the current
// one unchanged when already up to date.
func (in Installer) Install(ctx context.Context, name string) (string, error) {
	src, ok := sources[name]
	if !ok {
		return "", fmt.Errorf("%s is not a managed tool", name)
	}
	arch, isMusl := in.Arch, musl()
	if arch == "" {
		arch = runtime.GOARCH
	}
	if in.Musl != nil {
		isMusl = *in.Musl
	}
	pattern := src.asset(arch, isMusl)
	if pattern == "" {
		return "", fmt.Errorf("no %s build for linux/%s (musl=%v); install it with your package manager", name, arch, isMusl)
	}
	api := in.API
	if api == "" {
		api = "https://api.github.com"
	}
	b, err := in.get(ctx, api+"/repos/"+src.repo+"/releases/latest", 4<<20)
	if err != nil {
		return "", err
	}
	var rel release
	if err := json.Unmarshal(b, &rel); err != nil {
		return "", err
	}
	if rel.Tag != "" && rel.Tag == in.Installed(name) {
		return rel.Tag, nil
	}
	re := regexp.MustCompile(pattern)
	var assetName, assetURL, sumsURL string
	for _, a := range rel.Assets {
		if re.MatchString(a.Name) {
			assetName, assetURL = a.Name, a.URL
		}
	}
	for _, a := range rel.Assets {
		if assetName != "" && a.Name == src.sums(assetName) {
			sumsURL = a.URL
		}
	}
	if assetURL == "" || sumsURL == "" {
		return "", fmt.Errorf("%s %s: release asset or checksums not found", name, rel.Tag)
	}
	sums, err := in.get(ctx, sumsURL, 1<<20)
	if err != nil {
		return "", err
	}
	want := checksumFor(string(sums), assetName)
	if want == "" {
		return "", fmt.Errorf("%s: no checksum for %s", name, assetName)
	}

	bin := filepath.Join(in.DataDir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(bin, "."+name+"-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	req, _ := http.NewRequestWithContext(ctx, "GET", assetURL, nil)
	resp, err := in.Client.Do(req)
	if err != nil {
		tmp.Close()
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, 512<<20))
	resp.Body.Close()
	tmp.Close()
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download %s: HTTP %d", assetName, resp.StatusCode)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return "", fmt.Errorf("%s: checksum mismatch (got %s, want %s); not installed", assetName, got, want)
	}
	exe := tmp.Name()
	if src.inner != "" {
		if exe, err = unzipOne(tmp.Name(), src.inner, bin); err != nil {
			return "", err
		}
		defer os.Remove(exe)
	}
	if err := os.Chmod(exe, 0o755); err != nil {
		return "", err
	}
	tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(tctx, exe, src.selfArg).CombinedOutput(); err != nil {
		return "", fmt.Errorf("%s %s failed its self-test (%v: %.200s); not installed", name, rel.Tag, err, out)
	}
	final := filepath.Join(bin, name)
	if _, err := os.Stat(final); err == nil {
		os.Remove(final + ".prev")
		if err := os.Link(final, final+".prev"); err != nil { // keep the working copy for Rollback
			return "", err
		}
		if old := in.Installed(name); old != "" {
			os.WriteFile(final+".prev.version", []byte(old), 0o644)
		}
	}
	if err := os.Rename(exe, final); err != nil {
		return "", err
	}
	return rel.Tag, os.WriteFile(final+".version", []byte(rel.Tag), 0o644)
}

// Rollback puts back the copy that was replaced by the last update.
func (in Installer) Rollback(name string) error {
	final := filepath.Join(in.DataDir, "bin", name)
	if _, err := os.Stat(final + ".prev"); err != nil {
		return fmt.Errorf("no previous %s to roll back to", name)
	}
	if err := os.Rename(final+".prev", final); err != nil {
		return err
	}
	if v, err := os.ReadFile(final + ".prev.version"); err == nil {
		os.WriteFile(final+".version", v, 0o644)
		os.Remove(final + ".prev.version")
	}
	return nil
}

// checksumFor reads "HASH  name" lines (or a bare hash, as in per-file .sha256sum files).
func checksumFor(sums, name string) string {
	sc := bufio.NewScanner(strings.NewReader(sums))
	var only string
	lines := 0
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		lines++
		if len(f) == 1 {
			only = f[0]
		}
		if len(f) >= 2 && strings.TrimPrefix(f[len(f)-1], "*") == name {
			return f[0]
		}
	}
	if lines == 1 {
		return only
	}
	return ""
}

// unzipOne extracts the entry whose base name is want into dir and returns its path.
func unzipOne(zipPath, want, dir string) (string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if path.Base(f.Name) != want || f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		out, err := os.CreateTemp(dir, "."+want+"-x-*")
		if err != nil {
			rc.Close()
			return "", err
		}
		_, err = io.Copy(out, io.LimitReader(rc, 512<<20))
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(out.Name())
			return "", err
		}
		return out.Name(), nil
	}
	return "", errors.New("binary " + want + " not found in the archive")
}
