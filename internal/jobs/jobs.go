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
	"syscall"
	"time"

	"golang.org/x/time/rate"

	"github.com/sadatdaniel/download_me_not/internal/engine"
	"github.com/sadatdaniel/download_me_not/internal/httpdl"
	"github.com/sadatdaniel/download_me_not/internal/store"
	"github.com/sadatdaniel/download_me_not/internal/video"
)

type Options struct {
	DB              *sql.DB
	Downloads       string
	Client          *http.Client                    // netguard.Client() in production
	NewEngine       func() (engine.Engine, error)   // called on the first torrent job only
	MaxActive       int                             // default 3
	Connections     int                             // per web download, default 16
	MetadataTimeout time.Duration                   // default 5 min
	Tick            time.Duration                   // reconcile interval, default 2 s
	Allowed         func(kind string) bool          // provider toggles; nil = all allowed
	Limiter         *rate.Limiter                   // global download limit for web jobs
	Seed            SeedPolicy                      // default seeding policy for torrents
	DiskReserve     int64                           // keep this much free in the download folder; default 1 GiB
	FreeSpace       func(dir string) (int64, error) // tests override; default statfs
	Post            Post                            // default post-completion steps
	LearnTrackers   bool                            // swarm memory: remember good trackers, add them to new public torrents
	// TorrentGate returns a reason when torrents must not run (kill switch: the bound VPN interface is
	// down). Running torrents stop and wait, and resume when it returns "". nil = always open.
	TorrentGate func() string
	// Video returns how to run yt-dlp, or nil when it isn't installed. Called per job, so a tool
	// installed while the server runs is picked up.
	Video func() *video.Runner
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
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	dl      *httpdl.Download
	part    string
	tor     engine.Torrent
	seeding bool   // finished downloading; does not take a download slot
	reason  string // why the manager stopped it (e.g. disk full), shown as the job error
	vdone   int64  // video jobs: progress reported by yt-dlp
	vtotal  int64
	video   bool
}

type sample struct {
	at   time.Time
	done int64
}

// Post lists what happens after a download completes (scope §5.9 pipeline). Drive copy comes in M3.
type Post struct {
	Extract       bool `json:"extract,omitempty"`       // unpack archives into a folder next to them
	DeleteArchive bool `json:"deleteArchive,omitempty"` // remove the archive after a successful unpack
}

type webMeta struct {
	Post   *Post             `json:"post,omitempty"`
	Header map[string]string `json:"header,omitempty"`
	HTTP   *httpdl.State     `json:"http,omitempty"`
}

type videoMeta struct {
	Format string            `json:"format,omitempty"` // preset or yt-dlp format id
	Header map[string]string `json:"header,omitempty"`
}

