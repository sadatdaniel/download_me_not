// Package addons is a client for Stremio add-ons (protocol:
// https://github.com/Stremio/stremio-addon-sdk/blob/master/docs/protocol.md). An add-on is an HTTPS endpoint with
// /manifest.json at its base and GET routes /{resource}/{type}/{id}[/{extra}].json for catalog, meta, stream and
// subtitles. The server fetches add-ons (never the app), always through the SSRF-guarded client, and caches answers
// as long as the add-on allows (cacheMaxAge) or a short default.
package addons

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Manifest describes an add-on.
type Manifest struct {
	ID            string     `json:"id"`
	Version       string     `json:"version"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	Logo          string     `json:"logo,omitempty"`
	Background    string     `json:"background,omitempty"`
	Types         []string   `json:"types"`
	IDPrefixes    []string   `json:"idPrefixes,omitempty"`
	Resources     []Resource `json:"resources"`
	Catalogs      []Catalog  `json:"catalogs"`
	BehaviorHints struct {
		Adult                 bool `json:"adult,omitempty"`
		P2P                   bool `json:"p2p,omitempty"`
		Configurable          bool `json:"configurable,omitempty"`
		ConfigurationRequired bool `json:"configurationRequired,omitempty"`
	} `json:"behaviorHints"`
}

// Resource is "catalog"/"meta"/"stream"/"subtitles", optionally limited to types and id prefixes.
type Resource struct {
	Name       string   `json:"name"`
	Types      []string `json:"types,omitempty"`
	IDPrefixes []string `json:"idPrefixes,omitempty"`
	object     bool
}

// UnmarshalJSON accepts both forms the protocol allows: "stream" or {"name": "stream", "types": [...]}.
func (r *Resource) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &r.Name)
	}
	type plain Resource
	if err := json.Unmarshal(b, (*plain)(r)); err != nil {
		return err
	}
	r.object = true
	return nil
}

type Catalog struct {
	Type  string  `json:"type"`
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Extra []Extra `json:"extra,omitempty"`
	// Older add-ons describe extras with these two lists instead.
	ExtraSupported []string `json:"extraSupported,omitempty"`
	ExtraRequired  []string `json:"extraRequired,omitempty"`
}

type Extra struct {
	Name       string   `json:"name"`
	IsRequired bool     `json:"isRequired,omitempty"`
	Options    []string `json:"options,omitempty"`
}

// Supports reports whether the catalog accepts an extra (e.g. "search"); Required whether it insists on one.
func (c Catalog) Supports(name string) bool {
	return slices.ContainsFunc(c.Extra, func(e Extra) bool { return e.Name == name }) || slices.Contains(c.ExtraSupported, name)
}

func (c Catalog) Required() []string {
	out := slices.Clone(c.ExtraRequired)
	for _, e := range c.Extra {
		if e.IsRequired {
			out = append(out, e.Name)
		}
	}
	return out
}

// Meta is a title: a catalog entry (preview) or the full details with its videos (episodes).
type Meta struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Name        string   `json:"name"`
	Poster      string   `json:"poster,omitempty"`
	PosterShape string   `json:"posterShape,omitempty"`
	Background  string   `json:"background,omitempty"`
	Logo        string   `json:"logo,omitempty"`
	Description string   `json:"description,omitempty"`
	ReleaseInfo any      `json:"releaseInfo,omitempty"` // string or number in the wild
	IMDBRating  any      `json:"imdbRating,omitempty"`
	Runtime     string   `json:"runtime,omitempty"`
	Genres      []string `json:"genres,omitempty"`
	Videos      []Video  `json:"videos,omitempty"`
}

type Video struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	Name      string `json:"name,omitempty"`
	Released  string `json:"released,omitempty"`
	Thumbnail string `json:"thumbnail,omitempty"`
	Season    int    `json:"season,omitempty"`
	Episode   int    `json:"episode,omitempty"`
	Overview  string `json:"overview,omitempty"`
}

// Stream is one way to watch a title: a direct url, a torrent (infoHash + fileIdx + sources), or an external link.
type Stream struct {
	Name          string     `json:"name,omitempty"`
	Title         string     `json:"title,omitempty"`
	Description   string     `json:"description,omitempty"`
	URL           string     `json:"url,omitempty"`
	YtID          string     `json:"ytId,omitempty"`
	InfoHash      string     `json:"infoHash,omitempty"`
	FileIdx       *int       `json:"fileIdx,omitempty"`
	ExternalURL   string     `json:"externalUrl,omitempty"`
	Sources       []string   `json:"sources,omitempty"`
	Subtitles     []Subtitle `json:"subtitles,omitempty"`
	BehaviorHints struct {
		NotWebReady  bool   `json:"notWebReady,omitempty"`
		BingeGroup   string `json:"bingeGroup,omitempty"`
		Filename     string `json:"filename,omitempty"`
		VideoSize    int64  `json:"videoSize,omitempty"`
		VideoHash    string `json:"videoHash,omitempty"`
		ProxyHeaders *struct {
			Request map[string]string `json:"request,omitempty"`
		} `json:"proxyHeaders,omitempty"`
	} `json:"behaviorHints,omitempty"`
	Addon string `json:"addon,omitempty" doc:"name of the add-on that offered it (added by the server)"`
}

type Subtitle struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Lang  string `json:"lang"`
	Label string `json:"label,omitempty"`
	Addon string `json:"addon,omitempty"`
}

// Addon is an installed add-on: its base URL (without /manifest.json) and manifest.
type Addon struct {
	URL      string   `json:"url"`
	Manifest Manifest `json:"manifest"`
}

// Handles reports whether the add-on answers resource for this type and id (types and idPrefixes from the
// resource object, or the manifest's when the resource is a plain name). idPrefixes never limit catalogs.
func (a Addon) Handles(resource, typ, id string) bool {
	for _, r := range a.Manifest.Resources {
		if r.Name != resource {
			continue
		}
		types, prefixes := a.Manifest.Types, a.Manifest.IDPrefixes
		if r.object {
			if len(r.Types) > 0 {
				types = r.Types
			}
			prefixes = r.IDPrefixes
		}
		if len(types) > 0 && !slices.Contains(types, typ) {
			return false
		}
		if resource == "catalog" || len(prefixes) == 0 {
			return true
		}
		return slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(id, p) })
	}
	return false
}

// Base turns what a user pastes (…/manifest.json, stremio://…, a bare base URL) into the add-on's base URL.
func Base(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if rest, ok := strings.CutPrefix(s, "stremio://"); ok {
		s = "https://" + rest
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/manifest.json"), "/")
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", errors.New("an add-on address looks like https://example.com/manifest.json")
	}
	return s, nil
}

// Client fetches add-on routes with a cache. HTTP must be the SSRF-guarded client (netguard) in the server.
type Client struct {
	HTTP *http.Client

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	body []byte
	exp  time.Time
}

const (
	maxBody      = 8 << 20
	defaultCache = 10 * time.Minute
)

// get fetches base/path.json (path already escaped) into out, honouring the add-on's cacheMaxAge.
func (c *Client) get(ctx context.Context, u string, out any) error {
	c.mu.Lock()
	e, ok := c.cache[u]
	c.mu.Unlock()
	if ok && time.Now().Before(e.exp) {
		return json.Unmarshal(e.body, out)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("add-on answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxBody {
		return errors.New("add-on answer is too large")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("add-on answer is not valid JSON: %w", err)
	}
	var hint struct {
		CacheMaxAge int `json:"cacheMaxAge"`
	}
	json.Unmarshal(body, &hint)
	ttl := defaultCache
	if hint.CacheMaxAge > 0 {
		ttl = min(time.Duration(hint.CacheMaxAge)*time.Second, 24*time.Hour)
	}
	c.mu.Lock()
	if c.cache == nil || len(c.cache) > 2000 { // ponytail: crude bound; an LRU if add-on traffic grows
		c.cache = map[string]cached{}
	}
	c.cache[u] = cached{body: body, exp: time.Now().Add(ttl)}
	c.mu.Unlock()
	return nil
}

// route builds base/resource/type/id[/extra].json; extra values are query-encoded as the protocol requires.
func route(base, resource, typ, id string, extra [][2]string) string {
	u := base + "/" + resource + "/" + url.PathEscape(typ) + "/" + url.PathEscape(id)
	if len(extra) > 0 {
		parts := make([]string, 0, len(extra))
		for _, kv := range extra {
			if kv[1] != "" {
				parts = append(parts, kv[0]+"="+url.QueryEscape(kv[1]))
			}
		}
		if len(parts) > 0 {
			u += "/" + strings.Join(parts, "&")
		}
	}
	return u + ".json"
}

func (c *Client) Manifest(ctx context.Context, base string) (Manifest, error) {
	var m Manifest
	if err := c.get(ctx, base+"/manifest.json", &m); err != nil {
		return m, err
	}
	if m.ID == "" || m.Name == "" || len(m.Resources) == 0 {
		return m, errors.New("this is not a Stremio add-on manifest (id, name and resources are required)")
	}
	return m, nil
}

func (c *Client) Catalog(ctx context.Context, a Addon, typ, id string, extra [][2]string) ([]Meta, error) {
	var r struct {
		Metas []Meta `json:"metas"`
	}
	err := c.get(ctx, route(a.URL, "catalog", typ, id, extra), &r)
	return r.Metas, err
}

func (c *Client) Meta(ctx context.Context, a Addon, typ, id string) (Meta, error) {
	var r struct {
		Meta Meta `json:"meta"`
	}
	err := c.get(ctx, route(a.URL, "meta", typ, id, nil), &r)
	return r.Meta, err
}

func (c *Client) Streams(ctx context.Context, a Addon, typ, id string) ([]Stream, error) {
	var r struct {
		Streams []Stream `json:"streams"`
	}
	err := c.get(ctx, route(a.URL, "stream", typ, id, nil), &r)
	for i := range r.Streams {
		r.Streams[i].Addon = a.Manifest.Name
	}
	return r.Streams, err
}

func (c *Client) Subtitles(ctx context.Context, a Addon, typ, id string, extra [][2]string) ([]Subtitle, error) {
	var r struct {
		Subtitles []Subtitle `json:"subtitles"`
	}
	err := c.get(ctx, route(a.URL, "subtitles", typ, id, extra), &r)
	for i := range r.Subtitles {
		r.Subtitles[i].Addon = a.Manifest.Name
	}
	return r.Subtitles, err
}

// Magnet turns a torrent stream into a magnet link: the info hash, a name, and the trackers from "sources"
// ("tracker:udp://…"; "dht:" entries are left to the engine's DHT setting).
func Magnet(s Stream, name string) (string, error) {
	h := strings.ToLower(s.InfoHash)
	if len(h) != 40 || strings.Trim(h, "0123456789abcdef") != "" {
		return "", errors.New("not a torrent stream (infoHash must be 40 hex characters)")
	}
	q := url.Values{}
	if name != "" {
		q.Set("dn", name)
	}
	for _, src := range s.Sources {
		if tr, ok := strings.CutPrefix(src, "tracker:"); ok {
			q.Add("tr", tr)
		}
	}
	m := "magnet:?xt=urn:btih:" + h
	if e := q.Encode(); e != "" {
		m += "&" + e
	}
	return m, nil
}
