package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/sadatdaniel/getaload/internal/rclone"
	"github.com/sadatdaniel/getaload/internal/store"
)

// Dest is a configured cloud destination: the rclone remote plus where copies go.
type Dest struct {
	Runner    rclone.Runner
	Remote    rclone.Remote
	Folder    string
	SaveToken func(token string) error // rclone refreshed the OAuth token
}

type CopyRequest struct {
	Path   string `json:"path" doc:"file or folder inside the download folder"`
	User   string `json:"-"` // set by the API; Path is inside this user's folder
	Dest   string `json:"dest,omitempty" doc:"destination provider, default gdrive"`
	Move   bool   `json:"move,omitempty" doc:"delete the local copy after the upload is verified"`
	Folder string `json:"folder,omitempty" doc:"destination folder; default from the provider settings"`
}

type copyMeta struct {
	Dest   string `json:"dest"`
	Move   bool   `json:"move,omitempty"`
	Folder string `json:"folder,omitempty"`
}

// AddCopy queues a copy of a finished file or folder to a cloud destination.
func (m *Manager) AddCopy(r CopyRequest) (store.Job, error) {
	if r.Dest == "" {
		r.Dest = "gdrive"
	}
	if m.o.Allowed != nil && !m.o.Allowed(r.Dest) {
		return store.Job{}, fmt.Errorf("the %s provider is switched off", r.Dest)
	}
	rel := path.Clean("/" + filepath.ToSlash(r.Path))[1:]
	if rel != "" && r.User != "" {
		rel = path.Join(userDir(r.User), rel)
	}
	if rel == "" {
		return store.Job{}, errors.New("copy a file or folder, not the whole download folder")
	}
	root, err := os.OpenRoot(m.o.Downloads)
	if err != nil {
		return store.Job{}, err
	}
	fi, err := root.Stat(rel) // confined to the download folder
	root.Close()
	if err != nil {
		return store.Job{}, ErrNotFound
	}
	now := time.Now().Unix()
	j := store.Job{ID: newID(), Kind: "copy", Source: rel, Mode: "standard", Want: "running", State: "queued",
		Name: fi.Name(), Path: rel, Total: -1, Created: now, Updated: now, User: r.User}
	j.Meta, _ = json.Marshal(copyMeta{Dest: r.Dest, Move: r.Move, Folder: r.Folder})
	if err := store.InsertJob(m.o.DB, j); err != nil {
		return store.Job{}, err
	}
	m.poke()
	return j, nil
}

func (m *Manager) runCopy(ctx context.Context, r *runner, j store.Job) error {
	var meta copyMeta
	json.Unmarshal(j.Meta, &meta)
	if m.o.Destination == nil {
		return errors.New("no cloud destinations configured")
	}
	d, err := m.o.Destination(meta.Dest, j.User) // the owner's own Drive
	if err != nil {
		return err
	}
	folder := meta.Folder
	if folder == "" {
		folder = d.Folder
	}
	r.mu.Lock()
	r.ext = true
	r.mu.Unlock()
	store.SetJob(m.o.DB, j.ID, map[string]any{"state": "uploading"})
	var last time.Time
	tok, err := d.Runner.Copy(ctx, d.Remote, filepath.Join(m.o.Downloads, filepath.FromSlash(j.Source)), path.Join(folder, strings.TrimPrefix(j.Source, userDir(j.User)+"/")), meta.Move,
		func(done, total int64) {
			r.mu.Lock()
			r.vdone, r.vtotal = done, total
			r.mu.Unlock()
			if time.Since(last) > 2*time.Second {
				last = time.Now()
				store.SetJob(m.o.DB, j.ID, map[string]any{"done": done, "total": total})
			}
		})
	if tok != "" && d.SaveToken != nil {
		if serr := d.SaveToken(tok); serr != nil && err == nil {
			err = fmt.Errorf("upload done but the refreshed token could not be saved: %w", serr)
		}
	}
	if err != nil {
		return err
	}
	r.mu.Lock()
	done := r.vdone
	r.mu.Unlock()
	return store.SetJob(m.o.DB, j.ID, map[string]any{"done": done, "total": done})
}

// queueDriveCopy runs the Drive step of the post-completion pipeline.
// rel is the job path (with the user's folder); the copy request wants it inside that folder.
func (m *Manager) queueDriveCopy(r *runner, p *Post, rel, user string) {
	if p == nil || !p.Drive || rel == "" {
		return
	}
	inUser := strings.TrimPrefix(rel, userDir(user)+"/")
	if _, err := m.AddCopy(CopyRequest{Path: inUser, Move: p.DeleteLocal, User: user}); err != nil {
		r.mu.Lock()
		r.reason = "drive copy not queued: " + err.Error()
		r.mu.Unlock()
	}
}

// post returns a job's own post-completion steps, or the defaults.
func (m *Manager) post(override *Post) *Post {
	if override != nil {
		return override
	}
	p := m.o.Post
	return &p
}