type torMeta struct {
	Torrent  []byte         `json:"torrent,omitempty"`
	Magnet   string         `json:"magnet,omitempty"`
	Started  bool           `json:"started"`            // data may exist on disk: recheck before resuming
	Trackers [][]string     `json:"trackers,omitempty"` // user-edited tracker list, re-applied on start
	Seed     *SeedPolicy    `json:"seed,omitempty"`     // per-torrent override
	Post     *Post          `json:"post,omitempty"`
	Files    map[int]string `json:"files,omitempty"` // per-file priority: skip, normal, high
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
	if o.DiskReserve <= 0 {
		o.DiskReserve = 1 << 30
	}
	if o.FreeSpace == nil {
		o.FreeSpace = freeSpace
	}
	if o.Seed.Policy == "" {
		o.Seed.Policy = "stop"
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
	Source   string            `json:"source,omitempty"`   // URL or magnet link
	Torrent  []byte            `json:"torrent,omitempty"`  // .torrent file content (base64 in JSON)
	Mode     string            `json:"mode,omitempty"`     // standard (default) or stream
	Header   map[string]string `json:"header,omitempty"`   // e.g. Cookie, Referer, User-Agent from the browser
	Seed     *SeedPolicy       `json:"seed,omitempty"`     // torrents: override the default seeding policy
	Priority int               `json:"priority,omitempty"` // higher starts first
	Post     *Post             `json:"post,omitempty"`     // after completion; default from settings
	// Format makes it a video job (yt-dlp): a preset (best, 1080p, 720p, 480p, m4a, opus, mp3) or a
	// format id from /v1/probe. Without it, a URL that turns out to be a web page becomes a video job.
	Format string `json:"format,omitempty"`
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
	j := store.Job{ID: newID(), Mode: r.Mode, Want: "running", State: "queued", Total: -1, Priority: r.Priority, Created: now, Updated: now}
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
		meta = torMeta{Torrent: r.Torrent, Magnet: strings.TrimPrefix(r.Source, "torrent:"), Seed: r.Seed, Post: r.Post}
	default:
		u, err := url.Parse(r.Source)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return store.Job{}, false, ErrBadSource
		}
		j.Kind, j.Source = "web", r.Source
		meta = webMeta{Header: r.Header, Post: r.Post}
		if r.Format != "" {
			if _, err := video.Selector(r.Format); err != nil {
				return store.Job{}, false, err
			}
			j.Kind = "video"
			meta = videoMeta{Format: r.Format, Header: r.Header}
		}
	}
	if m.o.Allowed != nil && !m.o.Allowed(j.Kind) {
		return store.Job{}, false, fmt.Errorf("the %s provider is switched off", j.Kind)
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
	} else if t, err := m.torrentOf(id); err == nil && mode == "stream" {
		t.PrioritizeEnds(largest(t.Files()))
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
	free, ferr := m.o.FreeSpace(m.o.Downloads)
	lowDisk := ferr == nil && free < m.o.DiskReserve
	// Read jobs under the lock: finish() records a runner's final state under the same lock, so a
	// runner that just ended can't look like "running but missing" and be started twice.
	m.mu.Lock()
	defer m.mu.Unlock()
	jobs, err := store.ListJobs(m.o.DB)
	if err != nil {
		return err
	}
	// Higher priority first, then oldest first (FIFO).
	slices.SortStableFunc(jobs, func(a, b store.Job) int {
		if a.Priority != b.Priority {
			return b.Priority - a.Priority
		}
		return int(a.Created - b.Created)
	})
	gate := ""
	if m.o.TorrentGate != nil {
		gate = m.o.TorrentGate()
	}
	active := 0
	for _, r := range m.run {
		r.mu.Lock()
		if !r.seeding {
			active++
		}
		r.mu.Unlock()
	}
	for _, j := range jobs {
		if j.State == "done" || (j.State == "failed" && j.Want == "running") {
			continue
		}
		r := m.run[j.ID]
		need := j.Total - j.Done // bytes still to write; unknown (-1 total) counts as 0
		switch {
		case j.Want == "paused" && r != nil:
			r.cancel() // the runner records "paused" when it exits
		case j.Want == "paused" && j.State != "paused" && j.State != "failed":
			store.SetJob(m.o.DB, j.ID, map[string]any{"state": "paused"})
		case j.Want == "running" && j.Kind == "torrent" && gate != "" && r != nil:
			r.mu.Lock()
			r.reason = gate
			r.mu.Unlock()
			r.cancel()
		case j.Want == "running" && j.Kind == "torrent" && gate != "":
			if j.State != "queued" || j.Error != gate {
				store.SetJob(m.o.DB, j.ID, map[string]any{"state": "queued", "error": gate})
			}
		case j.Want == "running" && r != nil && lowDisk && j.State != "seeding":
			r.mu.Lock()
			r.reason = fmt.Sprintf("waiting for disk space: %d MiB free, keeping %d MiB in reserve", free>>20, m.o.DiskReserve>>20)
			r.mu.Unlock()
			r.cancel()
		case j.Want == "running" && r == nil && ferr == nil && free-max(need, 0) < m.o.DiskReserve:
			msg := fmt.Sprintf("waiting for disk space: needs %d MiB, %d MiB free", max(need, 0)>>20, free>>20)
			if j.State != "queued" || j.Error != msg {
				store.SetJob(m.o.DB, j.ID, map[string]any{"state": "queued", "error": msg})
			}
		case j.Want == "running" && r == nil && active < m.o.MaxActive:
			if strings.HasPrefix(j.Error, "waiting for disk space") || strings.HasPrefix(j.Error, "kill switch") {
				store.SetJob(m.o.DB, j.ID, map[string]any{"error": ""})
			}
			m.start(ctx, j)
			active++
		case j.Want == "running" && r == nil && j.State != "queued":
			store.SetJob(m.o.DB, j.ID, map[string]any{"state": "queued"}) // e.g. after a crash
		}
	}
	return nil
}

