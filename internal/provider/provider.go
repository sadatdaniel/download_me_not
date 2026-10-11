// Package provider holds sources (inputs → something to download) and destinations
// (where finished files go). Each declares its tools and a JSON Schema for its settings,
// and the apps render whatever the server sends (scope §5.12).
package provider

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/sadatdaniel/download_me_not/internal/tools"
)

type Kind string

const (
	Source      Kind = "source"
	Destination Kind = "destination"
)

type Status string

const (
	Ready       Status = "ready"
	Disabled    Status = "disabled"
	MissingTool Status = "missing-tool"
	NeedsSignin Status = "needs-signin"
)

// Info is what the API returns for one provider.
type Info struct {
	Name        string         `json:"name"`
	Kind        Kind           `json:"kind"`
	Description string         `json:"description"`
	Tools       []string       `json:"tools"`
	Optional    []string       `json:"optional,omitempty"` // helps but not required (e.g. deno for YouTube)
	AlwaysOn    bool           `json:"alwaysOn"`
	Enabled     bool           `json:"enabled"`
	Status      Status         `json:"status"`
	Reason      string         `json:"reason,omitempty"`
	Schema      map[string]any `json:"schema"` // JSON Schema of the settings; secret fields have "writeOnly": true
}

// def is a built-in provider. Behaviour (probe, upload, ...) is added per milestone.
type def struct {
	Info
	defaultOn bool
	signin    func(settings map[string]any) bool // nil: no sign-in needed
}

// restart marks a setting that takes effect after a server restart.
func restart(m map[string]any) map[string]any {
	m["x-restart"] = true
	return m
}

func obj(props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props, "additionalProperties": false}
}

var builtins = []def{
	{Info: Info{Name: "web", Kind: Source, Description: "Direct HTTP and HTTPS links",
		Schema: obj(map[string]any{
			"connections": map[string]any{"type": "integer", "minimum": 1, "maximum": 32, "default": 16, "title": "Connections per download"},
		})}, defaultOn: true},
	{Info: Info{Name: "torrent", Kind: Source, Description: "Magnet links and .torrent files",
		Schema: obj(map[string]any{
			"port":          restart(map[string]any{"type": "integer", "minimum": 1, "maximum": 65535, "default": 42069, "title": "Listening port (TCP and UDP)"}),
			"bind":          restart(map[string]any{"type": "string", "default": "", "title": "Bind to address or interface (e.g. wg0); empty = all"}),
			"dht":           restart(map[string]any{"type": "boolean", "default": true, "title": "DHT (find peers without trackers)"}),
			"pex":           restart(map[string]any{"type": "boolean", "default": true, "title": "Peer exchange"}),
			"utp":           restart(map[string]any{"type": "boolean", "default": true, "title": "uTP"}),
			"ipv6":          restart(map[string]any{"type": "boolean", "default": true, "title": "IPv6"}),
			"encryption":    restart(map[string]any{"type": "string", "enum": []string{"prefer", "require", "off"}, "default": "prefer", "title": "Protocol encryption"}),
			"maxPeers":      restart(map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "default": 50, "title": "Peers per torrent"}),
			"anonymous":     restart(map[string]any{"type": "boolean", "default": false, "title": "Anonymous mode (no DHT or PEX, hides client name; does NOT hide your IP)"}),
			"blocklist":     restart(map[string]any{"type": "string", "default": "", "title": "IP blocklist: file path or http(s) URL, P2P format, gzip ok; URLs refresh daily"}),
			"socks5":        restart(map[string]any{"type": "string", "default": "", "title": "SOCKS5 proxy host:port (or user:pass@host:port). Turns off DHT, uTP and UDP trackers so nothing bypasses it"}),
			"upnp":          restart(map[string]any{"type": "boolean", "default": false, "title": "Ask the router to forward the port (UPnP/NAT-PMP)"}),
			"learnTrackers": restart(map[string]any{"type": "boolean", "default": true, "title": "Swarm memory: remember trackers that worked and add them to new public torrents (never private ones)"}),
			"seed":          restart(map[string]any{"type": "string", "enum": []string{"stop", "ratio", "time", "forever"}, "default": "stop", "title": "After download: stop, seed to a ratio, seed for a time, or seed forever"}),
			"seedRatio":     restart(map[string]any{"type": "number", "minimum": 0, "default": 1.0, "title": "Seed ratio (for ratio)"}),
			"seedMinutes":   restart(map[string]any{"type": "integer", "minimum": 0, "default": 60, "title": "Seed minutes (for time)"}),
		})}, defaultOn: true},
	{Info: Info{Name: "video", Kind: Source, Description: "YouTube and other video sites through yt-dlp", Tools: []string{"yt-dlp"}, Optional: []string{"deno", "ffmpeg"},
		Schema: obj(map[string]any{})}, defaultOn: true},
	{Info: Info{Name: "local", Kind: Destination, Description: "The download folder on this server", AlwaysOn: true,
		Schema: obj(map[string]any{})}, defaultOn: true},
	{Info: Info{Name: "gdrive", Kind: Destination, Description: "Google Drive through rclone", Tools: []string{"rclone"},
		Schema: obj(map[string]any{
			"clientId":     map[string]any{"type": "string", "title": "Google OAuth client ID (type: TVs and Limited Input devices)"},
			"clientSecret": map[string]any{"type": "string", "writeOnly": true, "title": "Google OAuth client secret"},
			"folder":       map[string]any{"type": "string", "default": "dmn", "title": "Drive folder for copies"},
			"token":        map[string]any{"type": "string", "writeOnly": true, "readOnly": true, "title": "Set by Connect Google Drive"},
		})},
		signin: func(s map[string]any) bool { v, _ := s["token"].(string); return v == "" }},
}

