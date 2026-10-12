// Package api is the versioned HTTP API. It is the only interface: the CLI, web UI and
// Android app are all clients (scope §4.1). The OpenAPI spec is generated from these handlers
// at /openapi.json; /v1 changes must be additive (docs/architecture.md).
package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/danielgtaylor/huma/v2/sse"
	"golang.org/x/oauth2"

	"github.com/sadatdaniel/download_me_not/internal/addons"
	"github.com/sadatdaniel/download_me_not/internal/config"
	"github.com/sadatdaniel/download_me_not/internal/cookies"
	"github.com/sadatdaniel/download_me_not/internal/fsapi"
	"github.com/sadatdaniel/download_me_not/internal/jobs"
	"github.com/sadatdaniel/download_me_not/internal/limits"
	"github.com/sadatdaniel/download_me_not/internal/provider"
	"github.com/sadatdaniel/download_me_not/internal/secret"
	"github.com/sadatdaniel/download_me_not/internal/store"
	"github.com/sadatdaniel/download_me_not/internal/tools"
	"github.com/sadatdaniel/download_me_not/internal/webui"
)

type Deps struct {
	Version        string
	Cfg            *config.Config
	CfgPath        string
	DB             *sql.DB
	FS             *fsapi.FS
	Providers      *provider.Registry
	Jobs           *jobs.Manager
	Limits         *limits.Limits
	Cookies        *cookies.Store
	Secret         *secret.Box
	Tools          *tools.Installer // managed helper tools; nil = not offered
	Updates        *Updates         // server self-update; nil = not offered (e.g. tests)
	GoogleEndpoint *oauth2.Endpoint // tests point Drive sign-in at a fake server; nil = Google
	Addons         *addons.Client   // Stremio add-on client (guarded HTTP); nil = feature not offered
	Fingerprint    string           // SHA-256 of the self-signed certificate, for pairing; empty = none to pin
}

var rank = map[string]int{"read": 1, "write": 2, "admin": 3}

// HashToken is how tokens are stored in the config file.
func HashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

type server struct {
	Deps
	mu       sync.Mutex
	tokens   map[string]string // token hash → scope
	cfgMtime time.Time
	signin   signIns
	checked  time.Time
	addon    addonState
}

func (s *server) scope(authz string) string {
	tok, ok := strings.CutPrefix(authz, "Bearer ")
	if !ok || tok == "" {
		return ""
	}
	h := HashToken(tok) // map lookup on a hash: no timing leak of the token itself
	s.mu.Lock()
	defer s.mu.Unlock()
	if sc, ok := s.tokens[h]; ok {
		return sc
	}
	// Unknown token: maybe `dmn pair` or `dmn setup` just added it. Reload when the config file
	// changed, at most once a second, so new devices work without a restart.
	if time.Since(s.checked) > time.Second {
		s.checked = time.Now()
		if fi, err := os.Stat(s.CfgPath); err == nil && fi.ModTime().After(s.cfgMtime) {
			if _, c, err := config.Load(s.CfgPath, nil); err == nil {
				s.cfgMtime = fi.ModTime()
				s.setTokens(c.Auth.Tokens)
			}
		}
	}
	return s.tokens[h]
}

func (s *server) setTokens(ts []config.Token) {
	s.tokens = map[string]string{}
	for _, t := range ts {
		if rank[t.Scope] > 0 {
			s.tokens[t.Hash] = t.Scope
		}
	}
}

func New(d Deps) (http.Handler, huma.API) {
	s := &server{Deps: d}
	s.setTokens(d.Cfg.Auth.Tokens)
	if fi, err := os.Stat(d.CfgPath); err == nil {
		s.cfgMtime = fi.ModTime()
	}
	mux := http.NewServeMux()
	cfg := huma.DefaultConfig("download_me_not", d.Version)
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{"bearer": {Type: "http", Scheme: "bearer"}}
	api := humago.New(mux, cfg)
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		need := ""
		for _, sec := range ctx.Operation().Security {
			if sc := sec["bearer"]; len(sc) > 0 {
				need = sc[0]
			}
		}
		if need == "" {
			next(ctx)
			return
		}
		have := s.scope(ctx.Header("Authorization"))
		switch {
		case have == "":
			huma.WriteErr(api, ctx, http.StatusUnauthorized, "missing or invalid token")
		case rank[have] < rank[need]:
			huma.WriteErr(api, ctx, http.StatusForbidden, "this token has scope "+have+"; "+need+" is required")
		default:
			next(ctx)
		}
	})
	s.routes(api)
	mux.HandleFunc("GET /v1/raw/{path...}", s.raw)
	mux.HandleFunc("GET /v1/jobs/{id}/stream", s.stream)
	mux.HandleFunc("GET /v1/zip/{path...}", s.zip)
	mux.HandleFunc("GET /v1/archive/raw/{path...}", s.archiveRaw)
	ui := webui.Handler()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			http.NotFound(w, r)
			return
		}
		ui.ServeHTTP(w, r)
	})
	return cors(d.Cfg.Server.CORS, mux), api
}

