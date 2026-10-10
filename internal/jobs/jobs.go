// Package jobs runs downloads. Crash-only and reconciling (docs/architecture.md): the DB holds
// what the user wants (want = running|paused); a loop compares that with the runners that exist
// and starts or stops them. A restart is the same loop starting from an empty runner set, so
// recovery needs no special code path. Each runner is isolated with recover().
package jobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sadatdaniel/download_me_not/internal/engine"
	"github.com/sadatdaniel/download_me_not/internal/httpdl"
	"github.com/sadatdaniel/download_me_not/internal/store"
)

type Options struct {
	DB              *sql.DB
	Downloads       string
	Client          *http.Client                  // netguard.Client() in production
	NewEngine       func() (engine.Engine, error) // called on the first torrent job only
	MaxActive       int                           // default 3
	Connections     int                           // per web download, default 16
	MetadataTimeout time.Duration                 // default 5 min
	Tick            time.Duration                 // reconcile interval, default 2 s
}

var (
	ErrNotFound   = errors.New("no such job")
	ErrNotRunning = errors.New("job is not running")
	ErrBadSource  = errors.New("unsupported source: use an http(s) URL, a magnet link or a .torrent file")
)

type Manager struct {
	o    Options
	kick chan struct{}

	mu   sync.Mutex
	run  map[string]*runner
	eng  engine.Engine
	last map[string]sample
}

type runner struct {
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	dl     *httpdl.Download
	part   string
	tor    engine.Torrent
}

type sample struct {
	at   time.Time
	done int64
}

type webMeta struct {
	Header map[string]string `json:"header,omitempty"`
	HTTP   *httpdl.State     `json:"http,omitempty"`
}

type torMeta struct {
	Torrent []byte `json:"torrent,omitempty"`
	Magnet  string `json:"magnet,omitempty"`
	Started bool   `json:"started"` // data may exist on disk: recheck before resuming
}

func New(o Options) *Manager {
	if o.MaxActive <= 0 {
		o.MaxActive = 3
	}
	if o.Connections <= 0 {
		o.Connections = 16
	}
	if o.MetadataTimeout <= 0 {
		o.MetadataTimeout = 5 * time.Minute
	}
	if o.Tick <= 0 {
		o.Tick = 2 * time.Second
	}
	return &Manager{o: o, kick: make(chan struct{}, 1), run: map[string]*runner{}, last: map[string]sample{}}
}

func (m *Manager) poke() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type AddRequest struct {
	Source  string            `json:"source,omitempty"`  // URL or magnet link
	Torrent []byte            `json:"torrent,omitempty"` // .torrent file content (base64 in JSON)
	Mode    string            `json:"mode,omitempty"`    // standard (default) or stream
	Header  map[string]string `json:"header,omitempty"`  // e.g. Cookie, Referer, User-Agent from the browser
}

// Add creates a job. Adding a torrent that already has a job returns that job and true.
func (m *Manager) Add(r AddRequest) (store.Job, bool, error) {
	if r.Mode == "" {
		r.Mode = "standard"
	}
	if r.Mode != "standard" && r.Mode != "stream" {
		return store.Job{}, false, fmt.Errorf("mode must be standard or stream")
	}
	now := time.Now().Unix()
	j := store.Job{ID: newID(), Mode: r.Mode, Want: "running", State: "queued", Total: -1, Created: now, Updated: now}
	var meta any
	switch {
	case len(r.Torrent) > 0 || strings.HasPrefix(r.Source, "magnet:"):
		b := r.Torrent
		if len(b) == 0 {
			b = []byte(r.Source)
		}
		ih, err := engine.InfoHash(b)
		if err != nil {
			return store.Job{}, false, err
		}
		if old, err := store.JobByInfoHash(m.o.DB, ih); err == nil {
			return old, true, nil
		}
		j.Kind, j.InfoHash, j.Source = "torrent", ih, r.Source
		if j.Source == "" {
			j.Source = "torrent:" + ih
		}
		meta = torMeta{Torrent: r.Torrent, Magnet: strings.TrimPrefix(r.Source, "torrent:")}
	default:
		u, err := url.Parse(r.Source)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return store.Job{}, false, ErrBadSource
		}
		j.Kind, j.Source = "web", r.Source
		meta = webMeta{Header: r.Header}
	}
	j.Meta, _ = json.Marshal(meta)
	if err := store.InsertJob(m.o.DB, j); err != nil {
		return store.Job{}, false, err
	}
	m.poke()
	return j, false, nil
}

