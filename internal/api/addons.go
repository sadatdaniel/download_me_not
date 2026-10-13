package api

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/sadatdaniel/download_me_not/internal/addons"
	"github.com/sadatdaniel/download_me_not/internal/config"
	"github.com/sadatdaniel/download_me_not/internal/jobs"
	"github.com/sadatdaniel/download_me_not/internal/store"
)

// Stremio add-ons (experimental). The server is the add-on client: it fetches through the SSRF guard, caches,
// and merges answers from every installed add-on. Torrent streams become stream-mode downloads on this server.
// Nothing is installed by default; what users add is their responsibility.

type addonInfo struct {
	URL      string           `json:"url"`
	Enabled  bool             `json:"enabled"`
	Manifest *addons.Manifest `json:"manifest,omitempty"`
	Error    string           `json:"error,omitempty"`
}

type addonsOut struct {
	Enabled bool        `json:"enabled"`
	Addons  []addonInfo `json:"addons"`
}

type catalogRow struct {
	Addon string        `json:"addon"`
	Type  string        `json:"type"`
	ID    string        `json:"id"`
	Name  string        `json:"name"`
	Metas []addons.Meta `json:"metas"`
}

type addonState struct {
	mu       sync.Mutex
	enabled  bool
	urls     []string
	disabled []string
}

// defaultAddons are installed the first time add-ons are switched on: what Stremio itself ships with that
// fits here (titles, subtitles, and links to legal streaming services). None of them offers torrents.
var defaultAddons = []string{
	"https://v3-cinemeta.strem.io",
	"https://opensubtitles-v3.strem.io",
	"https://watchhub.strem.io",
}

func (s *server) addonList() (bool, []string) {
	s.addon.mu.Lock()
	defer s.addon.mu.Unlock()
	return s.addon.enabled, slices.Clone(s.addon.urls)
}

// installed returns the add-ons whose manifests load (from cache usually), in the order the user added them.
func (s *server) installed(ctx context.Context) []addons.Addon {
	_, urls := s.addonList()
	s.addon.mu.Lock()
	urls = slices.DeleteFunc(urls, func(u string) bool { return slices.Contains(s.addon.disabled, u) })
	s.addon.mu.Unlock()
	out := make([]addons.Addon, len(urls))
	ok := make([]bool, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Go(func() {
			if m, err := s.Addons.Manifest(ctx, u); err == nil {
				out[i], ok[i] = addons.Addon{URL: u, Manifest: m}, true
			}
		})
	}
	wg.Wait()
	var res []addons.Addon
	for i := range out {
		if ok[i] {
			res = append(res, out[i])
		}
	}
	return res
}

// parallel runs f for each item, at most 6 at a time (add-ons are often slow), and keeps the order.
func parallel[T, R any](items []T, f func(T) (R, bool)) []R {
	res := make([]R, len(items))
	keep := make([]bool, len(items))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i, it := range items {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			res[i], keep[i] = f(it)
		})
	}
	wg.Wait()
	var out []R
	for i, r := range res {
		if keep[i] {
			out = append(out, r)
		}
	}
	return out
}

func (s *server) needAddons() error {
	if s.Addons == nil {
		return huma.Error503ServiceUnavailable("add-ons are not available on this server")
	}
	if on, _ := s.addonList(); !on {
		return huma.Error404NotFound("add-ons are switched off (Settings, Add-ons)")
	}
	return nil
}

func (s *server) saveAddons() error {
	on, urls := s.addonList()
	if err := config.Set(s.CfgPath, "addons.enabled", on); err != nil {
		return err
	}
	if err := config.Set(s.CfgPath, "addons.urls", urls); err != nil {
		return err
	}
	s.addon.mu.Lock()
	disabled := slices.Clone(s.addon.disabled)
	s.addon.mu.Unlock()
	return config.Set(s.CfgPath, "addons.disabled", disabled)
}