func freeSpace(dir string) (int64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil {
		return 0, err
	}
	return int64(s.Bavail) * int64(s.Bsize), nil
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
		switch j.Kind {
		case "torrent":
			err = m.runTorrent(ctx, r, j)
		case "video":
			err = m.runVideo(ctx, r, j)
		default:
			err = m.runWeb(ctx, r, j)
		}
	}()
}

func (m *Manager) finish(ctx context.Context, id string, err error) {
	m.mu.Lock()
	reason := ""
	if r := m.run[id]; r != nil {
		r.mu.Lock()
		reason = r.reason
		r.mu.Unlock()
	}
	// The runner is removed and its final state written under one lock hold (see reconcile).
	defer m.poke()
	defer m.mu.Unlock()
	delete(m.run, id)
	delete(m.last, id)
	j, gerr := m.get(id)
	if gerr != nil {
		return // cancelled and deleted
	}
	f := map[string]any{}
	switch {
	case err == nil:
		f["state"], f["error"] = "done", reason // non-fatal notes, e.g. an archive that could not be unpacked
	case ctx.Err() != nil && j.Want == "paused":
		f["state"] = "paused"
	case ctx.Err() != nil:
		f["state"] = "queued" // mode switch, disk guard or shutdown: resume on the next pass or next start
		if reason != "" {
			f["error"] = reason
		}
	default:
		f["state"], f["error"] = "failed", err.Error()
	}
	store.SetJob(m.o.DB, id, f)
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
		// A web page, not a file: hand it to yt-dlp (the way IDM's video grabber works).
		if strings.HasPrefix(st.Type, "text/html") && m.o.Video != nil && (m.o.Allowed == nil || m.o.Allowed("video")) {
			b, _ := json.Marshal(videoMeta{Format: "best", Header: meta.Header})
			if err := store.SetJob(m.o.DB, j.ID, map[string]any{"kind": "video", "meta": json.RawMessage(b)}); err != nil {
				return err
			}
			j.Kind, j.Meta = "video", b
			return m.runVideo(ctx, r, j)
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
		Limiter: m.o.Limiter,
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
	if err := store.SetJob(m.o.DB, j.ID, map[string]any{"path": final, "done": d.Completed(), "total": d.State().Size}); err != nil {
		return err
	}
	m.removeFiles(m.postProcess(ctx, r, j.ID, meta.Post, []string{final}))
	return nil
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
	if len(meta.Trackers) > 0 {
		t.SetTrackers(meta.Trackers)
	}
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
	ownTrackers := t.Trackers() // before adding learned ones, so only real results get recorded
	if m.o.LearnTrackers && !t.Private() {
		m.addLearnedTrackers(t)
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
	// Both modes download everything; in stream mode the largest file's first and last pieces come
	// first, and a player's reader raises priority wherever it reads.
	t.Start() // synchronous here: metadata is known, so file priorities below aren't overwritten
	for i, p := range meta.Files {
		t.SetFilePriority(i, filePriority[p])
	}
	if j.Mode == "stream" {
		t.PrioritizeEnds(largest(t.Files()))
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-t.Done():
			store.SetJob(m.o.DB, j.ID, map[string]any{"done": t.Stats().Completed})
			var paths []string
			for _, f := range t.Files() {
				paths = append(paths, f.Path)
			}
			if m.o.LearnTrackers && !t.Private() {
				store.LearnTrackers(m.o.DB, publicTrackers(ownTrackers), true)
			}
			del := m.postProcess(ctx, r, j.ID, meta.Post, paths)
			err := m.seed(ctx, r, j.ID, t, total, meta.Seed)
			if err == nil {
				m.removeFiles(del) // only after seeding: peers may still need the archive
			}
			return err
		case <-ctx.Done():
			t.Pause()
			store.SetJob(m.o.DB, j.ID, map[string]any{"done": t.Stats().Completed})
			return ctx.Err()
		case <-tick.C:
			store.SetJob(m.o.DB, j.ID, map[string]any{"done": t.Stats().Completed})
		}
	}
}

var filePriority = map[string]engine.Priority{"skip": engine.Skip, "normal": engine.Normal, "high": engine.High}

// SeedPolicy decides what happens after a torrent completes (scope §5.9). Default: stop.
type SeedPolicy struct {
	Policy  string  `json:"policy" enum:"stop,ratio,time,forever"`
	Ratio   float64 `json:"ratio,omitempty"`   // policy ratio: stop when uploaded >= ratio * size
	Minutes int     `json:"minutes,omitempty"` // policy time: stop after this long
}

// seed keeps a finished torrent uploading according to its policy, then drops it (files stay).
// ponytail: upload and time counters restart with the server; persist them if users need exact ratios.
func (m *Manager) seed(ctx context.Context, r *runner, id string, t engine.Torrent, total int64, override *SeedPolicy) error {
	p := m.o.Seed
	if override != nil {
		p = *override
	}
	if p.Policy == "" || p.Policy == "stop" {
		return t.Drop(false)
	}
	r.mu.Lock()
	r.seeding = true // frees the download slot
	r.mu.Unlock()
	store.SetJob(m.o.DB, id, map[string]any{"state": "seeding"})
	m.poke()
	start, up0 := time.Now(), t.Stats().Uploaded
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		up := t.Stats().Uploaded - up0
		switch {
		case p.Policy == "ratio" && total > 0 && float64(up) >= p.Ratio*float64(total),
			p.Policy == "time" && time.Since(start) >= time.Duration(p.Minutes)*time.Minute:
			return t.Drop(false)
		}
		select {
		case <-ctx.Done():
			t.Pause()
			return ctx.Err()
		case <-tick.C:
		}
	}
}