// cors lets the listed browser origins (a Flutter web dev server) call the API. Tokens travel in the
// Authorization header, never in cookies, so allowing an origin does not let it act on its own.
func cors(origins []string, next http.Handler) http.Handler {
	if len(origins) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o := r.Header.Get("Origin")
		if o != "" && slices.Contains(origins, o) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", o)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Content-Disposition")
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Range")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func op(id, method, path, scope, summary string) huma.Operation {
	o := huma.Operation{OperationID: id, Method: method, Path: path, Summary: summary}
	if scope != "" {
		o.Security = []map[string][]string{{"bearer": {scope}}}
	}
	return o
}

func fsErr(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return huma.Error404NotFound("not found")
	case errors.Is(err, fs.ErrExist):
		return huma.Error409Conflict("already exists")
	default:
		return huma.Error400BadRequest(err.Error())
	}
}

type pathIn struct {
	Path string `query:"path" default:"/" doc:"Path inside the download folder"`
}

func body[T any](v T) *struct{ Body T } { return &struct{ Body T }{v} }

func (s *server) routes(api huma.API) {
	huma.Register(api, op("health", "GET", "/v1/health", "", "Liveness and version"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body map[string]string }, error) {
			return body(map[string]string{"status": "ok", "version": s.Version}), nil
		})

	huma.Register(api, op("list-jobs", "GET", "/v1/jobs", "read", "List downloads"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body []store.Job }, error) {
			jobs, err := store.ListJobs(s.DB)
			return body(jobs), err
		})
	s.jobRoutes(api)
	s.cookieRoutes(api)
	s.providerRoutes(api)
	s.toolRoutes(api)
	s.updateRoutes(api)
	s.pairRoutes(api)
	s.archiveRoutes(api)
	s.portRoutes(api)
	s.cleanupRoutes(api)
	s.addonRoutes(api)

	sse.Register(api, op("events", "GET", "/v1/events", "read", "Live progress of running jobs (server-sent events, every second)"),
		map[string]any{"progress": []jobs.Progress{}},
		func(ctx context.Context, _ *struct{}, send sse.Sender) {
			t := time.NewTicker(time.Second) // also keeps proxies and phones from dropping idle streams
			defer t.Stop()
			for {
				if send.Data(s.Jobs.Progress()) != nil {
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		})

	huma.Register(api, op("media", "GET", "/v1/media", "read", "Every video and audio file under the download folder, newest first"),
		func(ctx context.Context, in *struct {
			Limit int `query:"limit" default:"2000" minimum:"1" maximum:"20000"`
		}) (*struct{ Body []fsapi.Entry }, error) {
			es, err := s.FS.Media(in.Limit)
			return body(es), err
		})
	huma.Register(api, op("fs-list", "GET", "/v1/fs", "read", "List a folder"),
		func(ctx context.Context, in *pathIn) (*struct{ Body []fsapi.Entry }, error) {
			es, err := s.FS.List(in.Path)
			if err != nil {
				return nil, fsErr(err)
			}
			return body(es), nil
		})
	huma.Register(api, op("fs-stat", "GET", "/v1/fs/stat", "read", "Inspect one entry"),
		func(ctx context.Context, in *pathIn) (*struct{ Body fsapi.Entry }, error) {
			e, err := s.FS.Stat(in.Path)
			if err != nil {
				return nil, fsErr(err)
			}
			return body(e), nil
		})
	huma.Register(api, op("fs-usage", "GET", "/v1/fs/usage", "read", "Disk usage and free space"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body fsapi.Usage }, error) {
			u, err := s.FS.Usage()
			return body(u), err
		})
	huma.Register(api, op("fs-mkdir", "POST", "/v1/fs/mkdir", "write", "Create a folder"),
		func(ctx context.Context, in *struct {
			Body struct {
				Path string `json:"path" minLength:"1"`
			}
		}) (*struct{}, error) {
			if err := s.FS.Mkdir(in.Body.Path); err != nil {
				return nil, fsErr(err)
			}
			return nil, nil
		})
	huma.Register(api, op("fs-move", "POST", "/v1/fs/move", "write", "Rename or move (never overwrites)"),
		func(ctx context.Context, in *struct {
			Body struct {
				From string `json:"from" minLength:"1"`
				To   string `json:"to" minLength:"1"`
			}
		}) (*struct{}, error) {
			if err := s.FS.Move(in.Body.From, in.Body.To); err != nil {
				return nil, fsErr(err)
			}
			return nil, nil
		})
	huma.Register(api, op("fs-copy", "POST", "/v1/fs/copy", "write", "Copy a file or folder (never overwrites)"),
		func(ctx context.Context, in *struct {
			Body struct {
				From string `json:"from" minLength:"1"`
				To   string `json:"to" minLength:"1"`
			}
		}) (*struct{}, error) {
			if err := s.FS.Copy(in.Body.From, in.Body.To); err != nil {
				return nil, fsErr(err)
			}
			return nil, nil
		})
	huma.Register(api, op("fs-delete", "DELETE", "/v1/fs", "write", "Delete a file or folder"),
		func(ctx context.Context, in *struct {
			Path string `query:"path" required:"true"`
		}) (*struct{}, error) {
			if err := s.FS.Remove(in.Path); err != nil {
				return nil, fsErr(err)
			}
			return nil, nil
		})
	huma.Register(api, op("fs-sign", "POST", "/v1/fs/sign", "read", "Short-lived stream URL that needs no token"),
		func(ctx context.Context, in *struct {
			Body struct {
				Path       string `json:"path" minLength:"1"`
				TTLSeconds int    `json:"ttlSeconds,omitempty" minimum:"1" maximum:"86400" default:"21600"`
			}
		}) (*struct{ Body map[string]string }, error) {
			if _, err := s.FS.Stat(in.Body.Path); err != nil {
				return nil, fsErr(err)
			}
			ttl := in.Body.TTLSeconds
			if ttl == 0 {
				ttl = 21600
			}
			q, err := s.FS.Sign(in.Body.Path, time.Duration(ttl)*time.Second)
			if err != nil {
				return nil, fsErr(err)
			}
			return body(map[string]string{"url": "/v1/raw/" + strings.TrimPrefix(in.Body.Path, "/") + "?" + q}), nil
		})

	huma.Register(api, op("providers", "GET", "/v1/providers", "read", "Providers with status and settings schema"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body []provider.Info }, error) {
			return body(s.Providers.List()), nil
		})
	huma.Register(api, op("provider-enable", "PUT", "/v1/providers/{name}/enabled", "admin", "Switch a provider on or off (no restart)"),
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
			Body struct {
				Enabled bool `json:"enabled"`
			}
		}) (*struct{}, error) {
			if err := s.Providers.SetEnabled(in.Name, in.Body.Enabled); err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			return nil, config.Set(s.CfgPath, "providers."+in.Name+".enabled", in.Body.Enabled)
		})

	type limitsBody struct {
		Down int64 `json:"down" minimum:"0" doc:"bytes/s for all downloads together, 0 = unlimited"`
		Up   int64 `json:"up" minimum:"0" doc:"bytes/s torrent upload, 0 = unlimited"`
	}
	huma.Register(api, op("get-limits", "GET", "/v1/limits", "read", "Global bandwidth limits"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body limitsBody }, error) {
			d, u := s.Limits.Get()
			return body(limitsBody{d, u}), nil
		})
	huma.Register(api, op("set-limits", "PUT", "/v1/limits", "write", "Change global bandwidth limits (applies immediately, saved to config)"),
		func(ctx context.Context, in *struct{ Body limitsBody }) (*struct{}, error) {
			s.Limits.Set(in.Body.Down, in.Body.Up)
			if err := config.Set(s.CfgPath, "limits.down", in.Body.Down); err != nil {
				return nil, err
			}
			return nil, config.Set(s.CfgPath, "limits.up", in.Body.Up)
		})

	huma.Register(api, op("learned-trackers", "GET", "/v1/swarm/trackers", "read", "Swarm memory: trackers remembered from completed public torrents"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body []store.LearnedTracker }, error) {
			ts, err := store.TopTrackers(s.DB, 100)
			return body(ts), err
		})
	huma.Register(api, op("forget-trackers", "DELETE", "/v1/swarm/trackers", "admin", "Forget all remembered trackers"),
		func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			return nil, store.ForgetTrackers(s.DB)
		})

	huma.Register(api, op("doctor", "GET", "/v1/doctor", "read", "Helper tools present or missing"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body []tools.Tool }, error) {
			return body(tools.Doctor(s.Cfg.Paths.Data)), nil
		})
}

