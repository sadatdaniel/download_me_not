package api

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/sadatdaniel/getaload/internal/addons"
	"github.com/sadatdaniel/getaload/internal/config"
	"github.com/sadatdaniel/getaload/internal/cookies"
	"github.com/sadatdaniel/getaload/internal/fsapi"
	"github.com/sadatdaniel/getaload/internal/jobs"
	"github.com/sadatdaniel/getaload/internal/limits"
	"github.com/sadatdaniel/getaload/internal/provider"
	"github.com/sadatdaniel/getaload/internal/secret"
	"github.com/sadatdaniel/getaload/internal/store"
)

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv, cfgPath, _ := newTestServerJobs(t)
	return srv, cfgPath
}

func newTestServerJobs(t *testing.T) (*httptest.Server, string, *jobs.Manager) {
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
	db, err := store.Open(filepath.Join(dir, "getaload.db"))
	if err != nil {
		t.Fatal(err)
	}
	f, _ := fsapi.New(dl, []byte("key"))
	cfgPath := filepath.Join(dir, "config.yaml")
	config.Set(cfgPath, "auth.tokens", []map[string]any{ // as on a real install: tokens live in the file
		{"name": "r", "hash": HashToken("read-tok"), "scope": "read"},
		{"name": "w", "hash": HashToken("write-tok"), "scope": "write"},
		{"name": "a", "hash": HashToken("admin-tok"), "scope": "admin"},
	})
	reg := provider.NewRegistry(nil, dir)
	ck, _ := cookies.Open(filepath.Join(dir, "cookies"), filepath.Join(dir, "cookie.key"))
	box, _ := secret.Open(filepath.Join(dir, "secret.key"))
	jm := jobs.New(jobs.Options{DB: db, Downloads: dl, Client: http.DefaultClient, Allowed: reg.Enabled})
	h, _ := New(Deps{Version: "test", Cfg: cfg, CfgPath: cfgPath, DB: db, FS: f, Providers: reg, Jobs: jm, Limits: limits.New(0, 0), Cookies: ck, Secret: box, GoogleEndpoint: testGoogle,
		Addons: &addons.Client{HTTP: http.DefaultClient}}) // tests use a local test add-on (loopback)
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.Close(); db.Close(); f.Close() })
	return srv, cfgPath, jm
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
		{"GET", "/", "", "", 200},
		{"GET", "/app.js", "", "", 200},
		{"GET", "/v1/nope", "read-tok", "", 404},
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
		{"POST", "/v1/cookies", "read-tok", `{"domain":"example.com","content":"x"}`, 403},
		{"POST", "/v1/cookies", "admin-tok", `{"domain":"example.com","content":".example.com\tTRUE\t/\tTRUE\t0\tsid\tSECRETVALUE"}`, 200},
		{"DELETE", "/v1/cookies/nope.org", "admin-tok", "", 404},
		{"PUT", "/v1/limits", "read-tok", `{"down":1000,"up":0}`, 403},
		{"PUT", "/v1/limits", "write-tok", `{"down":1000,"up":0}`, 403}, // server-wide: the owner only
		{"PUT", "/v1/limits", "admin-tok", `{"down":1000,"up":0}`, 204},
		{"PUT", "/v1/limits", "admin-tok", `{"down":-1,"up":0}`, 422},
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
	// Files share the web app's origin: a downloaded page must never run scripts there.
	if csp := resp.Header.Get("Content-Security-Policy"); csp != "sandbox" {
		t.Fatalf("raw files need CSP sandbox, got %q", csp)
	}
	resp, err = http.Get(srv.URL + u + "&dl=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("dl=1 must download: %q", cd)
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

// testGoogle points Drive sign-in at a fake OAuth server (set by TestDriveConnect).
var testGoogle *oauth2.Endpoint

func TestDriveConnect(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/device":
			w.Write([]byte(`{"device_code":"d","user_code":"WXYZ-1234","verification_url":"https://www.google.com/device","expires_in":60,"interval":1}`))
		case r.Form.Get("client_secret") == "the-secret":
			w.Write([]byte(`{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":3600}`))
		default:
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"invalid_client"}`))
		}
	}))
	defer fake.Close()
	testGoogle = &oauth2.Endpoint{DeviceAuthURL: fake.URL + "/device", TokenURL: fake.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}
	defer func() { testGoogle = nil }()
	srv, cfgPath := newTestServer(t)

	if code, b := call(t, srv, "PUT", "/v1/providers/gdrive/settings", "admin-tok", `{"clientId":"cid","clientSecret":"the-secret"}`); code != 200 || strings.Contains(b, "the-secret") {
		t.Fatalf("set: %d %s", code, b)
	}
	if code, _ := call(t, srv, "PUT", "/v1/providers/gdrive/settings", "admin-tok", `{"token":"forged"}`); code != 422 {
		t.Fatal("token settable by hand")
	}
	raw, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(raw), "the-secret") || !strings.Contains(string(raw), "enc:v1:") {
		t.Fatalf("secret not encrypted in config:\n%s", raw)
	}
	code, b := call(t, srv, "POST", "/v1/providers/gdrive/signin", "admin-tok", "")
	if code != 202 || !strings.Contains(b, "WXYZ-1234") {
		t.Fatalf("signin: %d %s", code, b)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		_, st := call(t, srv, "GET", "/v1/providers/gdrive/signin", "admin-tok", "")
		if strings.Contains(st, `"done"`) {
			break
		}
		if time.Now().After(deadline) || strings.Contains(st, `"error"`) {
			t.Fatalf("sign-in: %s", st)
		}
		time.Sleep(200 * time.Millisecond)
	}
	_, v := call(t, srv, "GET", "/v1/providers/gdrive/settings", "admin-tok", "")
	if !strings.Contains(v, `"token":true`) || strings.Contains(v, "rt") {
		t.Fatalf("settings after sign-in: %s", v)
	}

	// With users, everyone connects their own Drive: Alice's sign-in is hers only.
	call(t, srv, "POST", "/v1/users", "admin-tok", `{"name":"Alice","pin":"1111"}`)
	a, _ := login(t, srv, "Alice", "1111", "phone")
	call(t, srv, "POST", "/v1/users", a, `{"name":"Bob","pin":"2222"}`)
	b2, _ := login(t, srv, "Bob", "2222", "phone")
	if _, d := call(t, srv, "GET", "/v1/me/drive", a, ""); !strings.Contains(d, `"connected":false`) {
		t.Fatalf("Alice starts unconnected (the old server-wide token is not hers): %s", d)
	}
	if code, b := call(t, srv, "POST", "/v1/providers/gdrive/signin", a, ""); code != 202 {
		t.Fatalf("Alice signs in: %d %s", code, b)
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if _, st := call(t, srv, "GET", "/v1/providers/gdrive/signin", a, ""); strings.Contains(st, `"done"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Alice's sign-in did not finish")
		}
	}
	if _, d := call(t, srv, "GET", "/v1/me/drive", a, ""); !strings.Contains(d, `"connected":true`) {
		t.Fatalf("Alice's Drive: %s", d)
	}
	if _, d := call(t, srv, "GET", "/v1/me/drive", b2, ""); !strings.Contains(d, `"connected":false`) {
		t.Fatalf("Bob sees Alice's Drive as his: %s", d)
	}
	if code, _ := call(t, srv, "GET", "/v1/providers/gdrive/signin", b2, ""); code != 404 {
		t.Fatalf("Bob sees Alice's sign-in: %d", code)
	}
	if code, _ := call(t, srv, "DELETE", "/v1/me/drive", a, ""); code != 204 {
		t.Fatalf("disconnect: %d", code)
	}
	if _, d := call(t, srv, "GET", "/v1/me/drive", a, ""); !strings.Contains(d, `"connected":false`) {
		t.Fatalf("still connected after disconnect: %s", d)
	}
}

