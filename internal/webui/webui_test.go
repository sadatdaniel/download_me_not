package webui

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func gz(s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}

func get(h http.Handler, path string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestFullAppAndLite(t *testing.T) {
	lite := fstest.MapFS{"index.html": {Data: []byte("lite panel")}, "app.js": {Data: []byte("js")}}
	app := fstest.MapFS{"index.html": {Data: []byte("full app")}, "main.dart.wasm.gz": {Data: gz("WASM")}}
	h := handler(lite, app, "0.2.1")

	if w := get(h, "/", nil); w.Body.String() != "full app" {
		t.Fatalf("/ should be the full app: %q", w.Body.String())
	}
	if w := get(h, "/some/route", nil); w.Body.String() != "full app" {
		t.Fatalf("app routes get index.html: %q", w.Body.String())
	}
	w := get(h, "/main.dart.wasm", map[string]string{"Accept-Encoding": "gzip, br"})
	if w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("Content-Type") != "application/wasm" {
		t.Fatalf("compressed wasm: %v", w.Header())
	}
	if w := get(h, "/main.dart.wasm", nil); w.Body.String() != "WASM" {
		t.Fatalf("a client without gzip gets it unpacked: %q", w.Body.String())
	}
	etag := w.Header().Get("ETag")
	if w := get(h, "/main.dart.wasm", map[string]string{"If-None-Match": etag}); w.Code != http.StatusNotModified {
		t.Fatalf("same version must be a 304, got %d", w.Code)
	}
	if w := get(handler(lite, app, "0.2.2"), "/main.dart.wasm", map[string]string{"If-None-Match": etag}); w.Code != 200 {
		t.Fatalf("a new server version must send the file again, got %d", w.Code)
	}
	if w := get(handler(lite, app, "dev"), "/main.dart.wasm", map[string]string{"If-None-Match": `"dev:main.dart.wasm"`}); w.Code != 200 || w.Header().Get("ETag") != "" {
		t.Fatalf("dev builds are never cached (each one is \"dev\"), got %d %v", w.Code, w.Header())
	}
	if w := get(h, "/lite/", nil); w.Body.String() != "lite panel" {
		t.Fatalf("/lite/ is the light panel: %q", w.Body.String())
	}
	if w := get(h, "/lite/app.js", nil); w.Body.String() != "js" {
		t.Fatalf("light panel files: %q", w.Body.String())
	}
	// A build without the full app (development, CI tests) serves the light panel at /.
	if w := get(handler(lite, fstest.MapFS{"README.md": {}}, "dev"), "/", nil); w.Body.String() != "lite panel" {
		t.Fatalf("without the app / is the light panel: %q", w.Body.String())
	}
	_ = io.Discard
}
