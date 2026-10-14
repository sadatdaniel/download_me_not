// Package webui serves the browser clients, embedded in the binary:
//   - the full GetALoad app (the Flutter web build of the getaload_app repo) at /, when the release build put it in
//     app/ (scripts/webapp.sh); its large files are stored gzip-compressed (*.gz) and served as such;
//   - the light panel (Preact + htm vendored, no build step) at /lite/, and at / in builds without the full app
//     (development and CI test builds).
//
// Both are plain clients of the /v1 API, like the Android app.
package webui

import (
	"compress/gzip"
	"embed"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

//go:embed static
var lite embed.FS

//go:embed all:app
var app embed.FS

// Handler serves the clients. version tags every response (ETag), so a browser downloads the app once per server
// version and afterwards only checks that nothing changed.
func Handler(version string) http.Handler {
	liteFS, _ := fs.Sub(lite, "static")
	appFS, _ := fs.Sub(app, "app")
	return handler(liteFS, appFS, version)
}

func handler(liteFS, appFS fs.FS, version string) http.Handler {
	_, err := fs.Stat(appFS, "index.html")
	hasApp := err == nil
	liteServer := http.FileServerFS(liteFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if r.URL.Path == "/lite" {
			http.Redirect(w, r, "/lite/", http.StatusMovedPermanently)
			return
		}
		rest, isLite := strings.CutPrefix(r.URL.Path, "/lite/")
		if isLite || !hasApp {
			if !isLite {
				rest = strings.TrimPrefix(r.URL.Path, "/")
			}
			// Strict CSP: only our own scripts and styles; media and API on the same origin.
			h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: https:; media-src 'self' blob:; "+
				"style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
			if strings.HasPrefix(rest, "vendor/") {
				h.Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				h.Set("Cache-Control", "no-cache") // index.html and app.js change with releases
			}
			if _, err := fs.Stat(liteFS, rest); rest == "" || err != nil {
				rest = "" // unknown paths get index.html (the panel routes with #hash)
			}
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/" + rest
			liteServer.ServeHTTP(w, r2)
			return
		}
		serveApp(w, r, appFS, version)
	})
}

// serveApp serves the Flutter web app: everything from our own origin (fonts too, as Flutter falls back to Google's
// font CDN for missing glyphs), WebAssembly allowed, unknown paths get index.html.
func serveApp(w http.ResponseWriter, r *http.Request, appFS fs.FS, version string) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; "+
		"style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; media-src 'self' blob:; "+
		"font-src 'self' data: https://fonts.gstatic.com; connect-src 'self' https://fonts.gstatic.com; "+
		"worker-src 'self' blob:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	gz := false
	if _, err := fs.Stat(appFS, name); err != nil {
		if _, err := fs.Stat(appFS, name+".gz"); err == nil {
			gz = true
		} else {
			name = "index.html" // the app's own routes
		}
	}
	if version == "dev" { // every dev build is "dev": a 304 would keep the previous build's app
		h.Set("Cache-Control", "no-store")
	} else {
		etag := `"` + version + ":" + name + `"`
		h.Set("ETag", etag)
		h.Set("Cache-Control", "no-cache") // revalidate each time: a 304 until the server is updated
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	ct := map[string]string{".wasm": "application/wasm", ".mjs": "text/javascript", ".js": "text/javascript"}[path.Ext(name)]
	if ct == "" {
		ct = mime.TypeByExtension(path.Ext(name)) // the system table may lack wasm and mjs, hence the map above
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	file := name
	if gz {
		file += ".gz"
	}
	f, err := appFS.Open(file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	var body io.Reader = f
	if gz {
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			h.Set("Content-Encoding", "gzip")
			h.Add("Vary", "Accept-Encoding")
		} else if zr, err := gzip.NewReader(f); err == nil { // a client without gzip (rare): unpack on the fly
			body = zr
		}
	}
	if r.Method == http.MethodHead {
		return
	}
	io.Copy(w, body)
}