func TestPair(t *testing.T) {
	srv, _ := newTestServer(t)
	req := `{"name":"phone","url":"https://example.test:8080"}`
	if code, _ := call(t, srv, "POST", "/v1/pair", "write-tok", req); code != 403 {
		t.Fatalf("write token may not pair: %d", code)
	}
	if code, _ := call(t, srv, "POST", "/v1/pair", "admin-tok", `{"name":"phone","url":"file:///etc"}`); code != 400 && code != 422 {
		t.Fatalf("bad url accepted: %d", code)
	}
	pair := func() string {
		code, b := call(t, srv, "POST", "/v1/pair", "admin-tok", req)
		var out pairOut
		if code != 200 || json.Unmarshal([]byte(b), &out) != nil || !strings.HasPrefix(out.QR, "data:image/png;base64,") {
			t.Fatalf("%d %s", code, b)
		}
		u, _ := url.Parse(out.Payload)
		if u.Query().Get("url") != "https://example.test:8080" {
			t.Fatalf("payload %s", out.Payload)
		}
		return u.Query().Get("token")
	}
	first := pair()
	if code, _ := call(t, srv, "POST", "/v1/jobs/x/pause", first, ""); code == 401 || code == 403 {
		t.Fatalf("new device token (default write) refused: %d", code)
	}
	second := pair()
	if code, _ := call(t, srv, "GET", "/v1/jobs", first, ""); code != 401 {
		t.Fatalf("re-pairing must revoke the old token: %d", code)
	}
	if code, _ := call(t, srv, "GET", "/v1/jobs", second, ""); code != 200 {
		t.Fatalf("second token: %d", code)
	}
	if code, _ := call(t, srv, "GET", "/v1/jobs", "admin-tok", ""); code != 200 {
		t.Fatalf("other tokens must survive: %d", code)
	}
}

