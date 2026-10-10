package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/sadatdaniel/download_me_not/internal/jobs"
	"github.com/sadatdaniel/download_me_not/internal/store"
)

func jobErr(err error) error {
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		return huma.Error404NotFound(err.Error())
	case errors.Is(err, jobs.ErrNotRunning):
		return huma.Error409Conflict(err.Error())
	default:
		return huma.Error400BadRequest(err.Error())
	}
}

type idIn struct {
	ID string `path:"id"`
}

type jobOut struct {
	Status int
	Body   store.Job
}

func (s *server) jobRoutes(api huma.API) {
	huma.Register(api, op("add-job", "POST", "/v1/jobs", "write",
		"Add a download: URL, magnet link or .torrent (base64). Re-adding a known torrent returns the existing job with 200"),
		func(ctx context.Context, in *struct{ Body jobs.AddRequest }) (*jobOut, error) {
			j, existed, err := s.Jobs.Add(in.Body)
			if err != nil {
				return nil, jobErr(err)
			}
			if existed {
				return &jobOut{http.StatusOK, j}, nil
			}
			return &jobOut{http.StatusCreated, j}, nil
		})
	huma.Register(api, op("get-job", "GET", "/v1/jobs/{id}", "read", "One download"),
		func(ctx context.Context, in *idIn) (*struct{ Body store.Job }, error) {
			j, err := s.Jobs.Get(in.ID)
			if err != nil {
				return nil, jobErr(err)
			}
			return body(j), nil
		})
	for _, a := range []struct {
		name string
		fn   func(string) error
	}{{"pause", s.Jobs.Pause}, {"resume", s.Jobs.Resume}} {
		huma.Register(api, op(a.name+"-job", "POST", "/v1/jobs/{id}/"+a.name, "write", a.name+" a download"),
			func(ctx context.Context, in *idIn) (*struct{}, error) {
				if err := a.fn(in.ID); err != nil {
					return nil, jobErr(err)
				}
				return nil, nil
			})
	}
	huma.Register(api, op("job-mode", "PUT", "/v1/jobs/{id}/mode", "write", "Switch between standard and stream mode while it runs"),
		func(ctx context.Context, in *struct {
			ID   string `path:"id"`
			Body struct {
				Mode string `json:"mode" enum:"standard,stream"`
			}
		}) (*struct{}, error) {
			if err := s.Jobs.SetMode(in.ID, in.Body.Mode); err != nil {
				return nil, jobErr(err)
			}
			return nil, nil
		})
	huma.Register(api, op("cancel-job", "DELETE", "/v1/jobs/{id}", "write", "Stop and forget a download; deleteFiles also removes its files"),
		func(ctx context.Context, in *struct {
			ID          string `path:"id"`
			DeleteFiles bool   `query:"deleteFiles"`
		}) (*struct{}, error) {
			if err := s.Jobs.Cancel(in.ID, in.DeleteFiles); err != nil {
				return nil, jobErr(err)
			}
			return nil, nil
		})
	huma.Register(api, op("sign-job", "POST", "/v1/jobs/{id}/sign", "read", "Short-lived stream URL for a download, also while it runs"),
		func(ctx context.Context, in *idIn) (*struct{ Body map[string]string }, error) {
			if _, err := s.Jobs.Get(in.ID); err != nil {
				return nil, jobErr(err)
			}
			q, err := s.FS.Sign("jobs/"+in.ID, 6*time.Hour)
			if err != nil {
				return nil, err
			}
			return body(map[string]string{"url": "/v1/jobs/" + in.ID + "/stream?" + q}), nil
		})
}

// stream serves a download with Range support while it is still downloading (stream mode).
// ?file=N picks a file inside a torrent (default: the largest).
func (s *server) stream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := r.URL.Query()
	if q.Get("sig") != "" {
		if !s.FS.Verify("jobs/"+id, q.Get("exp"), q.Get("sig")) {
			http.Error(w, "invalid or expired link", http.StatusForbidden)
			return
		}
	} else if rank[s.scope(r.Header.Get("Authorization"))] < rank["read"] {
		http.Error(w, "missing or invalid token", http.StatusUnauthorized)
		return
	}
	idx := -1
	if f := q.Get("file"); f != "" {
		n, err := strconv.Atoi(f)
		if err != nil {
			http.Error(w, "file must be a number", http.StatusBadRequest)
			return
		}
		idx = n
	}
	rd, name, err := s.Jobs.Open(r.Context(), id, idx)
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case errors.Is(err, jobs.ErrNotRunning):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer rd.Close()
	http.ServeContent(w, r, name, time.Time{}, rd)
}
