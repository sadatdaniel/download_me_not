package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/sadatdaniel/download_me_not/internal/engine"
	"github.com/sadatdaniel/download_me_not/internal/jobs"
	"github.com/sadatdaniel/download_me_not/internal/store"
)

func jobErr(err error) error {
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		return huma.Error404NotFound(err.Error())
	case errors.Is(err, jobs.ErrNotRunning):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, jobs.ErrBusyElsewhere):
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
			in.Body.User = userOf(ctx)
			j, existed, err := s.Jobs.Add(in.Body)
			if err != nil {
				return nil, jobErr(err)
			}
			j = own(j)
			if existed {
				return &jobOut{http.StatusOK, j}, nil
			}
			return &jobOut{http.StatusCreated, j}, nil
		})
	huma.Register(api, op("probe", "POST", "/v1/probe", "write", "What is this URL? Web file, video page (with formats and presets) or torrent"),
		func(ctx context.Context, in *struct {
			Body struct {
				URL    string            `json:"url" minLength:"1"`
				Header map[string]string `json:"header,omitempty"`
			}
		}) (*struct{ Body jobs.ProbeResult }, error) {
			ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			res, err := s.Jobs.Probe(ctx, in.Body.URL, in.Body.Header)
			if err != nil {
				return nil, jobErr(err)
			}
			return body(res), nil
		})
	huma.Register(api, op("copy", "POST", "/v1/copy", "write", "Copy (or move) a finished file or folder to a cloud destination such as Google Drive"),
		func(ctx context.Context, in *struct{ Body jobs.CopyRequest }) (*struct {
			Status int
			Body   store.Job
		}, error) {
			in.Body.User = userOf(ctx)
			j, err := s.Jobs.AddCopy(in.Body)
			if err != nil {
				return nil, jobErr(err)
			}
			j = own(j)
			return &struct {
				Status int
				Body   store.Job
			}{http.StatusCreated, j}, nil
		})
	huma.Register(api, op("get-job", "GET", "/v1/jobs/{id}", "read", "One download"),
		func(ctx context.Context, in *idIn) (*struct{ Body store.Job }, error) {
			j, err := s.mine(ctx, in.ID)
			if err != nil {
				return nil, err
			}
			return body(j), nil
		})
	for _, a := range []struct {
		name string
		fn   func(string) error
	}{{"pause", s.Jobs.Pause}, {"resume", s.Jobs.Resume}} {
		huma.Register(api, op(a.name+"-job", "POST", "/v1/jobs/{id}/"+a.name, "write", a.name+" a download"),
			func(ctx context.Context, in *idIn) (*struct{}, error) {
				if _, err := s.mine(ctx, in.ID); err != nil {
					return nil, err
				}
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
			if _, err := s.mine(ctx, in.ID); err != nil {
				return nil, err
			}
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
			if _, err := s.mine(ctx, in.ID); err != nil {
				return nil, err
			}
			if err := s.Jobs.Cancel(in.ID, in.DeleteFiles); err != nil {
				return nil, jobErr(err)
			}
			return nil, nil
		})
	huma.Register(api, op("job-peers", "GET", "/v1/jobs/{id}/peers", "read", "Connected peers of a running torrent"),
		func(ctx context.Context, in *idIn) (*struct{ Body []engine.Peer }, error) {
			if _, err := s.mine(ctx, in.ID); err != nil {
				return nil, err
			}
			ps, err := s.Jobs.Peers(in.ID)
			if err != nil {
				return nil, jobErr(err)
			}
			return body(ps), nil
		})
	huma.Register(api, op("job-pieces", "GET", "/v1/jobs/{id}/pieces", "read", "Piece map, availability and a plain-language hint for a running torrent"),
		func(ctx context.Context, in *idIn) (*struct{ Body jobs.PieceView }, error) {
			if _, err := s.mine(ctx, in.ID); err != nil {
				return nil, err
			}
			v, err := s.Jobs.Pieces(in.ID)
			if err != nil {
				return nil, jobErr(err)
			}
			return body(v), nil
		})
	huma.Register(api, op("job-trackers", "GET", "/v1/jobs/{id}/trackers", "read", "Tracker tiers of a running torrent"),
		func(ctx context.Context, in *idIn) (*struct{ Body [][]string }, error) {
			if _, err := s.mine(ctx, in.ID); err != nil {
				return nil, err
			}
			tr, err := s.Jobs.Trackers(in.ID)
			if err != nil {
				return nil, jobErr(err)
			}
			return body(tr), nil
		})
	huma.Register(api, op("set-job-trackers", "PUT", "/v1/jobs/{id}/trackers", "write", "Replace the tracker tiers (kept across restarts)"),
		func(ctx context.Context, in *struct {
			ID   string `path:"id"`
			Body [][]string
		}) (*struct{}, error) {
			for _, tier := range in.Body {
				for _, u := range tier {
					if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "udp://") {
						return nil, huma.Error422UnprocessableEntity("tracker URLs must be http, https or udp: " + u)
					}
				}
			}
			if _, err := s.mine(ctx, in.ID); err != nil {
				return nil, err
			}
			if err := s.Jobs.SetTrackers(in.ID, in.Body); err != nil {
				return nil, jobErr(err)
			}
			return nil, nil
		})
	huma.Register(api, op("recheck-job", "POST", "/v1/jobs/{id}/recheck", "write", "Verify a torrent's data on disk (runs in the background)"),
		func(ctx context.Context, in *idIn) (*struct{}, error) {
			if _, err := s.mine(ctx, in.ID); err != nil {
				return nil, err
			}
			if err := s.Jobs.Recheck(in.ID); err != nil {
				return nil, jobErr(err)
			}
			return nil, nil
		})
	huma.Register(api, op("job-priority", "PUT", "/v1/jobs/{id}/priority", "write", "Reorder the queue: higher starts first"),
		func(ctx context.Context, in *struct {
			ID   string `path:"id"`
			Body struct {
				Priority int `json:"priority"`
			}
		}) (*struct{}, error) {
			if _, err := s.mine(ctx, in.ID); err != nil {
				return nil, err
			}
			if err := s.Jobs.SetPriority(in.ID, in.Body.Priority); err != nil {
				return nil, jobErr(err)
			}
			return nil, nil
		})
	huma.Register(api, op("job-files", "GET", "/v1/jobs/{id}/files", "read", "Files of a running torrent (the file picker)"),
		func(ctx context.Context, in *idIn) (*struct{ Body []engine.File }, error) {
			if _, err := s.mine(ctx, in.ID); err != nil {
				return nil, err
			}
			fs, err := s.Jobs.Files(in.ID)
			if err != nil {
				return nil, jobErr(err)
			}
			return body(fs), nil
		})
	huma.Register(api, op("job-file-priority", "PUT", "/v1/jobs/{id}/files/{index}/priority", "write", "Skip a file or raise its priority"),
		func(ctx context.Context, in *struct {
			ID    string `path:"id"`
			Index int    `path:"index"`
			Body  struct {
				Priority string `json:"priority" enum:"skip,normal,high"`
			}
		}) (*struct{}, error) {
			if _, err := s.mine(ctx, in.ID); err != nil {
				return nil, err
			}
			if err := s.Jobs.SetFilePriority(in.ID, in.Index, in.Body.Priority); err != nil {
				return nil, jobErr(err)
			}
			return nil, nil
		})
	huma.Register(api, op("sign-job", "POST", "/v1/jobs/{id}/sign", "read", "Short-lived stream URL for a download, also while it runs"),
		func(ctx context.Context, in *idIn) (*struct{ Body map[string]string }, error) {
			if _, err := s.mine(ctx, in.ID); err != nil {
				return nil, err
			}
			f, err := s.fs(ctx)
			if err != nil {
				return nil, err
			}
			q, err := f.Sign("jobs/"+in.ID, 6*time.Hour)
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
	_, user, ok := s.access(w, r, "jobs/"+id)
	if !ok {
		return
	}
	if j, err := s.Jobs.Get(id); err != nil || (j.User != user && (user != "" || s.hasUsers())) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
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