// raw streams a file with Range support. Auth: a signed URL (for players) or a read token.
func (s *server) raw(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("path")
	q := r.URL.Query()
	if q.Get("sig") != "" {
		if !s.FS.Verify(p, q.Get("exp"), q.Get("sig")) {
			http.Error(w, "invalid or expired link", http.StatusForbidden)
			return
		}
	} else if rank[s.scope(r.Header.Get("Authorization"))] < rank["read"] {
		http.Error(w, "missing or invalid token", http.StatusUnauthorized)
		return
	}
	s.FS.Serve(w, r, p)
}

// zip downloads a folder as a ZIP (read token or a signed link to the folder).
func (s *server) zip(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("path")
	q := r.URL.Query()
	if q.Get("sig") != "" {
		if !s.FS.Verify(p, q.Get("exp"), q.Get("sig")) {
			http.Error(w, "invalid or expired link", http.StatusForbidden)
			return
		}
	} else if rank[s.scope(r.Header.Get("Authorization"))] < rank["read"] {
		http.Error(w, "missing or invalid token", http.StatusUnauthorized)
		return
	}
	e, err := s.FS.Stat(p)
	if err != nil || !e.Dir {
		http.Error(w, "not a folder", http.StatusNotFound)
		return
	}
	name := e.Name
	if name == "/" {
		name = "downloads"
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".zip"}))
	if err := s.FS.Zip(w, p); err != nil {
		// Headers are sent; the client sees a truncated ZIP, which fails its checksum.
		fmt.Fprintln(os.Stderr, "zip:", err)
	}
}
