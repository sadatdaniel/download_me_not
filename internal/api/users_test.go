package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func login(t *testing.T, srv *httptest.Server, name, pin, device string) (string, string) {
	t.Helper()
	code, b := call(t, srv, "POST", "/v1/login", "", `{"name":"`+name+`","pin":"`+pin+`","device":"`+device+`"}`)
	if code != 200 {
		t.Fatalf("login %s: %d %s", name, code, b)
	}
	var out loginOut
	json.Unmarshal([]byte(b), &out)
	return out.Token, out.User.ID
}

func firstEvent(t *testing.T, srv *httptest.Server, tok string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+"/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if d, ok := strings.CutPrefix(sc.Text(), "data:"); ok {
			return d
		}
	}
	t.Fatal("no SSE event")
	return ""
}

// Two users on one server never see each other's jobs, files, links, progress or preferences.
func TestUsersAreSeparate(t *testing.T) {
	srv, cfgPath, jm := newTestServerJobs(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go jm.Run(ctx)
	dl := filepath.Join(filepath.Dir(cfgPath), "dl")
	release := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow.bin" { // stays running, so it shows up in progress
			w.Header().Set("Content-Length", "1000000")
			w.Write(make([]byte, 1000))
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		http.ServeContent(w, r, "a.bin", time.Time{}, strings.NewReader("hello"))
	}))
	defer origin.Close()
	defer close(release) // before Close, which waits for the slow handler
	defer cancel()

	// The first user comes from an admin token and becomes the owner.
	if code, b := call(t, srv, "POST", "/v1/users", "write-tok", `{"name":"Alice","pin":"1111"}`); code != 403 {
		t.Fatalf("write token added a user: %d %s", code, b)
	}
	if code, b := call(t, srv, "POST", "/v1/users", "admin-tok", `{"name":"Alice","pin":"1111"}`); code != 201 || !strings.Contains(b, `"owner":true`) {
		t.Fatalf("first user: %d %s", code, b)
	}
	// From now on a token without a user reaches nobody's data, only server-wide admin routes.
	for _, tok := range []string{"read-tok", "admin-tok"} {
		if code, _ := call(t, srv, "GET", "/v1/jobs", tok, ""); code != 403 {
			t.Fatalf("%s listed jobs without signing in: %d", tok, code)
		}
		if code, _ := call(t, srv, "GET", "/v1/raw/video.mp4", tok, ""); code != 403 {
			t.Fatalf("%s read a file without signing in: %d", tok, code)
		}
	}
	if code, _ := call(t, srv, "GET", "/v1/doctor", "admin-tok", ""); code != 403 {
		// doctor is a read route: also refused
		t.Fatalf("doctor: %d", code)
	}
	if code, b := call(t, srv, "POST", "/v1/users", "admin-tok", `{"name":"Bob","pin":"2222"}`); code != 403 {
		t.Fatalf("admin token added a second user without being the owner: %d %s", code, b)
	}
	if code, b := call(t, srv, "GET", "/v1/users", "", ""); code != 200 || !strings.Contains(b, "Alice") || strings.Contains(b, "pin") {
		t.Fatalf("user names: %d %s", code, b)
	}

	// Same answer for an unknown name and a wrong PIN.
	_, wrongPIN := call(t, srv, "POST", "/v1/login", "", `{"name":"Alice","pin":"0000"}`)
	_, wrongName := call(t, srv, "POST", "/v1/login", "", `{"name":"Nobody","pin":"0000"}`)
	if !strings.Contains(wrongPIN, "wrong name or PIN") || !strings.Contains(wrongName, "wrong name or PIN") {
		t.Fatalf("login errors differ: %s / %s", wrongPIN, wrongName)
	}
	a, aliceID := login(t, srv, "alice", "1111", "laptop")
	if code, b := call(t, srv, "POST", "/v1/users", a, `{"name":"Bob","pin":"2222"}`); code != 201 {
		t.Fatalf("owner adds Bob: %d %s", code, b)
	}
	b, bobID := login(t, srv, "Bob", "2222", "phone")
	if code, _ := call(t, srv, "POST", "/v1/users", b, `{"name":"Eve","pin":"3333"}`); code != 403 {
		t.Fatalf("Bob added a user: %d", code)
	}

	// Files: Alice's folder is invisible to Bob, also through tricks.
	os.MkdirAll(filepath.Join(dl, "users", aliceID), 0o755)
	os.WriteFile(filepath.Join(dl, "users", aliceID, "secret.mp4"), []byte("alice's"), 0o644)
	if code, body := call(t, srv, "GET", "/v1/fs?path=/", a, ""); code != 200 || !strings.Contains(body, "secret.mp4") {
		t.Fatalf("Alice's listing: %d %s", code, body)
	}
	if code, body := call(t, srv, "GET", "/v1/fs?path=/", b, ""); code != 200 || strings.Contains(body, "secret.mp4") || strings.Contains(body, "video.mp4") {
		t.Fatalf("Bob's listing shows other files: %d %s", code, body)
	}
	for _, p := range []string{"/v1/raw/secret.mp4", "/v1/raw/../" + aliceID + "/secret.mp4", "/v1/fs/stat?path=../" + aliceID + "/secret.mp4", "/v1/media"} {
		if code, body := call(t, srv, "GET", p, b, ""); strings.Contains(body, "alice's") || strings.Contains(body, "secret") || code == 200 && p != "/v1/media" {
			t.Fatalf("Bob reached %s: %d %s", p, code, body)
		}
	}
	// Signed links name their user; changing or dropping that breaks the signature.
	_, sb := call(t, srv, "POST", "/v1/fs/sign", a, `{"path":"/secret.mp4"}`)
	link := sb[strings.Index(sb, "/v1/raw/"):strings.LastIndex(sb, `"`)]
	link = strings.ReplaceAll(link, `&`, "&")
	if code, body := call(t, srv, "GET", link, "", ""); code != 200 || body != "alice's" {
		t.Fatalf("Alice's signed link: %d %s", code, body)
	}
	for _, bad := range []string{strings.Replace(link, "u="+aliceID, "u="+bobID, 1), strings.Replace(link, "&u="+aliceID, "", 1)} {
		if code, _ := call(t, srv, "GET", bad, "", ""); code != 403 {
			t.Fatalf("tampered link %s: %d", bad, code)
		}
	}

	// Jobs: Bob can't see, touch, sign or stream Alice's.
	code, jb := call(t, srv, "POST", "/v1/jobs", a, `{"source":"`+origin.URL+`/a.bin"}`)
	if code != 201 {
		t.Fatalf("Alice adds: %d %s", code, jb)
	}
	var job struct{ ID string }
	json.Unmarshal([]byte(jb), &job)
	_, sj := call(t, srv, "POST", "/v1/jobs", a, `{"source":"`+origin.URL+`/slow.bin"}`)
	var slow struct{ ID string }
	json.Unmarshal([]byte(sj), &slow)
	if _, body := call(t, srv, "GET", "/v1/jobs", b, ""); strings.Contains(body, job.ID) {
		t.Fatalf("Bob lists Alice's job: %s", body)
	}
	for _, r := range [][2]string{{"GET", "/v1/jobs/" + job.ID}, {"POST", "/v1/jobs/" + job.ID + "/pause"}, {"POST", "/v1/jobs/" + job.ID + "/sign"},
		{"GET", "/v1/jobs/" + job.ID + "/stream"}, {"DELETE", "/v1/jobs/" + job.ID}} {
		if code, _ := call(t, srv, r[0], r[1], b, ""); code != 404 {
			t.Fatalf("Bob %s %s: %d", r[0], r[1], code)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, body := call(t, srv, "GET", "/v1/jobs/"+job.ID, a, "")
		if strings.Contains(body, `"state":"done"`) {
			if strings.Contains(body, "users/") {
				t.Fatalf("job path shows the server layout: %s", body)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job not done: %s", body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dl, "users", aliceID, "a.bin")); err != nil {
		t.Fatalf("download not in Alice's folder: %v", err)
	}
	for time.Now().Before(deadline) && !strings.Contains(firstEvent(t, srv, a), slow.ID) {
		time.Sleep(100 * time.Millisecond)
	}
	if ev := firstEvent(t, srv, a); !strings.Contains(ev, slow.ID) {
		t.Fatalf("Alice's progress misses her job: %s", ev)
	}
	if ev := firstEvent(t, srv, b); strings.Contains(ev, slow.ID) {
		t.Fatalf("Bob sees Alice's progress: %s", ev)
	}

	// Cookies are per user: Alice's sign-ins never show up (or get used) for Bob.
	if code, body := call(t, srv, "POST", "/v1/cookies", a, `{"domain":"youtube.com","content":".youtube.com\tTRUE\t/\tTRUE\t0\tSID\tALICE"}`); code != 200 {
		t.Fatalf("Alice imports cookies: %d %s", code, body)
	}
	if _, body := call(t, srv, "GET", "/v1/cookies", a, ""); !strings.Contains(body, "youtube.com") {
		t.Fatalf("Alice's cookies: %s", body)
	}
	if _, body := call(t, srv, "GET", "/v1/cookies", b, ""); strings.Contains(body, "youtube.com") {
		t.Fatalf("Bob sees Alice's cookies: %s", body)
	}
	if code, _ := call(t, srv, "DELETE", "/v1/cookies/youtube.com", b, ""); code != 404 {
		t.Fatalf("Bob removed Alice's cookies: %d", code)
	}

	// Preferences are per user.
	if code, body := call(t, srv, "PUT", "/v1/prefs/favorites", a, `["tt1"]`); code != 204 {
		t.Fatalf("set pref: %d %s", code, body)
	}
	if _, body := call(t, srv, "GET", "/v1/prefs", a, ""); !strings.Contains(body, "tt1") {
		t.Fatalf("Alice's prefs: %s", body)
	}
	if _, body := call(t, srv, "GET", "/v1/prefs", b, ""); strings.Contains(body, "tt1") {
		t.Fatalf("Bob sees Alice's prefs: %s", body)
	}

	// PINs: your own needs the current one; only the owner resets others.
	if code, _ := call(t, srv, "PUT", "/v1/users/"+bobID+"/pin", b, `{"pin":"5555","current":"9999"}`); code != 403 {
		t.Fatalf("Bob changed his PIN without the current one: %d", code)
	}
	if code, _ := call(t, srv, "PUT", "/v1/users/"+aliceID+"/pin", b, `{"pin":"5555"}`); code != 403 {
		t.Fatalf("Bob changed Alice's PIN: %d", code)
	}
	if code, _ := call(t, srv, "PUT", "/v1/users/"+bobID+"/pin", a, `{"pin":"5555"}`); code != 204 {
		t.Fatalf("owner resets Bob's PIN: %d", code)
	}

	// Devices: list, sign one out.
	a2, _ := login(t, srv, "Alice", "1111", "tv")
	if _, body := call(t, srv, "GET", "/v1/devices", a, ""); !strings.Contains(body, `"laptop","current":true`) || !strings.Contains(body, `"tv"`) {
		t.Fatalf("devices: %s", body)
	}
	if code, _ := call(t, srv, "DELETE", "/v1/devices/tv", a, ""); code != 204 {
		t.Fatalf("sign out tv: %d", code)
	}
	if code, _ := call(t, srv, "GET", "/v1/jobs", a2, ""); code != 401 {
		t.Fatalf("signed-out device still works: %d", code)
	}

	// Removing Bob signs his devices out and (with deleteFiles) removes his folder.
	call(t, srv, "POST", "/v1/fs/mkdir", b, `{"path":"/x"}`)
	if code, _ := call(t, srv, "DELETE", "/v1/users/"+aliceID, a, ""); code != 400 {
		t.Fatalf("owner removed themselves: %d", code)
	}
	if code, _ := call(t, srv, "DELETE", "/v1/users/"+bobID+"?deleteFiles=true", a, ""); code != 204 {
		t.Fatalf("remove Bob: %d", code)
	}
	if code, _ := call(t, srv, "GET", "/v1/jobs", b, ""); code != 401 {
		t.Fatalf("removed user's device still works: %d", code)
	}
	if _, err := os.Stat(filepath.Join(dl, "users", bobID)); !os.IsNotExist(err) {
		t.Fatalf("Bob's folder kept: %v", err)
	}
}
