package api

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sadatdaniel/download_me_not/internal/config"
	"github.com/sadatdaniel/download_me_not/internal/cookies"
	"github.com/sadatdaniel/download_me_not/internal/fsapi"
	"github.com/sadatdaniel/download_me_not/internal/jobs"
	"github.com/sadatdaniel/download_me_not/internal/limits"
	"github.com/sadatdaniel/download_me_not/internal/provider"
	"github.com/sadatdaniel/download_me_not/internal/store"
)

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	dl := filepath.Join(dir, "dl")
	os.MkdirAll(dl, 0o755)
	os.WriteFile(filepath.Join(dl, "video.mp4"), []byte("0123456789"), 0o644)
	cfg := &config.Config{}
	cfg.Paths.Data = dir
	cfg.Auth.Tokens = []config.Token{
		{Name: "r", Hash: HashToken("read-tok"), Scope: "read"},
		{Name: "w", Hash: HashToken("write-tok"), Scope: "write"},
		{Name: "a", Hash: HashToken("admin-tok"), Scope: "admin"},
	}
	db, err := store.Open(filepath.Join(dir, "dmn.db"))
	if err != nil {
		t.Fatal(err)
	}
	f, _ := fsapi.New(dl, []byte("key"))
	cfgPath := filepath.Join(dir, "config.yaml")
	reg := provider.NewRegistry(nil, dir)
	ck, _ := cookies.Open(filepath.Join(dir, "cookies"), filepath.Join(dir, "cookie.key"))
	jm := jobs.New(jobs.Options{DB: db, Downloads: dl, Client: http.DefaultClient, Allowed: reg.Enabled})
	h, _ := New(Deps{Version: "test", Cfg: cfg, CfgPath: cfgPath, DB: db, FS: f, Providers: reg, Jobs: jm, Limits: limits.New(0, 0), Cookies: ck})
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.Close(); db.Close(); f.Close() })
	return srv, cfgPath
}

func call(t *testing.T, srv *httptest.Server, method, path, tok, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestAuthAndScopes(t *testing.T) {
	srv, cfgPath := newTestServer(t)
	cases := []struct {
		method, path, tok, body string
		want                    int
	}{
		{"GET", "/v1/health", "", "", 200},
		{"GET", "/openapi.json", "", "", 200},
		{"GET", "/v1/jobs", "", "", 401},
		{"GET", "/v1/jobs", "wrong", "", 401},
		{"GET", "/v1/jobs", "read-tok", "", 200},
		{"GET", "/v1/fs?path=/", "read-tok", "", 200},
		{"POST", "/v1/fs/mkdir", "read-tok", `{"path":"/x"}`, 403},
		{"POST", "/v1/fs/mkdir", "write-tok", `{"path":"/x"}`, 204},
		{"POST", "/v1/fs/move", "write-tok", `{"from":"/x","to":"/y"}`, 204},
		{"POST", "/v1/fs/move", "write-tok", `{"from":"/y","to":"/video.mp4"}`, 409},
		{"GET", "/v1/fs/stat?path=/../etc/passwd", "read-tok", "", 404},
		{"DELETE", "/v1/fs?path=/y", "write-tok", "", 204},
		{"DELETE", "/v1/fs?path=/", "write-tok", "", 400},
		{"POST", "/v1/jobs", "read-tok", `{"source":"https://example.com/x"}`, 403},
		{"POST", "/v1/jobs", "write-tok", `{"source":"https://example.com/x"}`, 201},
		{"POST", "/v1/jobs", "write-tok", `{"source":"ftp://example.com/x"}`, 400},
		{"PUT", "/v1/providers/web/enabled", "write-tok", `{"enabled":false}`, 403},
		{"PUT", "/v1/providers/web/enabled", "admin-tok", `{"enabled":false}`, 204},
		{"PUT", "/v1/providers/local/enabled", "admin-tok", `{"enabled":false}`, 400},
		{"POST", "/v1/jobs", "write-tok", `{"source":"https://example.com/y"}`, 400}, // web provider now off
		{"GET", "/v1/jobs/nope", "read-tok", "", 404},
		{"GET", "/v1/jobs/nope/stream", "read-tok", "", 404},
		{"GET", "/v1/jobs/nope/peers", "read-tok", "", 404},
		{"PUT", "/v1/jobs/nope/trackers", "write-tok", `[["file:///etc"]]`, 422},
		{"GET", "/v1/raw/video.mp4", "", "", 401},
		{"POST", "/v1/cookies", "write-tok", `{"domain":"example.com","content":"x"}`, 403},
		{"POST", "/v1/cookies", "admin-tok", `{"domain":"example.com","content":".example.com\tTRUE\t/\tTRUE\t0\tsid\tSECRETVALUE"}`, 200},
		{"DELETE", "/v1/cookies/nope.org", "admin-tok", "", 404},
		{"PUT", "/v1/limits", "read-tok", `{"down":1000,"up":0}`, 403},
		{"PUT", "/v1/limits", "write-tok", `{"down":1000,"up":0}`, 204},
		{"PUT", "/v1/limits", "write-tok", `{"down":-1,"up":0}`, 422},
		{"GET", "/v1/limits", "read-tok", "", 200},
		{"POST", "/v1/fs/copy", "write-tok", `{"from":"/video.mp4","to":"/copy.mp4"}`, 204},
		{"GET", "/v1/zip/", "read-tok", "", 200},
		{"GET", "/v1/zip/", "", "", 401},
		{"GET", "/v1/zip/video.mp4", "read-tok", "", 404},
		{"GET", "/v1/raw/video.mp4", "read-tok", "", 200},
	}
	for _, c := range cases {
		if got, b := call(t, srv, c.method, c.path, c.tok, c.body); got != c.want {
			t.Errorf("%s %s (%s): got %d want %d: %.120s", c.method, c.path, c.tok, got, c.want, b)
		}
	}
	// Provider toggle persisted to the config file, so it survives a restart.
	_, cfg, _ := config.Load(cfgPath, nil)
	if p := cfg.Providers["web"]; p.Enabled == nil || *p.Enabled {
		t.Errorf("toggle not persisted: %+v", p)
	}
}

func TestSignedStream(t *testing.T) {
	srv, _ := newTestServer(t)
	code, b := call(t, srv, "POST", "/v1/fs/sign", "read-tok", `{"path":"/video.mp4","ttlSeconds":60}`)
	if code != 200 {
		t.Fatalf("sign: %d %s", code, b)
	}
	u := b[strings.Index(b, "/v1/raw/"):strings.LastIndex(b, `"`)]
	req, _ := http.NewRequest("GET", srv.URL+u, nil) // no token: the player only has the link
	req.Header.Set("Range", "bytes=2-4")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 206 || string(got) != "234" {
		t.Fatalf("signed range: %d %q", resp.StatusCode, got)
	}
	if code, _ := call(t, srv, "GET", strings.Replace(u, "video.mp4", "other.mp4", 1), "", ""); code != 403 {
		t.Fatalf("signature reused for another path: %d", code)
	}
}

func TestEvents(t *testing.T) {
	srv, _ := newTestServer(t)
	req, _ := http.NewRequest("GET", srv.URL+"/v1/events", nil)
	req.Header.Set("Authorization", "Bearer read-tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data:") {
			return // first event arrived
		}
	}
	t.Fatal("no SSE event")
}