func (m *Manager) get(id string) (store.Job, error) {
	j, err := store.GetJob(m.o.DB, id)
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}

func (m *Manager) Get(id string) (store.Job, error) { return m.get(id) }

func (m *Manager) Pause(id string) error {
	if _, err := m.get(id); err != nil {
		return err
	}
	if err := store.SetJob(m.o.DB, id, map[string]any{"want": "paused"}); err != nil {
		return err
	}
	m.poke()
	return nil
}

// Resume also retries a failed job.
func (m *Manager) Resume(id string) error {
	j, err := m.get(id)
	if err != nil {
		return err
	}
	f := map[string]any{"want": "running"}
	if j.State == "failed" || j.State == "paused" {
		f["state"], f["error"] = "queued", ""
	}
	if err := store.SetJob(m.o.DB, id, f); err != nil {
		return err
	}
	m.poke()
	return nil
}

// SetMode switches standard/stream; a running web job restarts its runner with the new
// connection count and resumes from its segment map.
func (m *Manager) SetMode(id, mode string) error {
	if mode != "standard" && mode != "stream" {
		return fmt.Errorf("mode must be standard or stream")
	}
	j, err := m.get(id)
	if err != nil {
		return err
	}
	if err := store.SetJob(m.o.DB, id, map[string]any{"mode": mode}); err != nil {
		return err
	}
	if j.Kind == "web" {
		m.stop(id)
	}
	m.poke()
	return nil
}

// Cancel stops a job and forgets it; with deleteFiles it also removes its files.
func (m *Manager) Cancel(id string, deleteFiles bool) error {
	j, err := m.get(id)
	if err != nil {
		return err
	}
	if err := store.SetJob(m.o.DB, id, map[string]any{"want": "paused"}); err != nil {
		return err
	}
	m.stop(id)
	var errs []error
	if j.Kind == "torrent" {
		m.mu.Lock()
		eng := m.eng
		m.mu.Unlock()
		if eng != nil {
			if t, ok := eng.Get(j.InfoHash); ok {
				errs = append(errs, t.Drop(deleteFiles))
			}
		}
	}
	if deleteFiles && j.Path != "" {
		root, err := os.OpenRoot(m.o.Downloads)
		if err != nil {
			return err
		}
		for _, p := range []string{j.Path + ".part", j.Path} {
			if err := root.RemoveAll(p); err != nil {
				errs = append(errs, err)
			}
		}
		root.Close()
	}
	errs = append(errs, store.DeleteJob(m.o.DB, id))
	return errors.Join(errs...)
}

// stop cancels a runner and waits for it to exit.
func (m *Manager) stop(id string) {
	m.mu.Lock()
	r := m.run[id]
	m.mu.Unlock()
	if r != nil {
		r.cancel()
		<-r.done
	}
}

// Run is the reconcile loop. It returns when ctx ends, after stopping all runners.
func (m *Manager) Run(ctx context.Context) error {
	t := time.NewTicker(m.o.Tick)
	defer t.Stop()
	for {
		if err := m.reconcile(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "jobs: reconcile:", err) // ponytail: stderr until structured logging lands
		}
		select {
		case <-ctx.Done():
			m.mu.Lock()
			ids := make([]string, 0, len(m.run))
			for id := range m.run {
				ids = append(ids, id)
			}
			m.mu.Unlock()
			for _, id := range ids {
				m.stop(id)
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.eng != nil {
				return m.eng.Close()
			}
			return nil
		case <-t.C:
		case <-m.kick:
		}
	}
}