type Progress struct {
	ID    string  `json:"id"`
	Done  int64   `json:"done"`
	Total int64   `json:"total"`
	Rate  float64 `json:"rate"` // bytes/s
	Peers int     `json:"peers,omitempty"`
	Hint  string  `json:"hint,omitempty"` // torrents: why it is slow or stalled
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
		case r.video:
			p.Done, p.Total = r.vdone, r.vtotal
		case r.dl != nil:
			p.Done, p.Total = r.dl.Completed(), r.dl.State().Size
		case r.tor != nil:
			s := r.tor.Stats()
			p.Done, p.Peers = s.Completed, s.Peers
			p.Hint = m.hint(r.tor.Swarm(), s)
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
		if j.State == "done" && j.Path != "" {
			root, err := os.OpenRoot(m.o.Downloads)
			if err != nil {
				return nil, "", err
			}
			defer root.Close()
			f, err := root.Open(j.Path)
			if err != nil {
				return nil, "", err
			}
			if fi, err := f.Stat(); err != nil || fi.IsDir() {
				f.Close()
				return nil, "", errors.New("this download is a folder: browse it with /v1/fs")
			}
			return f, j.Path, nil
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

// SetPriority reorders the queue: higher starts first. Running jobs keep running.
func (m *Manager) SetPriority(id string, p int) error {
	if _, err := m.get(id); err != nil {
		return err
	}
	if err := store.SetJob(m.o.DB, id, map[string]any{"priority": p}); err != nil {
		return err
	}
	m.poke()
	return nil
}
