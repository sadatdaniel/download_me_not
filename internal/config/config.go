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
	} `koanf:"server"`
	Paths struct {
		Data      string `koanf:"data"`
		Downloads string `koanf:"downloads"`
	} `koanf:"paths"`
	Auth struct {
		Tokens  []Token `koanf:"tokens"`
		Signkey string  `koanf:"signkey"` // HMAC key for signed stream URLs
	} `koanf:"auth"`
	Providers map[string]Provider `koanf:"providers"`
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
var secret = []string{"auth."}

func DefaultPath() string {
	if p := os.Getenv("DMN_CONFIG"); p != "" {
		return p
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
		"server.listen":   "127.0.0.1:8080",
		"paths.data":      filepath.Join(home, ".local", "share", "dmn"),
		"paths.downloads": filepath.Join(home, "Downloads", "dmn"),
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

// Set writes one key into the config file only (not env/flags), creating the file if needed.
func Set(path, key string, value any) error {
	k := koanf.New(".")
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil && !errors.Is(err, fs.ErrNotExist) {
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
