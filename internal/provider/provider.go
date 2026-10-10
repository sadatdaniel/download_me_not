// Package provider holds sources (inputs → something to download) and destinations
// (where finished files go). Each declares its tools and a JSON Schema for its settings,
// and the apps render whatever the server sends (scope §5.12).
package provider

import (
	"fmt"
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
	{Info: Info{Name: "web", Kind: Source, Description: "Direct HTTP, HTTPS and FTP links",
		Schema: obj(map[string]any{
			"connections": map[string]any{"type": "integer", "minimum": 1, "maximum": 32, "default": 16, "title": "Connections per download"},
		})}, defaultOn: true},
	{Info: Info{Name: "torrent", Kind: Source, Description: "Magnet links and .torrent files",
		Schema: obj(map[string]any{
			"port":       restart(map[string]any{"type": "integer", "minimum": 1, "maximum": 65535, "default": 42069, "title": "Listening port (TCP and UDP)"}),
			"bind":       restart(map[string]any{"type": "string", "default": "", "title": "Bind to address or interface (e.g. wg0); empty = all"}),
			"dht":        restart(map[string]any{"type": "boolean", "default": true, "title": "DHT (find peers without trackers)"}),
			"pex":        restart(map[string]any{"type": "boolean", "default": true, "title": "Peer exchange"}),
			"utp":        restart(map[string]any{"type": "boolean", "default": true, "title": "uTP"}),
			"ipv6":       restart(map[string]any{"type": "boolean", "default": true, "title": "IPv6"}),
			"encryption": restart(map[string]any{"type": "string", "enum": []string{"prefer", "require", "off"}, "default": "prefer", "title": "Protocol encryption"}),
			"maxPeers":   restart(map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "default": 50, "title": "Peers per torrent"}),
			"anonymous":  restart(map[string]any{"type": "boolean", "default": false, "title": "Anonymous mode (no DHT or PEX, hides client name; does NOT hide your IP)"}),
			"blocklist":  restart(map[string]any{"type": "string", "default": "", "title": "IP blocklist file (P2P format)"}),
		})}, defaultOn: true},
	{Info: Info{Name: "video", Kind: Source, Description: "YouTube and other video sites through yt-dlp", Tools: []string{"yt-dlp", "deno", "ffmpeg"},
		Schema: obj(map[string]any{})}, defaultOn: true},
	{Info: Info{Name: "local", Kind: Destination, Description: "The download folder on this server", AlwaysOn: true,
		Schema: obj(map[string]any{})}, defaultOn: true},
	{Info: Info{Name: "gdrive", Kind: Destination, Description: "Google Drive through rclone", Tools: []string{"rclone"},
		Schema: obj(map[string]any{
			"folder": map[string]any{"type": "string", "default": "dmn", "title": "Drive folder"},
			"token":  map[string]any{"type": "string", "writeOnly": true, "title": "OAuth token (set by sign-in)"},
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
	if d.signin != nil && d.signin(r.settings[d.Name]) {
		return NeedsSignin, "sign in with `dmn providers config " + d.Name + "`"
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