func (m *Manager) reconcile(ctx context.Context) error {
	jobs, err := store.ListJobs(m.o.DB)
	if err != nil {
		return err
	}
	slices.Reverse(jobs) // oldest first: FIFO queue
	m.mu.Lock()
	defer m.mu.Unlock()
	active := len(m.run)
	for _, j := range jobs {
		if j.State == "done" || (j.State == "failed" && j.Want == "running") {
			continue
		}
		r := m.run[j.ID]
		switch {
		case j.Want == "paused" && r != nil:
			r.cancel() // the runner records "paused" when it exits
		case j.Want == "paused" && j.State != "paused" && j.State != "failed":
			store.SetJob(m.o.DB, j.ID, map[string]any{"state": "paused"})
		case j.Want == "running" && r == nil && active < m.o.MaxActive:
			m.start(ctx, j)
			active++
		case j.Want == "running" && r == nil && j.State != "queued":
			store.SetJob(m.o.DB, j.ID, map[string]any{"state": "queued"}) // e.g. after a crash
		}
	}
	return nil
}

// start must be called with m.mu held.
func (m *Manager) start(parent context.Context, j store.Job) {
	ctx, cancel := context.WithCancel(parent)
	r := &runner{cancel: cancel, done: make(chan struct{})}
	m.run[j.ID] = r
	go func() {
		var err error
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("internal error: %v", p) // bulkhead: this job fails, the server lives
			}
			m.finish(ctx, j.ID, err)
			close(r.done)
		}()
		if j.Kind == "torrent" {
			err = m.runTorrent(ctx, r, j)
		} else {
			err = m.runWeb(ctx, r, j)
		}
	}()
}

func (m *Manager) finish(ctx context.Context, id string, err error) {
	m.mu.Lock()
	delete(m.run, id)
	delete(m.last, id)
	m.mu.Unlock()
	j, gerr := m.get(id)
	if gerr != nil {
		return // cancelled and deleted
	}
	f := map[string]any{}
	switch {
	case err == nil:
		f["state"], f["error"] = "done", ""
	case ctx.Err() != nil && j.Want == "paused":
		f["state"] = "paused"
	case ctx.Err() != nil:
		f["state"] = "queued" // mode switch or shutdown: resume on the next pass or next start
	default:
		f["state"], f["error"] = "failed", err.Error()
	}
	store.SetJob(m.o.DB, id, f)
	m.poke()
}

func (m *Manager) runWeb(ctx context.Context, r *runner, j store.Job) error {
	var meta webMeta
	json.Unmarshal(j.Meta, &meta)
	hdr := http.Header{}
	for k, v := range meta.Header {
		hdr.Set(k, v)
	}
	save := func(f map[string]any) error {
		b, _ := json.Marshal(meta)
		f["meta"] = json.RawMessage(b)
		return store.SetJob(m.o.DB, j.ID, f)
	}
	path := j.Path
	if meta.HTTP == nil {
		store.SetJob(m.o.DB, j.ID, map[string]any{"state": "probing"})
		st, err := httpdl.Probe(ctx, m.o.Client, j.Source, hdr)
		if err != nil {
			return err
		}
		meta.HTTP = &st
		if path, err = m.unique(st.Name); err != nil {
			return err
		}
		if err := save(map[string]any{"name": st.Name, "path": path, "total": st.Size}); err != nil {
			return err
		}
	}
	store.SetJob(m.o.DB, j.ID, map[string]any{"state": "downloading"})
	conns := m.o.Connections
	if j.Mode == "stream" {
		conns = 1
	}
	part := filepath.Join(m.o.Downloads, path+".part")
	d := httpdl.New(*meta.HTTP, part, httpdl.Options{
		Client: m.o.Client, Header: hdr, Connections: conns,
		OnSave: func(st httpdl.State) {
			meta.HTTP = &st
			var done int64
			for _, s := range st.Segments {
				done += s.Done
			}
			save(map[string]any{"done": done, "total": st.Size})
		},
	})
	r.mu.Lock()
	r.dl, r.part = d, part
	r.mu.Unlock()
	err := d.Run(ctx)
	if errors.Is(err, httpdl.ErrChanged) {
		meta.HTTP = nil // start over on resume
		save(map[string]any{"done": 0})
		return fmt.Errorf("%w (resume to download it again)", err)
	}
	if err != nil {
		return err
	}
	final := path
	if _, err := os.Lstat(filepath.Join(m.o.Downloads, final)); err == nil {
		if final, err = m.unique(meta.HTTP.Name); err != nil {
			return err
		}
	}
	if err := os.Rename(part, filepath.Join(m.o.Downloads, final)); err != nil {
		return err
	}
	return store.SetJob(m.o.DB, j.ID, map[string]any{"path": final, "done": d.Completed(), "total": d.State().Size})
}

