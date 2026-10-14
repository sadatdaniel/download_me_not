package api

import (
	"context"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/sadatdaniel/getaload/internal/fsapi"
)

type cleanupOut struct {
	Files []fsapi.Entry `json:"files" doc:"leftover partial files that no download owns"`
	Bytes int64         `json:"bytes"`
}

// Leftovers from downloads removed while unfinished (or from before the server tracked them): .part files that
// no job owns. They can never be resumed, so they only take space.
func (s *server) cleanupRoutes(api huma.API) {
	huma.Register(api, op("cleanup-list", "GET", "/v1/cleanup", "read", "Leftover partial files that no download owns"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body cleanupOut }, error) {
			_, o, err := s.orphans(ctx)
			return body(o), err
		})
	huma.Register(api, op("cleanup", "POST", "/v1/cleanup", "write", "Delete leftover partial files that no download owns"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body cleanupOut }, error) {
			fsys, o, err := s.orphans(ctx)
			if err != nil {
				return nil, err
			}
			done := cleanupOut{Files: []fsapi.Entry{}}
			for _, f := range o.Files {
				if fsys.Remove(f.Path) == nil {
					done.Files = append(done.Files, f)
					done.Bytes += f.Size
				}
			}
			return body(done), nil
		})
}

func (s *server) orphans(ctx context.Context) (*fsapi.FS, cleanupOut, error) {
	fsys, err := s.fs(ctx)
	if err != nil {
		return nil, cleanupOut{}, err
	}
	parts, err := fsys.Parts()
	if err != nil {
		return nil, cleanupOut{}, err
	}
	jobs, err := s.jobsOf(userOf(ctx))
	if err != nil {
		return nil, cleanupOut{}, err
	}
	out := cleanupOut{Files: []fsapi.Entry{}}
	for _, p := range parts {
		rel := strings.TrimPrefix(p.Path, "/")
		owned := false
		for _, j := range jobs {
			if j.Path != "" && (rel == j.Path+".part" || strings.HasPrefix(rel, j.Path+"/")) {
				owned = true
				break
			}
		}
		if !owned {
			out.Files = append(out.Files, p)
			out.Bytes += p.Size
		}
	}
	return fsys, out, nil
}
