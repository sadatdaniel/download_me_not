// Package fsapi is the folder API: every file operation goes through os.Root, which
// confines paths (including symlinks) to the downloads folder (scope §5.4).
package fsapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type FS struct {
	root *os.Root
	dir  string
	key  []byte
}

type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Dir     bool      `json:"dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

var ErrBadPath = errors.New("invalid path")

func New(dir string, signKey []byte) (*FS, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &FS{root: r, dir: dir, key: signKey}, nil
}

func (f *FS) Close() error { return f.root.Close() }

// clean turns an API path ("/", "/a/b", "a/b") into a root-relative one. os.Root still
// enforces confinement; this only gives clear errors and a canonical form.
func clean(p string) (string, error) {
	if strings.ContainsRune(p, 0) {
		return "", ErrBadPath
	}
	c := path.Clean("/" + p)
	if c == "/" {
		return ".", nil
	}
	return c[1:], nil
}

func entry(p string, fi fs.FileInfo) Entry {
	return Entry{Name: fi.Name(), Path: "/" + p, Dir: fi.IsDir(), Size: fi.Size(), ModTime: fi.ModTime()}
}

func (f *FS) Stat(p string) (Entry, error) {
	c, err := clean(p)
	if err != nil {
		return Entry{}, err
	}
	fi, err := f.root.Stat(c)
	if err != nil {
		return Entry{}, err
	}
	if c == "." {
		return Entry{Name: "/", Path: "/", Dir: true, ModTime: fi.ModTime()}, nil
	}
	return entry(c, fi), nil
}

// List reads a directory from disk each time, so outside changes show up without a restart.
func (f *FS) List(p string) ([]Entry, error) {
	c, err := clean(p)
	if err != nil {
		return nil, err
	}
	des, err := fs.ReadDir(f.root.FS(), c)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		fi, err := de.Info()
		if err != nil {
			continue // vanished between ReadDir and Info
		}
		out = append(out, entry(path.Join(c, de.Name()), fi))
	}
	return out, nil
}

func (f *FS) Mkdir(p string) error {
	c, err := clean(p)
	if err != nil {
		return err
	}
	return f.root.MkdirAll(c, 0o755)
}

func (f *FS) Move(from, to string) error {
	a, err := clean(from)
	if err != nil {
		return err
	}
	b, err := clean(to)
	if err != nil {
		return err
	}
	if a == "." || b == "." {
		return ErrBadPath
	}
	if _, err := f.root.Lstat(b); err == nil {
		return fs.ErrExist // never overwrite silently
	}
	return f.root.Rename(a, b)
}

func (f *FS) Remove(p string) error {
	c, err := clean(p)
	if err != nil {
		return err
	}
	if c == "." {
		return ErrBadPath // never delete the root itself
	}
	if _, err := f.root.Lstat(c); err != nil {
		return err
	}
	return f.root.RemoveAll(c)
}

type Usage struct {
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

func (f *FS) Usage() (Usage, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(f.dir, &s); err != nil {
		return Usage{}, err
	}
	return Usage{Total: s.Blocks * uint64(s.Bsize), Free: s.Bavail * uint64(s.Bsize)}, nil
}

// Serve streams a file with Range support (players can seek).
func (f *FS) Serve(w http.ResponseWriter, r *http.Request, p string) {
	c, err := clean(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	file, err := f.root.Open(c)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil || fi.IsDir() {
		http.Error(w, "not a file", http.StatusBadRequest)
		return
	}
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), file)
}

// Sign returns query parameters for a short-lived URL to one file, so a player never holds the token.
func (f *FS) Sign(p string, ttl time.Duration) (string, error) {
	c, err := clean(p)
	if err != nil {
		return "", err
	}
	exp := strconv.FormatInt(time.Now().Add(ttl).Unix(), 10)
	return "exp=" + exp + "&sig=" + f.mac(c, exp), nil
}

func (f *FS) Verify(p, exp, sig string) bool {
	c, err := clean(p)
	if err != nil || len(f.key) == 0 {
		return false
	}
	e, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > e {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(f.mac(c, exp)))
}

func (f *FS) mac(c, exp string) string {
	m := hmac.New(sha256.New, f.key)
	m.Write([]byte(c + "\x00" + exp))
	return hex.EncodeToString(m.Sum(nil))
}
