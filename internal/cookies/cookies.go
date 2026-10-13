// Package cookies keeps imported browser cookies per domain (scope §5.6): encrypted at rest with
// AES-GCM, sent only over HTTPS, never returned by the API and never logged.
//
// Honest limit: the key lives in the config folder (0600), separate from the data folder. That
// protects backups, copied data volumes and stray file reads, not a fully compromised server.
package cookies

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Cookie struct {
	Domain    string `json:"d"`
	Subdomain bool   `json:"s"`
	Path      string `json:"p"`
	Secure    bool   `json:"sec"`
	Expires   int64  `json:"e"` // unix seconds; 0 = session
	Name      string `json:"n"`
	Value     string `json:"v"`
}

type jar struct {
	UserAgent string   `json:"ua,omitempty"`
	Cookies   []Cookie `json:"c"`
}

type Store struct {
	dir   string
	gcm   cipher.AEAD
	scope string // the user these cookies belong to ("" = the server-wide store from before users)
}

var userRe = regexp.MustCompile(`^[0-9a-f]{1,32}$`)

// User is one user's own cookies, in a folder of their own: they apply only to that user's downloads.
// "" is the server-wide store; an id that is not a user id gives nil (no cookies).
func (s *Store) User(id string) *Store {
	if s == nil || id == "" {
		return s
	}
	if !userRe.MatchString(id) {
		return nil
	}
	return &Store{dir: filepath.Join(s.dir, "users", id), gcm: s.gcm, scope: id}
}

// Drop deletes all of one user's cookies (the user was removed).
func (s *Store) Drop() error {
	if s == nil || s.scope == "" {
		return nil
	}
	return os.RemoveAll(s.dir)
}

// aad binds a sealed jar to its domain and user, so a file moved to another name or user does not open.
func (s *Store) aad(domain string) []byte {
	if s.scope == "" {
		return []byte(domain)
	}
	return []byte(s.scope + "/" + domain) // ids are hex and domains have no slash
}

var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// Open uses (or creates) a 32-byte key at keyPath and stores jars in dir.
func Open(dir, keyPath string) (*Store, error) {
	key, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		rand.Read(key)
		if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(keyPath, key, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("cookie key %s: want 32 bytes", keyPath)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir, gcm: gcm}, nil
}

func (s *Store) path(domain string) string { return filepath.Join(s.dir, domain+".enc") }

// matches reports whether host is domain or one of its subdomains.
func matches(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// Import replaces the jar for domain with the cookies in a Netscape cookies.txt that belong to
// domain or its subdomains. Others in the file are ignored. Returns how many were kept.
func (s *Store) Import(domain string, r io.Reader, userAgent string) (int, error) {
	domain = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(domain), "."))
	if !domainRe.MatchString(domain) {
		return 0, fmt.Errorf("bad domain %q", domain)
	}
	var j jar
	j.UserAgent = userAgent
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		line = strings.TrimPrefix(line, "#HttpOnly_")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 7 {
			return 0, errors.New("not a Netscape cookies.txt (want 7 tab-separated fields per line)")
		}
		cd := strings.ToLower(strings.TrimPrefix(f[0], "."))
		if !matches(cd, domain) {
			continue
		}
		exp, _ := strconv.ParseInt(f[4], 10, 64)
		j.Cookies = append(j.Cookies, Cookie{Domain: cd, Subdomain: strings.EqualFold(f[1], "TRUE"), Path: f[2],
			Secure: strings.EqualFold(f[3], "TRUE"), Expires: exp, Name: f[5], Value: f[6]})
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	if len(j.Cookies) == 0 {
		return 0, fmt.Errorf("no cookies for %s in the file", domain)
	}
	plain, _ := json.Marshal(j)
	nonce := make([]byte, s.gcm.NonceSize())
	rand.Read(nonce)
	sealed := s.gcm.Seal(nonce, nonce, plain, s.aad(domain))
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return 0, err
	}
	tmp := s.path(domain) + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		return 0, err
	}
	return len(j.Cookies), os.Rename(tmp, s.path(domain))
}

