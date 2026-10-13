package api

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/sadatdaniel/download_me_not/internal/extract"
	"github.com/sadatdaniel/download_me_not/internal/fsapi"
)

// Archives open like folders: list one level, stream one file. Nothing is unpacked on disk.
func (s *server) archiveRoutes(api huma.API) {
	huma.Register(api, op("archive-list", "GET", "/v1/archive", "read",
		"List one folder inside an archive (zip, tar, 7z, rar) without unpacking it. Entry paths are relative to the archive; "+
			"stream one with /v1/archive/raw/{archive path}?entry=… (token, or the exp/sig of /v1/fs/sign for the archive)"),
		func(ctx context.Context, in *struct {
			Path string `query:"path" required:"true" doc:"the archive, inside the download folder"`
			Dir  string `query:"dir" default:"." doc:"folder inside the archive"`
		}) (*struct{ Body []fsapi.Entry }, error) {
			f, err := s.fs(ctx)
			if err != nil {
				return nil, err
			}
			afs, closeFn, err := s.openArchive(ctx, f, in.Path)
			if err != nil {
				return nil, err
			}
			defer closeFn()
			dir := strings.Trim(in.Dir, "/")
			if dir == "" {
				dir = "."
			}
			if !fs.ValidPath(dir) {
				return nil, huma.Error400BadRequest("invalid dir")
			}
			des, err := fs.ReadDir(afs, dir)
			if err != nil {
				return nil, archiveErr(err)
			}
			out := make([]fsapi.Entry, 0, len(des))
			for _, de := range des {
				fi, err := de.Info()
				if err != nil {
					continue
				}
				out = append(out, fsapi.Entry{Name: de.Name(), Path: path.Join(dir, de.Name()), Dir: de.IsDir(), Size: fi.Size(), ModTime: fi.ModTime()})
			}
			return body(out), nil
		})
}

func (s *server) openArchive(ctx context.Context, fsys *fsapi.FS, p string) (fs.FS, func(), error) {
	f, err := fsys.Open(p)
	if err != nil {
		return nil, nil, fsErr(err)
	}
	afs, err := extract.Browse(ctx, f)
	if err != nil {
		f.Close()
		return nil, nil, huma.Error400BadRequest(err.Error())
	}
	return afs, func() { f.Close() }, nil
}

func archiveErr(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return huma.Error404NotFound("not in the archive")
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "password") || strings.Contains(msg, "encrypt") {
		return huma.Error400BadRequest(extract.ErrEncrypted.Error())
	}
	return huma.Error400BadRequest(err.Error())
}

// archiveRaw streams one file out of an archive (read token, or a signed link to the archive).
func (s *server) archiveRaw(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("path")
	fsys, _, ok := s.access(w, r, p)
	if !ok {
		return
	}
	q := r.URL.Query()
	entry := strings.Trim(q.Get("entry"), "/")
	if !fs.ValidPath(entry) || entry == "." {
		http.Error(w, "invalid entry", http.StatusBadRequest)
		return
	}
	afs, closeFn, err := s.openArchive(r.Context(), fsys, p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer closeFn()
	// Index first: without it ArchiveFS.Open matches names by prefix, so "a" would open "a/hello.txt".
	if _, err := fs.ReadDir(afs, "."); err != nil {
		http.Error(w, archiveErr(err).Error(), http.StatusBadRequest)
		return
	}
	f, err := afs.Open(entry)
	if err != nil {
		http.Error(w, archiveErr(err).Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.Error(w, "not a file", http.StatusBadRequest)
		return
	}
	h := w.Header()
	h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": fi.Name()}))
	if rs, ok := f.(io.ReadSeeker); ok { // seekable entries (e.g. stored zip members) support Range
		http.ServeContent(w, r, fi.Name(), fi.ModTime(), rs)
		return
	}
	// ponytail: compressed entries stream from the start only (no Range, so players cannot seek). Upgrade if
	// people play videos straight out of compressed archives: extract to a temp file first.
	if ct := mime.TypeByExtension(path.Ext(fi.Name())); ct != "" {
		h.Set("Content-Type", ct)
	}
	h.Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	io.Copy(w, f)
}