func TestArchiveBrowse(t *testing.T) {
	srv, cfgPath := newTestServer(t)
	dl := filepath.Join(filepath.Dir(cfgPath), "dl") // newTestServer's download folder
	zf, _ := os.Create(filepath.Join(dl, "pack.zip"))
	zw := zip.NewWriter(zf)
	w, _ := zw.Create("a/hello.txt")
	w.Write([]byte("hello from the zip"))
	w, _ = zw.CreateHeader(&zip.FileHeader{Name: "stored.bin", Method: zip.Store})
	w.Write([]byte("0123456789"))
	zw.Close()
	zf.Close()

	code, b := call(t, srv, "GET", "/v1/archive?path=/pack.zip", "read-tok", "")
	if code != 200 || !strings.Contains(b, `"name":"a"`) || !strings.Contains(b, `"name":"stored.bin"`) {
		t.Fatalf("root: %d %s", code, b)
	}
	if code, b = call(t, srv, "GET", "/v1/archive?path=/pack.zip&dir=a", "read-tok", ""); code != 200 || !strings.Contains(b, `"path":"a/hello.txt"`) {
		t.Fatalf("dir a: %d %s", code, b)
	}
	if code, b = call(t, srv, "GET", "/v1/archive/raw/pack.zip?entry=a/hello.txt", "read-tok", ""); code != 200 || b != "hello from the zip" {
		t.Fatalf("raw: %d %q", code, b)
	}
	if code, _ = call(t, srv, "GET", "/v1/archive/raw/pack.zip?entry=a/hello.txt", "", ""); code != 401 {
		t.Fatalf("raw without token: %d", code)
	}
	_, b = call(t, srv, "POST", "/v1/fs/sign", "read-tok", `{"path":"/pack.zip"}`)
	var sig struct{ URL string }
	json.Unmarshal([]byte(b), &sig)
	q := sig.URL[strings.Index(sig.URL, "?"):]
	if code, b = call(t, srv, "GET", "/v1/archive/raw/pack.zip"+q+"&entry=stored.bin", "", ""); code != 200 || b != "0123456789" {
		t.Fatalf("signed raw: %d %q", code, b)
	}
	for _, bad := range []string{"../video.mp4", "/etc/passwd", "a"} {
		if code, _ = call(t, srv, "GET", "/v1/archive/raw/pack.zip?entry="+url.QueryEscape(bad), "read-tok", ""); code == 200 {
			t.Fatalf("entry %q served", bad)
		}
	}
	if code, _ = call(t, srv, "GET", "/v1/archive?path=/video.mp4", "read-tok", ""); code != 400 {
		t.Fatalf("not an archive: %d", code)
	}
}

