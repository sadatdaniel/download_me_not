package extract

import (
	"context"
	"io"
	"io/fs"
	"os"

	"github.com/mholt/archives"
)

// Browse opens an archive read-only as a file tree, so its folders can be listed and single files
// streamed without unpacking it. Nothing is written; entry names are checked by fs.ValidPath.
// ponytail: each call re-reads the index, and compressed tars are decompressed from the start up to the
// entry; fine for zip/7z/rar, slow for a huge .tar.gz. Upgrade: cache the index per (path, mtime).
func Browse(ctx context.Context, f *os.File) (fs.FS, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	format, _, err := archives.Identify(ctx, fi.Name(), io.NewSectionReader(f, 0, fi.Size()))
	if err != nil {
		return nil, ErrNotArchive
	}
	ex, ok := format.(archives.Extractor)
	if !ok {
		return nil, ErrNotArchive
	}
	return &archives.ArchiveFS{Stream: io.NewSectionReader(f, 0, fi.Size()), Format: ex, Context: ctx}, nil
}