// The committed api/openapi.json is the contract with the Android app. Any change fails this
// test until the snapshot is regenerated on purpose: UPDATE_OPENAPI=1 go test ./internal/api/
// Within /v1 only additive changes are allowed (docs/architecture.md).
func TestOpenAPISnapshot(t *testing.T) {
	srv, _ := newTestServer(t)
	_, got := call(t, srv, "GET", "/openapi.json", "", "")
	path := filepath.Join("..", "..", "api", "openapi.json")
	if os.Getenv("UPDATE_OPENAPI") == "1" {
		os.WriteFile(path, []byte(got), 0o644)
	}
	want, err := os.ReadFile(path)
	if err != nil || string(want) != got {
		t.Fatal("API changed: review it, then run UPDATE_OPENAPI=1 go test ./internal/api/ (breaking /v1 changes need /v2)")
	}
}

func TestCookieValuesNeverReturned(t *testing.T) {
	srv, _ := newTestServer(t)
	call(t, srv, "POST", "/v1/cookies", "admin-tok", `{"domain":"example.com","content":".example.com\tTRUE\t/\tTRUE\t0\tsid\tSECRETVALUE"}`)
	code, b := call(t, srv, "GET", "/v1/cookies", "read-tok", "")
	if code != 200 || !strings.Contains(b, "example.com") || strings.Contains(b, "SECRETVALUE") {
		t.Fatalf("%d %s", code, b)
	}
}

func TestTokenAddedWhileRunning(t *testing.T) {
	srv, cfgPath := newTestServer(t)
	if code, _ := call(t, srv, "GET", "/v1/jobs", "new-tok", ""); code != 401 {
		t.Fatalf("unknown token: %d", code)
	}
	time.Sleep(1100 * time.Millisecond) // reload is rate-limited to once a second
	config.Set(cfgPath, "auth.tokens", []map[string]any{{"name": "phone", "hash": HashToken("new-tok"), "scope": "read"}})
	if code, _ := call(t, srv, "GET", "/v1/jobs", "new-tok", ""); code != 200 {
		t.Fatalf("token added by pair not picked up: %d", code)
	}
}