func (s *Store) load(domain string) (jar, error) {
	var j jar
	b, err := os.ReadFile(s.path(domain))
	if err != nil {
		return j, err
	}
	n := s.gcm.NonceSize()
	if len(b) < n {
		return j, errors.New("cookie jar corrupted")
	}
	plain, err := s.gcm.Open(nil, b[:n], b[n:], s.aad(domain))
	if err != nil {
		return j, errors.New("cookie jar cannot be decrypted (wrong key or tampered)")
	}
	return j, json.Unmarshal(plain, &j)
}

type Summary struct {
	Domain    string `json:"domain"`
	Count     int    `json:"count"`
	Expired   int    `json:"expired"`
	UserAgent bool   `json:"userAgent"` // whether a matching User-Agent was saved
}

// List shows what is stored without revealing any value.
func (s *Store) List() ([]Summary, error) {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Summary{}, nil // a user who never imported any
		}
		return nil, err
	}
	now := time.Now().Unix()
	out := []Summary{}
	for _, e := range ents {
		d, ok := strings.CutSuffix(e.Name(), ".enc")
		if !ok {
			continue
		}
		j, err := s.load(d)
		if err != nil {
			out = append(out, Summary{Domain: d})
			continue
		}
		sum := Summary{Domain: d, Count: len(j.Cookies), UserAgent: j.UserAgent != ""}
		for _, c := range j.Cookies {
			if c.Expires != 0 && c.Expires < now {
				sum.Expired++
			}
		}
		out = append(out, sum)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Domain < out[b].Domain })
	return out, nil
}

func (s *Store) Delete(domain string) error {
	err := os.Remove(s.path(strings.ToLower(domain)))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no cookies for %s", domain)
	}
	return err
}

// shortLinks are share-link hosts that lead to a site: links on them get that site's cookies.
var shortLinks = map[string]string{"youtu.be": "youtube.com"}

// For returns the cookies (and saved User-Agent) that apply to rawURL, from the jar of the
// closest stored parent domain. Only for https: cookies never travel in clear text.
func (s *Store) For(rawURL string) ([]Cookie, string) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return nil, ""
	}
	host := strings.ToLower(u.Hostname())
	if site, ok := shortLinks[host]; ok {
		host = site // a share link opens the site, signed in with the site's cookies
	}
	now := time.Now().Unix()
	for d := host; strings.Contains(d, "."); d = d[strings.Index(d, ".")+1:] {
		j, err := s.load(d)
		if err != nil {
			continue
		}
		var out []Cookie
		for _, c := range j.Cookies {
			if (c.Expires != 0 && c.Expires < now) || !strings.HasPrefix(u.Path+"/", strings.TrimSuffix(c.Path, "/")+"/") {
				continue
			}
			if host == c.Domain || (c.Subdomain && matches(host, c.Domain)) {
				out = append(out, c)
			}
		}
		return out, j.UserAgent
	}
	return nil, ""
}

// Header builds a Cookie header value.
func Header(cs []Cookie) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = c.Name + "=" + c.Value
	}
	return strings.Join(parts, "; ")
}

// TempFile writes the cookies for rawURL as a Netscape file (0600) in a fresh private folder, for
// yt-dlp. Call cleanup when the process ends. Returns "" when there is nothing to send.
func (s *Store) TempFile(rawURL string) (path string, userAgent string, cleanup func(), err error) {
	cs, ua := s.For(rawURL)
	if len(cs) == 0 {
		return "", ua, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "dmn-cookies-")
	if err != nil {
		return "", "", func() {}, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	var b strings.Builder
	b.WriteString("# Netscape HTTP Cookie File\n")
	for _, c := range cs {
		d := c.Domain
		if c.Subdomain {
			d = "." + d
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", d, strings.ToUpper(strconv.FormatBool(c.Subdomain)), c.Path,
			strings.ToUpper(strconv.FormatBool(c.Secure)), c.Expires, c.Name, c.Value)
	}
	path = filepath.Join(dir, "cookies.txt")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		cleanup()
		return "", "", func() {}, err
	}
	return path, ua, cleanup, nil
}
