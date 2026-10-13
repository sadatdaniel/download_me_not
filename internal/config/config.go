// Package config loads the single config file with precedence
// flags > environment (DMN_*) > file > defaults (scope §5.11).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"
)

// Keys are single lowercase words so DMN_SERVER_LISTEN maps cleanly to server.listen.
type Config struct {
	Server struct {
		Listen string `koanf:"listen"`
		// TLS: auto (off on loopback, else self-signed), off (behind a proxy, tunnel or VPN), self-signed (LAN,
		// app pins the fingerprint), acme (domain with automatic certificates).
		TLS         string `koanf:"tls"`
		Domain      string `koanf:"domain"`
		Email       string `koanf:"email"`       // acme: expiry notices from the CA
		DNSProvider string `koanf:"dnsprovider"` // acme: cloudflare or duckdns for DNS-01 (no open ports needed)
		DNSToken    string `koanf:"dnstoken"`    // acme: DNS API token; sealed automatically on first start
		HTTPListen  string `koanf:"httplisten"`  // acme: port 80 listener for HTTP-01 and redirects
		ACMECA      string `koanf:"acmeca"`      // acme: CA directory URL; empty = Let's Encrypt
		ACMECARoot  string `koanf:"acmecaroot"`  // acme: PEM root for a private CA's TLS (step-ca, tests)
		// CORS: browser origins (e.g. a Flutter web dev server) allowed to call the API. Empty = same origin only.
		CORS []string `koanf:"cors"`
	} `koanf:"server"`
	Paths struct {
		Data      string `koanf:"data"`
		Downloads string `koanf:"downloads"`
	} `koanf:"paths"`
	Auth struct {
		Tokens  []Token `koanf:"tokens"`
		Signkey string  `koanf:"signkey"` // HMAC key for signed stream URLs
	} `koanf:"auth"`
	Jobs struct {
		MaxActive     int   `koanf:"maxactive"`     // downloads at the same time (seeding ones don't count)
		Reserve       int64 `koanf:"reserve"`       // bytes to keep free in the download folder
		Extract       bool  `koanf:"extract"`       // unpack archives after download by default
		DeleteArchive bool  `koanf:"deletearchive"` // and delete the archive afterwards
	} `koanf:"jobs"`
	Updates struct {
		Tools  string `koanf:"tools"`  // auto (default): install missing helpers and update them daily; off
		Server string `koanf:"server"` // notify (default): check daily, apply on request; auto: apply when idle; off
		Token  string `koanf:"token"`  // GitHub token while the repo is private (sealed automatically); env GITHUB_TOKEN also works
	} `koanf:"updates"`
	Limits struct {
		Down int64 `koanf:"down"` // bytes/s, 0 = unlimited
		Up   int64 `koanf:"up"`
	} `koanf:"limits"`
	Providers map[string]Provider `koanf:"providers"`
	// Stremio add-ons (experimental, off by default): base URLs the server fetches catalogs, streams and subtitles
	// from. Users add them; none are shipped.
	Addons struct {
		Enabled  bool     `koanf:"enabled"`
		URLs     []string `koanf:"urls"`
		Disabled []string `koanf:"disabled"` // installed but switched off
		Phone    []string `koanf:"phone"`    // asked by the app itself (they block this server)
	} `koanf:"addons"`
}

type Token struct {
	Name  string `koanf:"name"`
	Hash  string `koanf:"hash"` // hex sha256 of the token; the token itself is never stored
	Scope string `koanf:"scope"`
}

type Provider struct {
	Enabled  *bool          `koanf:"enabled"`
	Settings map[string]any `koanf:"settings"`
}

// FlagKeys maps CLI flag names to config keys; only flags the user actually set override.
var FlagKeys = map[string]string{
	"listen":    "server.listen",
	"data":      "paths.data",
	"downloads": "paths.downloads",
}

// Secret keys are redacted by `config show`.
var secret = []string{"auth.", "server.dnstoken", "updates.token"}

// SystemPath is where install.sh puts the config.
const SystemPath = "/etc/dmn/config.yaml"

func DefaultPath() string {
	if p := os.Getenv("DMN_CONFIG"); p != "" {
		return p
	}
	if _, err := os.Stat(SystemPath); err == nil { // installed by install.sh: `sudo dmn ...` must act on it
		return SystemPath
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "dmn", "config.yaml")
}

func defaults() map[string]any {
	home, _ := os.UserHomeDir()
	return map[string]any{
		"server.listen":     "127.0.0.1:8080",
		"server.tls":        "auto",
		"server.httplisten": ":80",
		"updates.tools":     "auto",
		"updates.server":    "notify",
		"paths.data":        filepath.Join(home, ".local", "share", "dmn"),
		"paths.downloads":   filepath.Join(home, "Downloads", "dmn"),
	}
}

// Load merges all sources. flags may be nil.
func Load(path string, flags *pflag.FlagSet) (*koanf.Koanf, *Config, error) {
	k := koanf.New(".")
	if err := k.Load(confmap.Provider(defaults(), "."), nil); err != nil {
		return nil, nil, err
	}
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("config %s: %w", path, err)
	}
	if err := k.Load(env.Provider("DMN_", ".", func(s string) string {
		return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(s, "DMN_")), "_", ".")
	}), nil); err != nil {
		return nil, nil, err
	}
	if flags != nil {
		if err := k.Load(posflag.ProviderWithFlag(flags, ".", k, func(f *pflag.Flag) (string, any) {
			key, ok := FlagKeys[f.Name]
			if !ok || !f.Changed {
				return "", nil
			}
			return key, posflag.FlagVal(flags, f)
		}), nil); err != nil {
			return nil, nil, err
		}
	}
	var c Config
	if err := k.Unmarshal("", &c); err != nil {
		return nil, nil, err
	}
	return k, &c, nil
}

// LoadFile loads only the config file (missing file = empty).
func LoadFile(path string) (*koanf.Koanf, bool, error) {
	k := koanf.New(".")
	err := k.Load(file.Provider(path), yaml.Parser())
	if errors.Is(err, fs.ErrNotExist) {
		return k, false, nil
	}
	return k, err == nil, err
}

// Set writes one key into the config file only (not env/flags), creating the file if needed.
func Set(path, key string, value any) error {
	k, _, err := LoadFile(path)
	if err != nil {
		return err
	}
	if err := k.Set(key, value); err != nil {
		return err
	}
	b, err := k.Marshal(yaml.Parser())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Write-then-rename so a crash never leaves a half-written config.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	// Keep the owner: `sudo dmn pair` must not turn the service's config into a root-only file.
	if fi, err := os.Stat(path); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 {
			os.Chown(tmp, int(st.Uid), int(st.Gid))
		}
	}
	return os.Rename(tmp, path)
}

// Effective returns all merged settings with secrets redacted.
func Effective(k *koanf.Koanf) map[string]any {
	out := map[string]any{}
	for key, v := range k.All() {
		out[key] = v
		for _, s := range secret {
			if strings.HasPrefix(key, s) {
				out[key] = "<redacted>"
			}
		}
	}
	return out
}
