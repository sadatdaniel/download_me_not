package addons

// Offline only (AGENTS.md rule 7): a local test add-on with our own made-up titles and a torrent hash of our own.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

const manifestJSON = `{
  "id": "org.test.addon", "version": "1.0.0", "name": "Test add-on", "description": "Local test",
  "types": ["movie", "series"], "idPrefixes": ["tt"],
  "resources": ["catalog", {"name": "stream", "types": ["movie"], "idPrefixes": ["tt", "test:"]}, "meta", "subtitles"],
  "catalogs": [{"type": "movie", "id": "top", "name": "Top", "extra": [{"name": "search"}, {"name": "skip"}]}],
  "behaviorHints": {"p2p": true}
}`

func testAddon(t *testing.T) (string, *atomic.Int64, *[]string) {
	var hits atomic.Int64
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		paths = append(paths, r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/manifest.json":
			w.Write([]byte(manifestJSON))
		case strings.HasPrefix(r.URL.Path, "/catalog/movie/top"):
			json.NewEncoder(w).Encode(map[string]any{"metas": []Meta{{ID: "tt0000001", Type: "movie", Name: "Test Pattern"}}, "cacheMaxAge": 60})
		case r.URL.Path == "/meta/movie/tt0000001.json":
			w.Write([]byte(`{"meta": {"id": "tt0000001", "type": "movie", "name": "Test Pattern", "releaseInfo": 2026, "imdbRating": "7.1"}}`))
		case r.URL.Path == "/stream/movie/tt0000001.json":
			w.Write([]byte(`{"streams": [
			  {"name": "1080p", "infoHash": "08ADA5A7A6183AAE1E09D831DF6748D566095A10", "fileIdx": 2, "sources": ["tracker:udp://tracker.test:6969/announce", "dht:08ada5a7a6183aae1e09d831df6748d566095a10"]},
			  {"name": "Direct", "url": "https://media.test/v.mp4", "behaviorHints": {"notWebReady": true, "proxyHeaders": {"request": {"User-Agent": "x"}}}}
			]}`))
		case strings.HasPrefix(r.URL.Path, "/subtitles/movie/tt0000001"):
			w.Write([]byte(`{"subtitles": [{"id": "1", "url": "https://subs.test/a.srt", "lang": "eng"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &hits, &paths
}

func TestAddonClient(t *testing.T) {
	base, hits, paths := testAddon(t)
	c := &Client{HTTP: http.DefaultClient}
	ctx := context.Background()

	for _, in := range []string{base + "/manifest.json", base + "/", base} {
		if b, err := Base(in); err != nil || b != base {
			t.Fatalf("Base(%q) = %q, %v", in, b, err)
		}
	}
	if b, _ := Base("stremio://addon.test/x/manifest.json"); b != "https://addon.test/x" {
		t.Fatalf("stremio:// link: %q", b)
	}
	if _, err := Base("ftp://x/manifest.json"); err == nil {
		t.Fatal("ftp accepted")
	}

	m, err := c.Manifest(ctx, base)
	if err != nil || m.Name != "Test add-on" || !m.BehaviorHints.P2P || len(m.Resources) != 4 {
		t.Fatalf("manifest: %+v %v", m, err)
	}
	a := Addon{URL: base, Manifest: m}
	// Plain "meta" uses the manifest's types and idPrefixes; the stream object has its own.
	checks := []struct {
		res, typ, id string
		want         bool
	}{
		{"meta", "movie", "tt1", true}, {"meta", "movie", "kitsu:1", false}, {"meta", "channel", "tt1", false},
		{"stream", "movie", "test:9", true}, {"stream", "series", "tt1", false}, {"catalog", "movie", "anything", true},
		{"addon_catalog", "movie", "tt1", false},
	}
	for _, ck := range checks {
		if got := a.Handles(ck.res, ck.typ, ck.id); got != ck.want {
			t.Errorf("Handles(%s, %s, %s) = %v", ck.res, ck.typ, ck.id, got)
		}
	}

	metas, err := c.Catalog(ctx, a, "movie", "top", [][2]string{{"search", "test pattern"}, {"skip", ""}})
	if err != nil || len(metas) != 1 {
		t.Fatalf("catalog: %v %v", metas, err)
	}
	if last := (*paths)[len(*paths)-1]; last != "/catalog/movie/top/search=test+pattern.json" {
		t.Fatalf("extra encoding: %s", last)
	}
	before := hits.Load()
	c.Catalog(ctx, a, "movie", "top", [][2]string{{"search", "test pattern"}})
	if hits.Load() != before {
		t.Fatal("cached catalog fetched again")
	}

	meta, err := c.Meta(ctx, a, "movie", "tt0000001")
	if err != nil || meta.Name != "Test Pattern" {
		t.Fatalf("meta: %+v %v", meta, err)
	}
	streams, err := c.Streams(ctx, a, "movie", "tt0000001")
	if err != nil || len(streams) != 2 || streams[0].Addon != "Test add-on" || *streams[0].FileIdx != 2 ||
		streams[1].BehaviorHints.ProxyHeaders.Request["User-Agent"] != "x" {
		t.Fatalf("streams: %+v %v", streams, err)
	}
	mag, err := Magnet(streams[0], "Test Pattern")
	if err != nil || !strings.HasPrefix(mag, "magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10&") ||
		!strings.Contains(mag, "tr=udp%3A%2F%2Ftracker.test%3A6969%2Fannounce") || strings.Contains(mag, "dht") {
		t.Fatalf("magnet: %s %v", mag, err)
	}
	if _, err := Magnet(streams[1], ""); err == nil {
		t.Fatal("url stream turned into a magnet")
	}
	subs, err := c.Subtitles(ctx, a, "movie", "tt0000001", [][2]string{{"videoSize", "123"}, {"filename", "a b.mkv"}})
	if err != nil || len(subs) != 1 || subs[0].Lang != "eng" {
		t.Fatalf("subtitles: %+v %v", subs, err)
	}
	if last := (*paths)[len(*paths)-1]; last != "/subtitles/movie/tt0000001/videoSize=123&filename=a+b.mkv.json" {
		t.Fatalf("subtitle extra encoding: %s", last)
	}

	if _, err := c.Manifest(ctx, base+"/nothing"); err == nil {
		t.Fatal("missing manifest accepted")
	}
}

// Opt-in check that real manifests and catalogs parse (GETALOAD_LIVE_ADDONS=1, needs internet): Stremio's official
// metadata add-on (Cinemeta) and the official OpenSubtitles add-on. JSON only: no streams, no media (rule 7).
func TestLiveAddons(t *testing.T) {
	if os.Getenv("GETALOAD_LIVE_ADDONS") != "1" {
		t.Skip("set GETALOAD_LIVE_ADDONS=1 to check real add-ons")
	}
	c := &Client{HTTP: http.DefaultClient}
	ctx := context.Background()
	for _, base := range []string{"https://v3-cinemeta.strem.io", "https://opensubtitles-v3.strem.io"} {
		m, err := c.Manifest(ctx, base)
		if err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		t.Logf("%s: %s %s, resources %d, catalogs %d", base, m.Name, m.Version, len(m.Resources), len(m.Catalogs))
		a := Addon{URL: base, Manifest: m}
		for _, cat := range m.Catalogs {
			if len(cat.Required()) == 0 {
				metas, err := c.Catalog(ctx, a, cat.Type, cat.ID, nil)
				if err != nil || len(metas) == 0 {
					t.Fatalf("%s catalog %s: %d %v", base, cat.ID, len(metas), err)
				}
				t.Logf("  catalog %s/%s: %d titles, first %q", cat.Type, cat.ID, len(metas), metas[0].Name)
				meta, err := c.Meta(ctx, a, metas[0].Type, metas[0].ID)
				t.Logf("  meta: %q %v", meta.Name, err)
				break
			}
		}
		if a.Handles("subtitles", "movie", "tt1254207") {
			subs, err := c.Subtitles(ctx, a, "movie", "tt1254207", nil) // Big Buck Bunny (CC BY)
			t.Logf("  subtitles for Big Buck Bunny: %d %v", len(subs), err)
		}
	}
}
