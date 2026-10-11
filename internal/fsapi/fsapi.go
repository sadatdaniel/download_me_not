// Package fsapi is the folder API: every file operation goes through os.Root, which
// confines paths (including symlinks) to the downloads folder (scope §5.4).
package fsapi

import (
	"archive/zip"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"sort"
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

// Open opens a file for reading inside the root (archive browsing).
func (f *FS) Open(p string) (*os.File, error) {
	c, err := clean(p)
	if err != nil {
		return nil, err
	}
	return f.root.Open(c)
}

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

// Copy copies a file or a folder tree inside the root. It never overwrites.
func (f *FS) Copy(from, to string) error {
	a, err := clean(from)
	if err != nil {
		return err
	}
	b, err := clean(to)
	if err != nil {
		return err
	}
	if a == "." || b == "." || b == a || strings.HasPrefix(b, a+"/") {
		return ErrBadPath // no root, no copying a folder into itself
	}
	if _, err := f.root.Lstat(b); err == nil {
		return fs.ErrExist
	}
	return fs.WalkDir(f.root.FS(), a, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := b + strings.TrimPrefix(p, a)
		if d.IsDir() {
			return f.root.Mkdir(dst, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil // skip symlinks and devices
		}
		in, err := f.root.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := f.root.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// Zip streams a folder as a ZIP. Entries are stored, not compressed: media doesn't shrink and
// storing keeps it fast on small devices. archive/zip switches to ZIP64 for large files.
func (f *FS) Zip(w io.Writer, p string) error {
	c, err := clean(p)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(w)
	err = fs.WalkDir(f.root.FS(), c, func(fp string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		h, err := zip.FileInfoHeader(fi)
		if err != nil {
			return err
		}
		h.Name = strings.TrimPrefix(strings.TrimPrefix(fp, c), "/")
		if c == "." {
			h.Name = fp
		}
		h.Method = zip.Store
		zf, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		in, err := f.root.Open(fp)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(zf, in)
		return err
	})
	if err != nil {
		return err
	}
	return zw.Close()
}

var mediaExts = map[string]bool{
	".mp4": true, ".mkv": true, ".webm": true, ".avi": true, ".mov": true, ".m4v": true, ".ts": true, ".m2ts": true,
	".wmv": true, ".flv": true, ".mp3": true, ".flac": true, ".m4a": true, ".aac": true, ".ogg": true, ".opus": true, ".wav": true,
}

// Media lists every video and audio file under the download folder, newest first, at most limit.
// ponytail: walks the tree on every call; fine for thousands of files. Upgrade: index in SQLite, updated by
// jobs on completion and by fsapi writes, if libraries grow to hundreds of thousands of files.
func (f *FS) Media(limit int) ([]Entry, error) {
	var out []Entry
	err := fs.WalkDir(f.root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable folder: skip it, list the rest
		}
		if d.IsDir() || !mediaExts[strings.ToLower(path.Ext(p))] {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			out = append(out, entry(p, fi))
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, err
}