// Registry is safe for concurrent use; toggles apply without a restart.
type Registry struct {
	mu       sync.RWMutex
	enabled  map[string]bool
	settings map[string]map[string]any
	dataDir  string
}

// Settings is the per-provider config as loaded from the config file.
type Settings struct {
	Enabled  *bool
	Settings map[string]any
}

func NewRegistry(cfg map[string]Settings, dataDir string) *Registry {
	r := &Registry{enabled: map[string]bool{}, settings: map[string]map[string]any{}, dataDir: dataDir}
	for _, d := range builtins {
		r.enabled[d.Name] = d.defaultOn
		if c, ok := cfg[d.Name]; ok {
			if c.Enabled != nil && !d.AlwaysOn {
				r.enabled[d.Name] = *c.Enabled
			}
			r.settings[d.Name] = c.Settings
		}
	}
	return r
}

func (r *Registry) List() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Info, 0, len(builtins))
	for _, d := range builtins {
		in := d.Info
		in.Enabled = r.enabled[d.Name]
		in.Status, in.Reason = r.status(d, in.Enabled)
		out = append(out, in)
	}
	return out
}

func (r *Registry) status(d def, enabled bool) (Status, string) {
	if !enabled {
		return Disabled, ""
	}
	var missing []string
	for _, t := range d.Tools {
		if _, ok := tools.Find(t, r.dataDir); !ok {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		return MissingTool, strings.Join(missing, ", ") + " not found; run `dmn doctor`"
	}
	var soft []string
	for _, t := range d.Optional {
		if _, ok := tools.Find(t, r.dataDir); !ok {
			soft = append(soft, t)
		}
	}
	if d.signin != nil && d.signin(r.settings[d.Name]) {
		return NeedsSignin, "sign in with `dmn providers config " + d.Name + "`"
	}
	if len(soft) > 0 {
		return Ready, strings.Join(soft, ", ") + " not installed: some sites (YouTube) or formats may not work"
	}
	return Ready, ""
}

func (r *Registry) SetEnabled(name string, on bool) error {
	for _, d := range builtins {
		if d.Name != name {
			continue
		}
		if d.AlwaysOn && !on {
			return fmt.Errorf("%s is always on", name)
		}
		r.mu.Lock()
		r.enabled[name] = on
		r.mu.Unlock()
		return nil
	}
	return fmt.Errorf("unknown provider %q", name)
}

func (r *Registry) Enabled(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.enabled[name]
}

func (r *Registry) def(name string) (def, bool) {
	for _, d := range builtins {
		if d.Name == name {
			return d, true
		}
	}
	return def{}, false
}

// Setting returns a raw stored value (secrets stay sealed; the caller decrypts).
func (r *Registry) Setting(name, key string) any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.settings[name][key]
}

// View returns a provider's settings for display: secrets show only whether they are set.
func (r *Registry) View(name string) (map[string]any, error) {
	d, ok := r.def(name)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", name)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]any{}
	props, _ := d.Schema["properties"].(map[string]any)
	for k, p := range props {
		v, set := r.settings[name][k]
		if pm, _ := p.(map[string]any); pm["writeOnly"] == true {
			out[k] = set && v != ""
			continue
		}
		if !set {
			v = props[k].(map[string]any)["default"]
		}
		out[k] = v
	}
	return out, nil
}

// Check validates one setting against the schema and reports whether it is a secret.
func (r *Registry) Check(name, key string, v any) (secret bool, err error) {
	d, ok := r.def(name)
	if !ok {
		return false, fmt.Errorf("unknown provider %q", name)
	}
	props, _ := d.Schema["properties"].(map[string]any)
	p, ok := props[key].(map[string]any)
	if !ok {
		return false, fmt.Errorf("%s has no setting %q", name, key)
	}
	if p["readOnly"] == true {
		return false, fmt.Errorf("%s.%s is set by the server", name, key)
	}
	switch p["type"] {
	case "string":
		s, ok := v.(string)
		if !ok {
			return false, fmt.Errorf("%s.%s must be a string", name, key)
		}
		if enum, ok := p["enum"].([]string); ok && !slices.Contains(enum, s) {
			return false, fmt.Errorf("%s.%s must be one of %v", name, key, enum)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return false, fmt.Errorf("%s.%s must be true or false", name, key)
		}
	case "integer", "number":
		f, ok := v.(float64) // JSON numbers
		if !ok {
			return false, fmt.Errorf("%s.%s must be a number", name, key)
		}
		if min, ok := p["minimum"].(int); ok && f < float64(min) {
			return false, fmt.Errorf("%s.%s must be at least %d", name, key, min)
		}
		if max, ok := p["maximum"].(int); ok && f > float64(max) {
			return false, fmt.Errorf("%s.%s must be at most %d", name, key, max)
		}
	}
	return p["writeOnly"] == true, nil
}

// SetSetting stores a (validated, sealed if secret) value in memory; the caller persists it.
func (r *Registry) SetSetting(name, key string, v any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.settings[name] == nil {
		r.settings[name] = map[string]any{}
	}
	r.settings[name][key] = v
}

// Restart reports whether a setting only applies after a server restart.
func (r *Registry) Restart(name, key string) bool {
	d, _ := r.def(name)
	props, _ := d.Schema["properties"].(map[string]any)
	p, _ := props[key].(map[string]any)
	return p["x-restart"] == true
}
