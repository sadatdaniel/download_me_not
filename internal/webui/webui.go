// Package webui is the browser control panel (seedr.cc-style), embedded in the binary. It is a
// plain client of the /v1 API like the Android app: static files, no server-side rendering, no
// build step (Preact + htm vendored as ES modules).
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var files embed.FS

// Handler serves the UI. Unknown paths get index.html (the app routes with #hash anyway).
func Handler() http.Handler {
	sub, _ := fs.Sub(files, "static")
	fileServer := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// Strict CSP: only our own scripts and styles; media and API on the same origin.
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: https:; media-src 'self' blob:; "+
			"style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/vendor/") {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache") // index.html and app.js change with releases
		}
		if r.URL.Path != "/" {
			if _, err := fs.Stat(sub, strings.TrimPrefix(r.URL.Path, "/")); err != nil {
				r.URL.Path = "/"
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}