// unique picks a free name in the download folder: "a.mp4", "a (1).mp4", ...
// ponytail: two jobs probing the same name in the same instant could pick it twice; the final
// rename re-checks, so the worst case is a " (n)" suffix decided late.
func (m *Manager) unique(name string) (string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 0; i < 1000; i++ {
		n := name
		if i > 0 {
			n = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		_, e1 := os.Lstat(filepath.Join(m.o.Downloads, n))
		_, e2 := os.Lstat(filepath.Join(m.o.Downloads, n+".part"))
		if os.IsNotExist(e1) && os.IsNotExist(e2) {
			return n, nil
		}
	}
	return "", fmt.Errorf("no free file name for %q", name)
}

func (m *Manager) engine() (engine.Engine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.eng == nil {
		if m.o.NewEngine == nil {
			return nil, errors.New("torrents are not enabled")
		}
		e, err := m.o.NewEngine()
		if err != nil {
			return nil, err
		}
		m.eng = e
	}
	return m.eng, nil
}

func (m *Manager) runTorrent(ctx context.Context, r *runner, j store.Job) error {
	eng, err := m.engine()
	if err != nil {
		return err
	}
	var meta torMeta
	json.Unmarshal(j.Meta, &meta)
	src := meta.Torrent
	if len(src) == 0 {
		src = []byte(meta.Magnet)
	}
	t, _, err := eng.Add(src)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.tor = t
	r.mu.Unlock()
	if t.Files() == nil {
		store.SetJob(m.o.DB, j.ID, map[string]any{"state": "metadata"})
		wctx, cancel := context.WithTimeout(ctx, m.o.MetadataTimeout)
		err := t.WaitInfo(wctx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("no peers found: metadata did not arrive within %v", m.o.MetadataTimeout)
		}
	}
	var total int64
	for _, f := range t.Files() {
		total += f.Length
	}
	f := map[string]any{"name": t.Name(), "path": t.Name(), "total": total}
	if meta.Started {
		store.SetJob(m.o.DB, j.ID, map[string]any{"state": "checking"})
		if err := t.Recheck(ctx); err != nil { // spike: progress reads 0 after a restart until rechecked
			return err
		}
	} else {
		meta.Started = true
		b, _ := json.Marshal(meta)
		f["meta"] = json.RawMessage(b)
	}
	f["state"] = "downloading"
	if err := store.SetJob(m.o.DB, j.ID, f); err != nil {
		return err
	}
	t.Resume()
	// Both modes download everything; a player's reader raises priority where it reads, which is
	// what stream mode needs. ponytail: first/last-piece priority for stream mode comes with M2.
	t.Start()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-t.Done():
			// Seeding policy default: stop at 100% (scope §5.9). Files stay on disk.
			store.SetJob(m.o.DB, j.ID, map[string]any{"done": total})
			return t.Drop(false)
		case <-ctx.Done():
			t.Pause()
			store.SetJob(m.o.DB, j.ID, map[string]any{"done": t.Stats().Completed})
			return ctx.Err()
		case <-tick.C:
			store.SetJob(m.o.DB, j.ID, map[string]any{"done": t.Stats().Completed})
		}
	}
}