func (s *server) addonRoutes(api huma.API) {
	s.addon.enabled, s.addon.urls = s.Cfg.Addons.Enabled, slices.Clone(s.Cfg.Addons.URLs)
	s.addon.disabled = slices.Clone(s.Cfg.Addons.Disabled)

	huma.Register(api, op("addons", "GET", "/v1/addons", "read", "Installed Stremio add-ons and whether the feature is on"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body addonsOut }, error) {
			on, urls := s.addonList()
			out := addonsOut{Enabled: on, Addons: []addonInfo{}}
			if s.Addons == nil {
				return body(out), nil
			}
			out.Addons = parallel(urls, func(u string) (addonInfo, bool) {
				s.addon.mu.Lock()
				on := !slices.Contains(s.addon.disabled, u)
				s.addon.mu.Unlock()
				m, err := s.Addons.Manifest(ctx, u)
				if err != nil {
					return addonInfo{URL: u, Enabled: on, Error: err.Error()}, true
				}
				return addonInfo{URL: u, Enabled: on, Manifest: &m}, true
			})
			return body(out), nil
		})

	huma.Register(api, op("addons-enable", "PUT", "/v1/addons/enabled", "admin", "Switch Stremio add-on support on or off"),
		func(ctx context.Context, in *struct {
			Body struct {
				Enabled bool `json:"enabled"`
			}
		}) (*struct{}, error) {
			s.addon.mu.Lock()
			s.addon.enabled = in.Body.Enabled
			if in.Body.Enabled && len(s.addon.urls) == 0 && len(s.addon.disabled) == 0 {
				s.addon.urls = slices.Clone(defaultAddons) // first time on: the ethical defaults
			}
			s.addon.mu.Unlock()
			return nil, s.saveAddons()
		})

	huma.Register(api, op("addons-add", "POST", "/v1/addons", "admin", "Install an add-on by its manifest URL (checked first)"),
		func(ctx context.Context, in *struct {
			Body struct {
				URL string `json:"url" minLength:"8" doc:"https://…/manifest.json or stremio://…"`
			}
		}) (*struct{ Body addonInfo }, error) {
			if s.Addons == nil {
				return nil, huma.Error503ServiceUnavailable("add-ons are not available on this server")
			}
			base, err := addons.Base(in.Body.URL)
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			m, err := s.Addons.Manifest(ctx, base)
			if err != nil {
				return nil, huma.Error400BadRequest("could not load the add-on: " + err.Error())
			}
			s.addon.mu.Lock()
			if !slices.Contains(s.addon.urls, base) {
				s.addon.urls = append(s.addon.urls, base)
			}
			s.addon.mu.Unlock()
			return body(addonInfo{URL: base, Manifest: &m}), s.saveAddons()
		})

	huma.Register(api, op("addons-remove", "DELETE", "/v1/addons", "admin", "Uninstall an add-on"),
		func(ctx context.Context, in *struct {
			URL string `query:"url" required:"true"`
		}) (*struct{}, error) {
			s.addon.mu.Lock()
			s.addon.urls = slices.DeleteFunc(s.addon.urls, func(u string) bool { return u == in.URL })
			s.addon.disabled = slices.DeleteFunc(s.addon.disabled, func(u string) bool { return u == in.URL })
			s.addon.mu.Unlock()
			return nil, s.saveAddons()
		})

	huma.Register(api, op("addons-state", "PUT", "/v1/addons/state", "admin", "Switch one installed add-on on or off"),
		func(ctx context.Context, in *struct {
			Body struct {
				URL     string `json:"url"`
				Enabled bool   `json:"enabled"`
			}
		}) (*struct{}, error) {
			s.addon.mu.Lock()
			s.addon.disabled = slices.DeleteFunc(s.addon.disabled, func(u string) bool { return u == in.Body.URL })
			if !in.Body.Enabled {
				s.addon.disabled = append(s.addon.disabled, in.Body.URL)
			}
			s.addon.mu.Unlock()
			return nil, s.saveAddons()
		})

	huma.Register(api, op("addons-home", "GET", "/v1/addons/home", "read", "First page of every catalog that needs no search, for the Discover screen"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body []catalogRow }, error) {
			if err := s.needAddons(); err != nil {
				return nil, err
			}
			type job struct {
				a addons.Addon
				c addons.Catalog
			}
			var jobs []job
			for _, a := range s.installed(ctx) {
				for _, c := range a.Manifest.Catalogs {
					if len(c.Required()) == 0 && len(jobs) < 30 {
						jobs = append(jobs, job{a, c})
					}
				}
			}
			rows := parallel(jobs, func(j job) (catalogRow, bool) {
				metas, err := s.Addons.Catalog(ctx, j.a, j.c.Type, j.c.ID, nil)
				return catalogRow{Addon: j.a.Manifest.Name, Type: j.c.Type, ID: j.c.ID, Name: j.c.Name, Metas: metas},
					err == nil && len(metas) > 0
			})
			if rows == nil {
				rows = []catalogRow{}
			}
			return body(rows), nil
		})

	huma.Register(api, op("addons-search", "GET", "/v1/addons/search", "read", "Search every catalog that supports search"),
		func(ctx context.Context, in *struct {
			Q string `query:"q" required:"true" minLength:"1"`
		}) (*struct{ Body []catalogRow }, error) {
			if err := s.needAddons(); err != nil {
				return nil, err
			}
			type job struct {
				a addons.Addon
				c addons.Catalog
			}
			var jobs []job
			for _, a := range s.installed(ctx) {
				for _, c := range a.Manifest.Catalogs {
					// Search catalogs may require "search"; any other required extra can't be filled here.
					if c.Supports("search") && !slices.ContainsFunc(c.Required(), func(r string) bool { return r != "search" }) {
						jobs = append(jobs, job{a, c})
					}
				}
			}
			rows := parallel(jobs, func(j job) (catalogRow, bool) {
				metas, err := s.Addons.Catalog(ctx, j.a, j.c.Type, j.c.ID, [][2]string{{"search", in.Q}})
				return catalogRow{Addon: j.a.Manifest.Name, Type: j.c.Type, ID: j.c.ID, Name: j.c.Name, Metas: metas},
					err == nil && len(metas) > 0
			})
			if rows == nil {
				rows = []catalogRow{}
			}
			return body(rows), nil
		})

	huma.Register(api, op("addons-meta", "GET", "/v1/addons/meta", "read", "Details of a title (and its episodes) from the first add-on that has them"),
		func(ctx context.Context, in *struct {
			Type string `query:"type" required:"true"`
			ID   string `query:"id" required:"true"`
		}) (*struct{ Body addons.Meta }, error) {
			if err := s.needAddons(); err != nil {
				return nil, err
			}
			for _, a := range s.installed(ctx) {
				if a.Handles("meta", in.Type, in.ID) {
					if m, err := s.Addons.Meta(ctx, a, in.Type, in.ID); err == nil && m.ID != "" {
						return body(m), nil
					}
				}
			}
			return nil, huma.Error404NotFound("no add-on has details for this title")
		})

	huma.Register(api, op("addons-streams", "GET", "/v1/addons/streams", "read", "Ways to watch a title, from every add-on that offers streams"),
		func(ctx context.Context, in *struct {
			Type string `query:"type" required:"true"`
			ID   string `query:"id" required:"true" doc:"title id, or an episode id such as tt0108778:1:1"`
		}) (*struct{ Body []addons.Stream }, error) {
			if err := s.needAddons(); err != nil {
				return nil, err
			}
			var with []addons.Addon
			for _, a := range s.installed(ctx) {
				if a.Handles("stream", in.Type, in.ID) {
					with = append(with, a)
				}
			}
			lists := parallel(with, func(a addons.Addon) ([]addons.Stream, bool) {
				st, err := s.Addons.Streams(ctx, a, in.Type, in.ID)
				return st, err == nil
			})
			out := []addons.Stream{}
			for _, l := range lists {
				out = append(out, l...)
			}
			return body(out), nil
		})

	huma.Register(api, op("addons-subtitles", "GET", "/v1/addons/subtitles", "read", "Subtitles for a title from every add-on that offers them"),
		func(ctx context.Context, in *struct {
			Type      string `query:"type" required:"true"`
			ID        string `query:"id" required:"true"`
			VideoSize int64  `query:"videoSize"`
			VideoHash string `query:"videoHash"`
			Filename  string `query:"filename"`
		}) (*struct{ Body []addons.Subtitle }, error) {
			if err := s.needAddons(); err != nil {
				return nil, err
			}
			extra := [][2]string{{"videoHash", in.VideoHash}, {"filename", in.Filename}}
			if in.VideoSize > 0 {
				extra = append(extra, [2]string{"videoSize", strconv.FormatInt(in.VideoSize, 10)})
			}
			var with []addons.Addon
			for _, a := range s.installed(ctx) {
				if a.Handles("subtitles", in.Type, in.ID) {
					with = append(with, a)
				}
			}
			lists := parallel(with, func(a addons.Addon) ([]addons.Subtitle, bool) {
				l, err := s.Addons.Subtitles(ctx, a, in.Type, in.ID, extra)
				return l, err == nil
			})
			out := []addons.Subtitle{}
			for _, l := range lists {
				out = append(out, l...)
			}
			return body(out), nil
		})

	type playOut struct {
		Job      *store.Job `json:"job,omitempty" doc:"the download on this server; play it through its stream link"`
		FileIdx  int        `json:"fileIdx" doc:"file inside the torrent to play (-1 = the largest)"`
		External string     `json:"external,omitempty" doc:"a web page to open (e.g. a legal streaming service); nothing is downloaded"`
	}
	huma.Register(api, op("addons-play", "POST", "/v1/addons/play", "write",
		"Start a stream from an add-on on this server: torrents and direct links become downloads (stream mode plays while downloading); external links are returned to open"),
		func(ctx context.Context, in *struct {
			Body struct {
				Stream addons.Stream `json:"stream"`
				Name   string        `json:"name,omitempty" doc:"title, used as the download name"`
				Mode   string        `json:"mode,omitempty" enum:"stream,standard" default:"stream" doc:"stream: play while downloading; standard: download it"`
			}
		}) (*struct{ Body playOut }, error) {
			if err := s.needAddons(); err != nil {
				return nil, err
			}
			st, mode := in.Body.Stream, in.Body.Mode
			if mode == "" {
				mode = "stream"
			}
			out := playOut{FileIdx: -1}
			if st.FileIdx != nil {
				out.FileIdx = *st.FileIdx
			}
			var req jobs.AddRequest
			switch {
			case st.InfoHash != "":
				m, err := addons.Magnet(st, in.Body.Name)
				if err != nil {
					return nil, huma.Error400BadRequest(err.Error())
				}
				req = jobs.AddRequest{Source: m, Mode: mode, User: userOf(ctx)}
			case st.URL != "":
				// Direct links also go through the server (the app only ever talks to the server): a web download,
				// in stream mode when playing, which the app plays through the job's stream link.
				req = jobs.AddRequest{Source: st.URL, Mode: mode, User: userOf(ctx)}
				if st.BehaviorHints.ProxyHeaders != nil {
					req.Header = st.BehaviorHints.ProxyHeaders.Request
				}
			case st.ExternalURL != "":
				out.External = st.ExternalURL
				return body(out), nil
			default:
				return nil, huma.Error400BadRequest("this stream can't be played here (a YouTube video)")
			}
			j, _, err := s.Jobs.Add(req)
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			j = own(j)
			out.Job = &j
			return body(out), nil
		})

	huma.Register(api, op("addons-subtitle-save", "POST", "/v1/addons/subtitles/save", "write",
		"Download a subtitle from an add-on and save it next to a video (never overwrites)"),
		func(ctx context.Context, in *struct {
			Body struct {
				URL   string `json:"url" minLength:"8"`
				Video string `json:"video" minLength:"1" doc:"the video's path in the download folder"`
				Lang  string `json:"lang,omitempty"`
			}
		}) (*struct{ Body map[string]string }, error) {
			if err := s.needAddons(); err != nil {
				return nil, err
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.Body.URL, nil)
			if err != nil || (req.URL.Scheme != "https" && req.URL.Scheme != "http") {
				return nil, huma.Error400BadRequest("invalid subtitle URL")
			}
			resp, err := s.Addons.HTTP.Do(req)
			if err != nil {
				return nil, huma.Error502BadGateway(err.Error())
			}
			defer resp.Body.Close()
			data, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
			if err != nil || resp.StatusCode != http.StatusOK || len(data) == 0 {
				return nil, huma.Error502BadGateway("could not download the subtitle")
			}
			ext := strings.ToLower(path.Ext(req.URL.Path))
			if ext != ".vtt" && ext != ".ass" && ext != ".ssa" && ext != ".sub" {
				ext = ".srt"
			}
			lang := strings.Trim(strings.Map(func(r rune) rune {
				if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' {
					return r
				}
				return -1
			}, in.Body.Lang), "-")
			f, err := s.fs(ctx)
			if err != nil {
				return nil, err
			}
			video := "/" + strings.TrimPrefix(in.Body.Video, "/")
			stem := strings.TrimSuffix(video, path.Ext(video))
			if lang != "" {
				stem += "." + lang
			}
			for i := 0; i < 50; i++ {
				p := stem + ext
				if i > 0 {
					p = stem + "." + strconv.Itoa(i+1) + ext
				}
				if err := f.WriteNew(p, data); err == nil {
					return body(map[string]string{"path": p}), nil
				}
			}
			return nil, huma.Error409Conflict("no free subtitle file name next to the video")
		})
}

