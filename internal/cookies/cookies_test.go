package cookies

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const jarFile = "# Netscape HTTP Cookie File\n" +
	".example.com\tTRUE\t/\tTRUE\t0\tsid\tsecret-session\n" +
	"#HttpOnly_media.example.com\tFALSE\t/videos\tTRUE\t0\tmtok\tmedia-token\n" +
	".example.com\tTRUE\t/\tTRUE\t1\told\texpired\n" +
	".other.org\tTRUE\t/\tTRUE\t0\tleak\tnot-for-example\n"

func TestImportAndUse(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "data", "cookies"), filepath.Join(dir, "config", "cookie.key"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.Import("example.com", strings.NewReader(jarFile), "Mozilla/5.0 test")
	if err != nil || n != 3 {
		t.Fatalf("import: %d %v", n, err) // other.org ignored
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "data", "cookies", "example.com.enc"))
	if bytes.Contains(raw, []byte("secret-session")) {
		t.Fatal("stored in clear text")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "config", "cookie.key")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v", fi.Mode().Perm())
	}

	cs, ua := s.For("https://media.example.com/videos/1")
	if h := Header(cs); h != "sid=secret-session; mtok=media-token" || ua != "Mozilla/5.0 test" {
		t.Fatalf("header %q ua %q", h, ua)
	}
	if cs, _ := s.For("https://www.example.com/"); Header(cs) != "sid=secret-session" {
		t.Fatalf("subdomain/path scoping: %q", Header(cs))
	}
	if cs, _ := s.For("http://www.example.com/"); len(cs) != 0 {
		t.Fatal("cookies sent over plain http")
	}
	if cs, _ := s.For("https://evil-example.com/"); len(cs) != 0 {
		t.Fatal("cookies sent to a look-alike domain")
	}

	list, _ := s.List()
	if len(list) != 1 || list[0].Count != 3 || list[0].Expired != 1 || !list[0].UserAgent {
		t.Fatalf("list: %+v", list)
	}

	p, _, cleanup, err := s.TempFile("https://www.example.com/")
	if err != nil || p == "" {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "\tsid\tsecret-session") {
		t.Fatalf("temp file: %q", b)
	}
	cleanup()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("temp cookie file left behind")
	}

	// A jar renamed to another domain must not decrypt (domain is bound into the ciphertext).
	os.Rename(filepath.Join(dir, "data", "cookies", "example.com.enc"), filepath.Join(dir, "data", "cookies", "other.org.enc"))
	if cs, _ := s.For("https://other.org/"); len(cs) != 0 {
		t.Fatal("swapped jar decrypted")
	}
	if _, err := s.Import("../etc", strings.NewReader(jarFile), ""); err == nil {
		t.Fatal("bad domain accepted")
	}
	if _, err := s.Import("example.com", strings.NewReader("not a cookie file\n"), ""); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestPerUser(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, filepath.Join(dir, "k"))
	if err != nil {
		t.Fatal(err)
	}
	a, b := s.User("aaaa"), s.User("bbbb")
	if _, err := a.Import("youtube.com", strings.NewReader(".youtube.com\tTRUE\t/\tTRUE\t0\tSID\tALICE"), ""); err != nil {
		t.Fatal(err)
	}
	if cs, _ := a.For("https://www.youtube.com/watch"); len(cs) != 1 {
		t.Fatalf("Alice's own cookies: %v", cs)
	}
	if cs, _ := b.For("https://www.youtube.com/watch"); len(cs) != 0 {
		t.Fatal("Bob's downloads would use Alice's cookies")
	}
	if cs, _ := s.For("https://www.youtube.com/watch"); len(cs) != 0 {
		t.Fatal("a user's cookies leaked into the server-wide store")
	}
	// A jar copied into another user's folder does not open there (sealed with the user).
	os.MkdirAll(filepath.Join(dir, "users", "bbbb"), 0o700)
	data, _ := os.ReadFile(filepath.Join(dir, "users", "aaaa", "youtube.com.enc"))
	os.WriteFile(filepath.Join(dir, "users", "bbbb", "youtube.com.enc"), data, 0o600)
	if cs, _ := b.For("https://www.youtube.com/watch"); len(cs) != 0 {
		t.Fatal("a copied jar opened for another user")
	}
	if s.User("../x") != nil {
		t.Fatal("a user id that is not hex must not name a folder")
	}
	if err := a.Drop(); err != nil {
		t.Fatal(err)
	}
	if l, _ := a.List(); len(l) != 0 {
		t.Fatalf("dropped user still has cookies: %v", l)
	}
}