type Progress struct {
	ID    string  `json:"id"`
	Done  int64   `json:"done"`
	Total int64   `json:"total"`
	Rate  float64 `json:"rate"` // bytes/s
	Peers int     `json:"peers,omitempty"`
}

// Progress snapshots the running jobs (for SSE).
func (m *Manager) Progress() []Progress {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	out := make([]Progress, 0, len(m.run))
	for id, r := range m.run {
		p := Progress{ID: id, Total: -1}
		r.mu.Lock()
		switch {
		case r.dl != nil:
			p.Done, p.Total = r.dl.Completed(), r.dl.State().Size
		case r.tor != nil:
			s := r.tor.Stats()
			p.Done, p.Peers = s.Completed, s.Peers
			if s.Total > 0 {
				p.Total = s.Total
			}
		}
		r.mu.Unlock()
		if l, ok := m.last[id]; ok {
			if dt := now.Sub(l.at).Seconds(); dt > 0 {
				p.Rate = max(0, float64(p.Done-l.done)/dt)
			}
		}
		m.last[id] = sample{now, p.Done}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Progress) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// Open returns a seekable reader for a job's file: the finished file, a growing web download,
// or a torrent file (reads move piece priority). index picks the torrent file (-1 = largest).
func (m *Manager) Open(ctx context.Context, id string, index int) (io.ReadSeekCloser, string, error) {
	j, err := m.get(id)
	if err != nil {
		return nil, "", err
	}
	m.mu.Lock()
	r := m.run[id]
	m.mu.Unlock()
	if r == nil {
		if j.State == "done" && j.Kind == "web" {
			root, err := os.OpenRoot(m.o.Downloads)
			if err != nil {
				return nil, "", err
			}
			defer root.Close()
			f, err := root.Open(j.Path)
			return f, j.Path, err
		}
		return nil, "", ErrNotRunning
	}
	r.mu.Lock()
	dl, part, tor := r.dl, r.part, r.tor
	r.mu.Unlock()
	switch {
	case dl != nil:
		size := dl.State().Size
		if size <= 0 {
			return nil, "", errors.New("size unknown: cannot stream before it finishes")
		}
		f, err := os.Open(part)
		if err != nil {
			return nil, "", err
		}
		return &growing{ctx: ctx, f: f, dl: dl, size: size}, j.Path, nil
	case tor != nil:
		files := tor.Files()
		if files == nil {
			return nil, "", errors.New("metadata not available yet")
		}
		if index < 0 {
			for i, f := range files {
				if index < 0 || f.Length > files[index].Length {
					index = i
				}
			}
		}
		if index >= len(files) {
			return nil, "", fmt.Errorf("file index %d out of range", index)
		}
		return tor.Reader(index), files[index].Path, nil
	}
	return nil, "", ErrNotRunning
}

// growing reads a file that is still being downloaded, waiting for bytes that aren't there yet.
type growing struct {
	ctx  context.Context
	f    *os.File
	dl   *httpdl.Download
	size int64
	off  int64
}

func (g *growing) Read(p []byte) (int, error) {
	if g.off >= g.size {
		return 0, io.EOF
	}
	for {
		if n := g.dl.Available(g.off); n > 0 {
			k, err := g.f.ReadAt(p[:min(int64(len(p)), n)], g.off)
			g.off += int64(k)
			if err == io.EOF {
				err = nil
			}
			return k, err
		}
		select { // ponytail: 100 ms polling; switch to a condition variable if it shows in profiles
		case <-g.ctx.Done():
			return 0, g.ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (g *growing) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		off += g.off
	case io.SeekEnd:
		off += g.size
	}
	if off < 0 {
		return 0, errors.New("negative position")
	}
	g.off = off
	return off, nil
}

func (g *growing) Close() error { return g.f.Close() }
