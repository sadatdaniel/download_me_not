// Package extract unpacks finished downloads (zip, tar and its compressed forms, 7z, rar) safely
// (scope §5.9): every write goes through os.Root, links are skipped, and size and ratio limits stop
// decompression bombs. Password-protected archives are skipped with a clear message.
package extract

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/mholt/archives"
)

type Limits struct {
	MaxBytes int64   // total extracted bytes; default 100 GiB
	MaxRatio float64 // extracted / archive size; default 200
}

var (
	ErrNotArchive = errors.New("not an archive")
	ErrEncrypted  = errors.New("archive is password-protected; skipped")
	ErrTooBig     = errors.New("archive expands beyond the size or ratio limit (possible decompression bomb); stopped")
)

type Result struct {
	Dir   string `json:"dir"` // folder created next to the archive
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// IsArchive guesses from the name, cheaply, before opening anything.
func IsArchive(name string) bool {
	n := strings.ToLower(name)
	for _, ext := range []string{".zip", ".tar", ".tar.gz", ".tgz", ".tar.xz", ".txz", ".tar.bz2", ".tbz2", ".tar.zst", ".7z", ".rar"} {
		if strings.HasSuffix(n, ext) {
			return true
		}
	}
	return false
}

// Extract unpacks archivePath into a new folder next to it, named after the archive.
// On error the partly extracted folder is removed.
func Extract(ctx context.Context, archivePath string, lim Limits) (Result, error) {
	if lim.MaxBytes <= 0 {
		lim.MaxBytes = 100 << 30
	}
	if lim.MaxRatio <= 0 {
		lim.MaxRatio = 200
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Result{}, err
	}
	format, stream, err := archives.Identify(ctx, filepath.Base(archivePath), f)
	if err != nil {
		return Result{}, ErrNotArchive
	}
	ex, ok := format.(archives.Extractor)
	if !ok {
		return Result{}, ErrNotArchive
	}
	// zip and 7z need random access: give them the file itself, rewound.
	var input io.Reader = stream
	if _, isZip := format.(archives.Zip); isZip {
		f.Seek(0, io.SeekStart)
		input = f
	}
	if _, is7z := format.(archives.SevenZip); is7z {
		f.Seek(0, io.SeekStart)
		input = f
	}

	dir, err := freeName(archivePath)
	if err != nil {
		return Result{}, err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return Result{}, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Result{}, err
	}
	defer root.Close()

	res := Result{Dir: dir}
	maxTotal := min(lim.MaxBytes, int64(float64(max(fi.Size(), 1))*lim.MaxRatio))
	err = ex.Extract(ctx, input, func(ctx context.Context, e archives.FileInfo) error {
		name := path.Clean("/" + filepath.ToSlash(e.NameInArchive))[1:] // anchors "../x" and "/abs" inside
		if name == "" || name == "." {
			return nil
		}
		if e.IsDir() {
			return root.MkdirAll(name, 0o755)
		}
		if !e.Mode().IsRegular() {
			return nil // symlinks, hard links, devices: skipped
		}
		if d := path.Dir(name); d != "." {
			if err := root.MkdirAll(d, 0o755); err != nil {
				return err
			}
		}
		in, err := e.Open()
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		// Count real bytes, not the header's claim; stop one byte past the budget.
		n, err := io.Copy(out, io.LimitReader(in, maxTotal-res.Bytes+1))
		cerr := out.Close()
		res.Bytes += n
		if res.Bytes > maxTotal {
			return ErrTooBig
		}
		if err != nil {
			return err
		}
		res.Files++
		return cerr
	})
	if err != nil {
		os.RemoveAll(dir)
		if strings.Contains(strings.ToLower(err.Error()), "password") || strings.Contains(strings.ToLower(err.Error()), "encrypt") {
			return Result{}, ErrEncrypted
		}
		return Result{}, err
	}
	return res, nil
}

// freeName returns "<archive without extension>", or with " (n)" if that exists.
func freeName(archivePath string) (string, error) {
	base := archivePath
	for _, ext := range []string{".tar.gz", ".tar.xz", ".tar.bz2", ".tar.zst"} {
		if strings.HasSuffix(strings.ToLower(base), ext) {
			base = base[:len(base)-len(ext)]
			break
		}
	}
	if base == archivePath {
		base = strings.TrimSuffix(base, filepath.Ext(base))
	}
	for i := 0; i < 1000; i++ {
		n := base
		if i > 0 {
			n = fmt.Sprintf("%s (%d)", base, i)
		}
		if _, err := os.Lstat(n); os.IsNotExist(err) {
			return n, nil
		}
	}
	return "", fmt.Errorf("no free folder name for %s", base)
}