func TestCORS(t *testing.T) {
	h := cors([]string{"http://localhost:5000"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	req := httptest.NewRequest("OPTIONS", "/v1/jobs", nil)
	req.Header.Set("Origin", "http://localhost:5000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5000" {
		t.Fatalf("preflight: %d %v", rec.Code, rec.Header())
	}
	req = httptest.NewRequest("GET", "/v1/jobs", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unlisted origin allowed")
	}
}

func TestPortsBeforeTorrents(t *testing.T) {
	srv, _ := newTestServer(t)
	code, b := call(t, srv, "GET", "/v1/ports", "read-tok", "")
	if code != 200 || !strings.Contains(b, `"verdict":"not-running"`) || !strings.Contains(b, `"torrentPort":42069`) {
		t.Fatalf("%d %s", code, b)
	}
}

func TestCleanupOrphans(t *testing.T) {
	srv, cfgPath := newTestServer(t)
	dl := filepath.Join(filepath.Dir(cfgPath), "dl")
	os.MkdirAll(filepath.Join(dl, "old"), 0o755)
	os.WriteFile(filepath.Join(dl, "old", "film.mkv.part"), make([]byte, 1000), 0o644)
	os.WriteFile(filepath.Join(dl, "keep.txt"), []byte("not partial"), 0o644)
	code, b := call(t, srv, "GET", "/v1/cleanup", "read-tok", "")
	if code != 200 || !strings.Contains(b, "film.mkv.part") || !strings.Contains(b, `"bytes":1000`) {
		t.Fatalf("list: %d %s", code, b)
	}
	if code, _ = call(t, srv, "POST", "/v1/cleanup", "read-tok", ""); code != 403 {
		t.Fatalf("read token may not delete: %d", code)
	}
	if code, b = call(t, srv, "POST", "/v1/cleanup", "write-tok", ""); code != 200 || !strings.Contains(b, `"bytes":1000`) {
		t.Fatalf("cleanup: %d %s", code, b)
	}
	if _, err := os.Stat(filepath.Join(dl, "old", "film.mkv.part")); !os.IsNotExist(err) {
		t.Fatal("orphan still there")
	}
	if _, err := os.Stat(filepath.Join(dl, "keep.txt")); err != nil {
		t.Fatal("cleanup removed a normal file")
	}
}

// A whole add-on session against a local test add-on of our own (rule 7): install, browse, search, details,
// streams, play a torrent stream (becomes a stream-mode job), and save a subtitle next to a video.
func TestAddons(t *testing.T) {
	addon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/manifest.json":
			w.Write([]byte(`{"id":"org.test","version":"1.0.0","name":"Test add-on","description":"d","types":["movie"],
			  "idPrefixes":["tt"],"resources":["catalog","meta","stream","subtitles"],
			  "catalogs":[{"type":"movie","id":"top","name":"Top","extra":[{"name":"search"}]}]}`))
		case strings.HasPrefix(r.URL.Path, "/catalog/movie/top"):
			w.Write([]byte(`{"metas":[{"id":"tt0000001","type":"movie","name":"Test Pattern","poster":"https://img.test/p.jpg"}]}`))
		case r.URL.Path == "/meta/movie/tt0000001.json":
			w.Write([]byte(`{"meta":{"id":"tt0000001","type":"movie","name":"Test Pattern"}}`))
		case r.URL.Path == "/stream/movie/tt0000001.json":
			w.Write([]byte(`{"streams":[{"name":"720p","infoHash":"0123456789abcdef0123456789abcdef01234567","fileIdx":0}]}`))
		case strings.HasPrefix(r.URL.Path, "/subtitles/movie/tt0000001"):
			w.Write([]byte(`{"subtitles":[{"id":"1","url":"` + "http://" + r.Host + `/sub.srt","lang":"eng"}]}`))
		case r.URL.Path == "/sub.srt":
			w.Header().Set("Content-Type", "application/x-subrip")
			w.Write([]byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n"))
		case r.URL.Path == "/p.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("PNG fake"))
		case r.URL.Path == "/page.html":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html></html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer addon.Close()
	srv, cfgPath := newTestServer(t)

	if code, _ := call(t, srv, "GET", "/v1/addons/home", "read-tok", ""); code != 404 {
		t.Fatalf("add-ons must be off by default: %d", code)
	}
	if code, _ := call(t, srv, "POST", "/v1/addons", "write-tok", `{"url":"`+addon.URL+`/manifest.json"}`); code != 403 {
		t.Fatalf("installing needs admin: %d", code)
	}
	if code, b := call(t, srv, "POST", "/v1/addons", "admin-tok", `{"url":"`+addon.URL+`/manifest.json"}`); code != 200 || !strings.Contains(b, "Test add-on") {
		t.Fatalf("install: %d %s", code, b)
	}
	if code, _ := call(t, srv, "POST", "/v1/addons", "admin-tok", `{"url":"`+addon.URL+`/nothing/manifest.json"}`); code != 400 {
		t.Fatalf("a broken add-on was installed: %d", code)
	}
	call(t, srv, "PUT", "/v1/addons/enabled", "admin-tok", `{"enabled":true}`)
	if _, c, _ := config.Load(cfgPath, nil); !c.Addons.Enabled || len(c.Addons.URLs) != 1 {
		t.Fatalf("not saved: %+v", c.Addons)
	}

	if code, b := call(t, srv, "GET", "/v1/addons/home", "read-tok", ""); code != 200 || !strings.Contains(b, "Test Pattern") {
		t.Fatalf("home: %d %s", code, b)
	}
	if code, b := call(t, srv, "GET", "/v1/addons/search?q=test", "read-tok", ""); code != 200 || !strings.Contains(b, "tt0000001") {
		t.Fatalf("search: %d %s", code, b)
	}
	if code, b := call(t, srv, "GET", "/v1/addons/meta?type=movie&id=tt0000001", "read-tok", ""); code != 200 || !strings.Contains(b, "Test Pattern") {
		t.Fatalf("meta: %d %s", code, b)
	}
	code, b := call(t, srv, "GET", "/v1/addons/streams?type=movie&id=tt0000001", "read-tok", "")
	if code != 200 || !strings.Contains(b, `"addon":"Test add-on"`) {
		t.Fatalf("streams: %d %s", code, b)
	}
	code, b = call(t, srv, "POST", "/v1/addons/play", "write-tok",
		`{"name":"Test Pattern","stream":{"infoHash":"0123456789abcdef0123456789abcdef01234567","fileIdx":0}}`)
	if code != 200 || !strings.Contains(b, `"kind":"torrent"`) || !strings.Contains(b, `"mode":"stream"`) || !strings.Contains(b, `"fileIdx":0`) {
		t.Fatalf("play: %d %s", code, b)
	}
	code, b = call(t, srv, "GET", "/v1/addons/subtitles?type=movie&id=tt0000001&filename=video.mp4", "read-tok", "")
	if code != 200 || !strings.Contains(b, "sub.srt") {
		t.Fatalf("subtitles: %d %s", code, b)
	}
	code, b = call(t, srv, "POST", "/v1/addons/subtitles/save", "write-tok", `{"url":"`+addon.URL+`/sub.srt","video":"/video.mp4","lang":"eng"}`)
	if code != 200 || !strings.Contains(b, `"/video.eng.srt"`) {
		t.Fatalf("save: %d %s", code, b)
	}
	if got, _ := os.ReadFile(filepath.Join(filepath.Dir(cfgPath), "dl", "video.eng.srt")); !strings.Contains(string(got), "Hello") {
		t.Fatalf("saved subtitle: %q", got)
	}
	// Saving again never overwrites: a second file appears.
	if _, b = call(t, srv, "POST", "/v1/addons/subtitles/save", "write-tok", `{"url":"`+addon.URL+`/sub.srt","video":"/video.mp4","lang":"eng"}`); !strings.Contains(b, "video.eng.2.srt") {
		t.Fatalf("second save: %s", b)
	}

	// Direct links also become downloads on the server; external links are only handed back.
	code, b = call(t, srv, "POST", "/v1/addons/play", "write-tok", `{"stream":{"url":"`+addon.URL+`/sub.srt"}}`)
	if code != 200 || !strings.Contains(b, `"kind":"web"`) || !strings.Contains(b, `"mode":"stream"`) {
		t.Fatalf("play url: %d %s", code, b)
	}
	code, b = call(t, srv, "POST", "/v1/addons/play", "write-tok", `{"stream":{"externalUrl":"https://watch.test/x"}}`)
	if code != 200 || !strings.Contains(b, `"external":"https://watch.test/x"`) {
		t.Fatalf("external: %d %s", code, b)
	}

	// Posters and subtitles pass through the server; other content does not; signed links work without a token.
	img := "/v1/addons/proxy?url=" + url.QueryEscape(addon.URL+"/p.png")
	if code, b = call(t, srv, "GET", img, "read-tok", ""); code != 200 || !strings.HasPrefix(b, "PNG") {
		t.Fatalf("proxy image: %d %q", code, b)
	}
	if code, _ = call(t, srv, "GET", img, "", ""); code != 401 {
		t.Fatalf("proxy without token: %d", code)
	}
	if code, _ = call(t, srv, "GET", "/v1/addons/proxy?url="+url.QueryEscape(addon.URL+"/page.html"), "read-tok", ""); code != 415 {
		t.Fatalf("proxy passed html: %d", code)
	}
	_, b = call(t, srv, "POST", "/v1/addons/proxy/sign", "read-tok", `{"url":"`+addon.URL+`/sub.srt"}`)
	var sg struct{ URL string }
	json.Unmarshal([]byte(b), &sg)
	if code, b = call(t, srv, "GET", sg.URL, "", ""); code != 200 || !strings.Contains(b, "Hello") {
		t.Fatalf("signed proxy: %d %q", code, b)
	}

	// Switching one add-on off hides it; the defaults are only added when nothing was ever installed.
	call(t, srv, "PUT", "/v1/addons/state", "admin-tok", `{"url":"`+addon.URL+`","enabled":false}`)
	if _, b = call(t, srv, "GET", "/v1/addons/home", "read-tok", ""); strings.TrimSpace(b) != "[]" {
		t.Fatalf("disabled add-on still answers: %s", b)
	}
	if _, b = call(t, srv, "GET", "/v1/addons", "read-tok", ""); !strings.Contains(b, `"enabled":false`) {
		t.Fatalf("state not shown: %s", b)
	}
}

func TestAddonDefaults(t *testing.T) {
	srv, cfgPath := newTestServer(t)
	call(t, srv, "PUT", "/v1/addons/enabled", "admin-tok", `{"enabled":true}`)
	if _, c, _ := config.Load(cfgPath, nil); len(c.Addons.URLs) != 3 || !strings.Contains(strings.Join(c.Addons.URLs, " "), "cinemeta") {
		t.Fatalf("defaults not installed on first switch-on: %v", c.Addons.URLs)
	}
}

// An add-on that blocks the server is asked by the app ("phone" mode): the server stores it but never contacts it.
func TestAddonPhoneMode(t *testing.T) {
	var hits atomic.Int32
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "data centres not welcome", http.StatusForbidden)
	}))
	defer blocked.Close()
	srv, cfgPath := newTestServer(t)
	if code, b := call(t, srv, "POST", "/v1/addons", "admin-tok", `{"url":"`+blocked.URL+`/manifest.json"}`); code != 400 || !strings.Contains(b, "refused this server") {
		t.Fatalf("a refusing add-on must explain itself: %d %s", code, b)
	}
	if code, b := call(t, srv, "POST", "/v1/addons", "admin-tok", `{"url":"`+blocked.URL+`/manifest.json","phone":true}`); code != 200 || !strings.Contains(b, `"phone":true`) {
		t.Fatalf("add in phone mode: %d %s", code, b)
	}
	call(t, srv, "PUT", "/v1/addons/enabled", "admin-tok", `{"enabled":true}`) // after adding: no defaults, nothing public
	hits.Store(0)
	if _, b := call(t, srv, "GET", "/v1/addons", "read-tok", ""); !strings.Contains(b, `"phone":true`) {
		t.Fatalf("list: %s", b)
	}
	for _, p := range []string{"/v1/addons/home", "/v1/addons/search?q=x", "/v1/addons/streams?type=movie&id=tt1", "/v1/addons/meta?type=movie&id=tt1"} {
		call(t, srv, "GET", p, "read-tok", "")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the server contacted a phone-mode add-on %d times", n)
	}
	if _, c, _ := config.Load(cfgPath, nil); len(c.Addons.Phone) != 1 {
		t.Fatalf("phone mode not saved: %+v", c.Addons)
	}
	if code, _ := call(t, srv, "PUT", "/v1/addons/phone", "admin-tok", `{"url":"`+blocked.URL+`","phone":false}`); code != 204 {
		t.Fatalf("switch back: %d", code)
	}
	call(t, srv, "DELETE", "/v1/addons?url="+url.QueryEscape(blocked.URL), "admin-tok", "")
	if _, c, _ := config.Load(cfgPath, nil); len(c.Addons.Phone)+len(c.Addons.URLs) != 0 {
		t.Fatalf("removal left traces: %+v", c.Addons)
	}
}