// addonProxy fetches an add-on's poster or subtitle file for the app, so the app only ever talks to this server.
// Read token, or a signed link (POST /v1/addons/proxy/sign) for players that can't send one. Only images and
// subtitle/text files pass, at most 10 MiB, always through the SSRF guard.
func (s *server) addonProxy(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	target := q.Get("url")
	if _, _, ok := s.access(w, r, "addon-proxy/"+target); !ok {
		return
	}
	if err := s.needAddons(); err != nil {
		http.Error(w, "add-ons are switched off", http.StatusNotFound)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil || (req.URL.Scheme != "https" && req.URL.Scheme != "http") {
		http.Error(w, "invalid url", http.StatusBadRequest)
		return
	}
	resp, err := s.Addons.HTTP.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream answered "+resp.Status, http.StatusBadGateway)
		return
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if !(strings.HasPrefix(ct, "image/") || strings.HasPrefix(ct, "text/plain") || strings.HasPrefix(ct, "text/vtt") ||
		strings.Contains(ct, "subrip") || strings.HasPrefix(ct, "application/octet-stream") || ct == "") {
		http.Error(w, "only images and subtitle files pass through", http.StatusUnsupportedMediaType)
		return
	}
	h := w.Header()
	if ct == "" || strings.HasPrefix(ct, "application/octet-stream") {
		ct = "text/plain; charset=utf-8" // subtitle files are often served untyped
	}
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", "private, max-age=86400")
	h.Set("Content-Security-Policy", "sandbox")
	io.Copy(w, io.LimitReader(resp.Body, 10<<20))
}

func (s *server) addonProxyRoutes(api huma.API) {
	huma.Register(api, op("addons-proxy-sign", "POST", "/v1/addons/proxy/sign", "read",
		"A short-lived link to an add-on image or subtitle through this server (for players that can't send a token)"),
		func(ctx context.Context, in *struct {
			Body struct {
				URL string `json:"url" minLength:"8"`
			}
		}) (*struct{ Body map[string]string }, error) {
			if err := s.needAddons(); err != nil {
				return nil, err
			}
			f, err := s.fs(ctx)
			if err != nil {
				return nil, err
			}
			q, err := f.Sign("addon-proxy/"+in.Body.URL, 6*time.Hour)
			if err != nil {
				return nil, err
			}
			return body(map[string]string{"url": "/v1/addons/proxy?url=" + url.QueryEscape(in.Body.URL) + "&" + q}), nil
		})
}
