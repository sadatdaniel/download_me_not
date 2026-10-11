package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/sadatdaniel/download_me_not/internal/cookies"
	"github.com/sadatdaniel/download_me_not/internal/httpdl"
	"github.com/sadatdaniel/download_me_not/internal/store"
	"github.com/sadatdaniel/download_me_not/internal/video"
)

// runVideo downloads a video page with yt-dlp. Pausing kills yt-dlp; resuming runs it again and it
// continues its own .part file.
func (m *Manager) runVideo(ctx context.Context, r *runner, j store.Job) error {
	if m.o.Video == nil {
		return errors.New("video downloads are not available")
	}
	vr, cleanup, err := m.videoRunner(j.Source)
	if err != nil {
		return err
	}
	defer cleanup()
	var meta videoMeta
	json.Unmarshal(j.Meta, &meta)
	r.mu.Lock()
	r.ext = true
	r.mu.Unlock()
	store.SetJob(m.o.DB, j.ID, map[string]any{"state": "downloading"})
	var last time.Time
	file, err := vr.Download(ctx, j.Source, meta.Format, m.o.Downloads, func(done, total int64) {
		r.mu.Lock()
		r.vdone, r.vtotal = done, total
		r.mu.Unlock()
		if time.Since(last) > 2*time.Second {
			last = time.Now()
			store.SetJob(m.o.DB, j.ID, map[string]any{"done": done, "total": total})
		}
	})
	if err != nil {
		return err
	}
	rel, _ := filepath.Rel(m.o.Downloads, file)
	r.mu.Lock()
	done := r.vdone
	r.mu.Unlock()
	if err := store.SetJob(m.o.DB, j.ID, map[string]any{"name": filepath.Base(file), "path": rel, "done": done, "total": done}); err != nil {
		return err
	}
	m.queueDriveCopy(r, m.post(nil), rel)
	return nil
}

// ProbeResult tells the app what a URL is before it adds the download (the two-step flow, §5.2).
type ProbeResult struct {
	Kind    string         `json:"kind"` // web, video or torrent
	Name    string         `json:"name,omitempty"`
	Size    int64          `json:"size,omitempty"`
	Video   *video.Media   `json:"video,omitempty"`
	Presets []video.Preset `json:"presets,omitempty"`
}

func (m *Manager) Probe(ctx context.Context, src string, header map[string]string) (ProbeResult, error) {
	if strings.HasPrefix(src, "magnet:") {
		return ProbeResult{Kind: "torrent"}, nil
	}
	u, err := url.Parse(src)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ProbeResult{}, ErrBadSource
	}
	hdr := http.Header{}
	for k, v := range header {
		hdr.Set(k, v)
	}
	m.withCookies(src, hdr)
	st, err := httpdl.Probe(ctx, m.o.Client, src, hdr)
	if err != nil {
		return ProbeResult{}, err
	}
	if !strings.HasPrefix(st.Type, "text/html") || m.o.Video == nil || (m.o.Allowed != nil && !m.o.Allowed("video")) {
		return ProbeResult{Kind: "web", Name: st.Name, Size: max(st.Size, 0)}, nil
	}
	vr, cleanup, err := m.videoRunner(src)
	if err != nil {
		return ProbeResult{}, err
	}
	defer cleanup()
	info, err := vr.Probe(ctx, src)
	if err != nil {
		return ProbeResult{}, err
	}
	return ProbeResult{Kind: "video", Video: &info, Presets: video.Presets}, nil
}

// withCookies adds the stored cookies and matching User-Agent for src unless the request already
// carries its own (e.g. sent by the browser extension).
func (m *Manager) withCookies(src string, h http.Header) {
	if m.o.Cookies == nil || h.Get("Cookie") != "" {
		return
	}
	cs, ua := m.o.Cookies.For(src)
	if len(cs) > 0 {
		h.Set("Cookie", cookies.Header(cs))
	}
	if ua != "" && h.Get("User-Agent") == "" {
		h.Set("User-Agent", ua)
	}
}

// videoRunner returns yt-dlp's runner with this URL's cookies in a temporary file (deleted by done).
func (m *Manager) videoRunner(src string) (*video.Runner, func(), error) {
	vr := m.o.Video()
	if vr == nil {
		return nil, nil, errors.New("yt-dlp is not installed: run `dmn doctor`")
	}
	r := *vr
	done := func() {}
	if m.o.Cookies != nil {
		p, ua, cleanup, err := m.o.Cookies.TempFile(src)
		if err != nil {
			return nil, nil, err
		}
		r.Cookies, r.UA, done = p, ua, cleanup
	}
	return &r, done, nil
}
