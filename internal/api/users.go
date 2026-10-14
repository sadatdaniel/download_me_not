package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"golang.org/x/time/rate"

	"github.com/sadatdaniel/getaload/internal/config"
	"github.com/sadatdaniel/getaload/internal/fsapi"
	"github.com/sadatdaniel/getaload/internal/jobs"
	"github.com/sadatdaniel/getaload/internal/store"
)

// Household users (scope §17): name + 4-digit PIN. Signing in creates a token for that device; the token
// carries the user, so every route sees only that user's jobs, files, links and preferences. Before the
// first user exists the server behaves as before (one shared folder).

type userKey struct{}
type addrKey struct{}

// userOf is the signed-in user of a request ("" = none: no users yet, or a server-wide admin call).
func userOf(ctx context.Context) string {
	u, _ := ctx.Value(userKey{}).(string)
	return u
}

func bearer(authz string) string {
	tok, _ := strings.CutPrefix(authz, "Bearer ")
	return tok
}

// auth returns a token's scope and the user its device signed in as.
func (s *server) auth(authz string) (scope, user string) {
	scope = s.scope(authz)
	if scope != "" {
		user, _ = store.DeviceUser(s.DB, HashToken(bearer(authz)))
	}
	return scope, user
}

func (s *server) hasUsers() bool {
	ok, err := store.HasUsers(s.DB)
	return ok || err != nil // fail closed
}

// fsOf is a user's own folder (their signed links verify only there); "" is the whole download folder.
func (s *server) fsOf(user string) (*fsapi.FS, error) {
	if user == "" {
		return s.FS, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.userFS[user]; f != nil {
		return f, nil
	}
	if _, err := store.GetUser(s.DB, user); err != nil { // also keeps a forged ?u= from naming a path
		return nil, err
	}
	f, err := s.FS.Sub(jobs.UserDir(user), user)
	if err != nil {
		return nil, err
	}
	if s.userFS == nil {
		s.userFS = map[string]*fsapi.FS{}
	}
	s.userFS[user] = f
	return f, nil
}

// fs is the caller's folder in an API handler.
func (s *server) fs(ctx context.Context) (*fsapi.FS, error) {
	f, err := s.fsOf(userOf(ctx))
	if err != nil {
		return nil, huma.Error401Unauthorized("this user no longer exists")
	}
	return f, nil
}

// access authorizes a plain HTTP route (players, downloads): a signed link, verified in the scope of the
// user it names, or a read token of a signed-in device. signed is what the link was signed for.
func (s *server) access(w http.ResponseWriter, r *http.Request, signed string) (*fsapi.FS, string, bool) {
	// Downloaded files share the web app's origin: a page or SVG opened from here must not run scripts that could
	// read the app's sign-in. sandbox gives it an origin of its own, with scripts off; media still plays.
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	q := r.URL.Query()
	if q.Get("sig") != "" {
		u := q.Get("u")
		f, err := s.fsOf(u)
		if err != nil || (u == "" && s.hasUsers()) || !f.Verify(signed, q.Get("exp"), q.Get("sig")) {
			http.Error(w, "invalid or expired link", http.StatusForbidden)
			return nil, "", false
		}
		return f, u, true
	}
	sc, u := s.auth(r.Header.Get("Authorization"))
	if rank[sc] < rank["read"] {
		http.Error(w, "missing or invalid token", http.StatusUnauthorized)
		return nil, "", false
	}
	if u == "" && s.hasUsers() {
		http.Error(w, "sign in with your name and PIN first", http.StatusForbidden)
		return nil, "", false
	}
	f, err := s.fsOf(u)
	if err != nil {
		http.Error(w, "this user no longer exists", http.StatusUnauthorized)
		return nil, "", false
	}
	return f, u, true
}

// own shows a job as its user sees it: the path inside their folder.
func own(j store.Job) store.Job {
	if j.User != "" {
		j.Path = strings.TrimPrefix(j.Path, jobs.UserDir(j.User)+"/")
	}
	return j
}

// mine returns a job of the caller; another user's job is "not found".
func (s *server) mine(ctx context.Context, id string) (store.Job, error) {
	j, err := s.Jobs.Get(id)
	if err != nil {
		return j, jobErr(err)
	}
	if u := userOf(ctx); u != j.User && (u != "" || s.hasUsers()) {
		return store.Job{}, jobErr(jobs.ErrNotFound)
	}
	return own(j), nil
}

// jobsOf lists the caller's jobs (all of them before users exist).
func (s *server) jobsOf(user string) ([]store.Job, error) {
	if user == "" {
		return store.ListJobs(s.DB)
	}
	js, err := store.ListJobsOf(s.DB, user)
	for i := range js {
		js[i] = own(js[i])
	}
	return js, err
}

// reloadTokens takes the token list from the config file now, so added and revoked tokens apply at once.
func (s *server) reloadTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fi, err := os.Stat(s.CfgPath); err == nil {
		if _, c, err := config.Load(s.CfgPath, nil); err == nil {
			s.cfgMtime = fi.ModTime()
			s.setTokens(c.Auth.Tokens)
		}
	}
}

// devicePrefix names a user's device tokens in the config file: "u:<user id>:<device name>".
func devicePrefix(user string) string { return "u:" + user + ":" }

// revoke removes tokens (and their device bindings) whose name matches.
func (s *server) revoke(match func(name string) bool) error {
	hashes, err := RemoveTokens(s.CfgPath, func(t config.Token) bool { return match(t.Name) })
	for _, h := range hashes {
		store.UnbindDevice(s.DB, h)
	}
	s.reloadTokens()
	return err
}

// loginLimit: besides the per-user lockout in the store, at most a few tries per second per client address
// and overall, so guessing across many names is slow too.
type loginLimit struct {
	global *rate.Limiter
	by     map[string]*rate.Limiter
}

func (s *server) loginAllowed(addr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.login.global == nil {
		s.login.global = rate.NewLimiter(2, 20)
	}
	if len(s.login.by) > 4096 { // ponytail: crude bound; an LRU if many addresses ever try at once
		s.login.by = nil
	}
	if s.login.by == nil {
		s.login.by = map[string]*rate.Limiter{}
	}
	l := s.login.by[addr]
	if l == nil {
		l = rate.NewLimiter(rate.Every(2*time.Second), 5)
		s.login.by[addr] = l
	}
	return l.Allow() && s.login.global.Allow()
}

type userOut struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type loginOut struct {
	Token       string  `json:"token" doc:"this device's token (shown once); send it as Authorization: Bearer"`
	User        userOut `json:"user"`
	Fingerprint string  `json:"fingerprint,omitempty" doc:"SHA-256 of the self-signed certificate"`
}

type deviceOut struct {
	Name    string `json:"name"`
	Current bool   `json:"current,omitempty" doc:"the device making this request"`
}

func (s *server) me(ctx context.Context) (store.User, error) {
	u := userOf(ctx)
	if u == "" {
		return store.User{}, huma.Error403Forbidden("sign in with your name and PIN first")
	}
	us, err := store.GetUser(s.DB, u)
	if err != nil {
		return us, huma.Error401Unauthorized("this user no longer exists")
	}
	return us, nil
}

func pinErr(err error) error {
	var l store.ErrLocked
	switch {
	case errors.As(err, &l):
		return huma.Error429TooManyRequests(err.Error())
	case errors.Is(err, store.ErrBadPIN):
		return huma.Error403Forbidden(err.Error())
	case errors.Is(err, store.ErrNoUser):
		return huma.Error404NotFound(err.Error())
	default:
		return huma.Error400BadRequest(err.Error())
	}
}

func (s *server) userRoutes(api huma.API) {
	huma.Register(api, op("users", "GET", "/v1/users", "", "Names to sign in as (no token needed)"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body []userOut }, error) {
			us, err := store.ListUsers(s.DB)
			out := make([]userOut, 0, len(us))
			for _, u := range us {
				out = append(out, userOut{u.ID, u.Name})
			}
			return body(out), err
		})

	huma.Register(api, op("login", "POST", "/v1/login", "", "Sign in with name and PIN; returns a new token for this device"),
		func(ctx context.Context, in *struct {
			Body struct {
				Name   string `json:"name" minLength:"1" maxLength:"40"`
				PIN    string `json:"pin" minLength:"4" maxLength:"4"`
				Device string `json:"device,omitempty" maxLength:"64" doc:"device name; signing in again with the same name replaces its token" default:"device"`
			}
		}) (*struct{ Body loginOut }, error) {
			addr, _ := ctx.Value(addrKey{}).(string)
			if !s.loginAllowed(addr) {
				return nil, huma.Error429TooManyRequests("too many sign-in attempts; wait a little")
			}
			u, err := store.UserByName(s.DB, in.Body.Name)
			if err == nil {
				u, err = store.CheckPIN(s.DB, u.ID, in.Body.PIN)
			}
			var l store.ErrLocked
			switch {
			case errors.As(err, &l):
				return nil, huma.Error429TooManyRequests(err.Error())
			case errors.Is(err, store.ErrNoUser), errors.Is(err, store.ErrBadPIN):
				return nil, huma.Error403Forbidden("wrong name or PIN") // the same for both
			case err != nil:
				return nil, err
			}
			dev := strings.TrimSpace(in.Body.Device)
			if dev == "" {
				dev = "device"
			}
			name := devicePrefix(u.ID) + dev
			if err := s.revoke(func(n string) bool { return n == name }); err != nil {
				return nil, err
			}
			// A sign-in never controls the server: that takes an admin key (getaload admin, scope §17).
			tok, err := AddToken(s.CfgPath, name, "write")
			if err != nil {
				return nil, err
			}
			if err := store.BindDevice(s.DB, HashToken(tok), u.ID); err != nil {
				return nil, err
			}
			s.reloadTokens()
			return body(loginOut{tok, userOut{u.ID, u.Name}, s.Fingerprint}), nil
		})

	huma.Register(api, op("me", "GET", "/v1/me", "read", "The signed-in user"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body userOut }, error) {
			u, err := s.me(ctx)
			return body(userOut{u.ID, u.Name}), err
		})

	huma.Register(api, op("add-user", "POST", "/v1/users", "admin", "Add a user (admin key)"),
		func(ctx context.Context, in *struct {
			Body struct {
				Name string `json:"name" minLength:"1" maxLength:"40"`
				PIN  string `json:"pin" minLength:"4" maxLength:"4"`
			}
		}) (*struct {
			Status int
			Body   userOut
		}, error) {
			u, err := store.CreateUser(s.DB, in.Body.Name, in.Body.PIN)
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			return &struct {
				Status int
				Body   userOut
			}{http.StatusCreated, userOut{u.ID, u.Name}}, nil
		})

	huma.Register(api, op("set-pin", "PUT", "/v1/users/{id}/pin", "write", "Change your own PIN (with the current one); an admin key resets anyone's with PUT /v1/admin/users/{id}/pin"),
		func(ctx context.Context, in *struct {
			ID   string `path:"id"`
			Body struct {
				PIN     string `json:"pin" minLength:"4" maxLength:"4"`
				Current string `json:"current,omitempty" doc:"your current PIN, when changing your own"`
			}
		}) (*struct{}, error) {
			me, err := s.me(ctx)
			if err != nil {
				return nil, err
			}
			if me.ID != in.ID {
				return nil, huma.Error403Forbidden("you can change only your own PIN")
			}
			if _, err := store.CheckPIN(s.DB, me.ID, in.Body.Current); err != nil {
				return nil, pinErr(err)
			}
			if err := store.SetPIN(s.DB, in.ID, in.Body.PIN); err != nil {
				return nil, pinErr(err)
			}
			return nil, nil
		})

	huma.Register(api, op("delete-user", "DELETE", "/v1/users/{id}", "admin", "Remove a user (admin key): stops their downloads and signs out their devices; deleteFiles also deletes their folder"),
		func(ctx context.Context, in *struct {
			ID          string `path:"id"`
			DeleteFiles bool   `query:"deleteFiles"`
		}) (*struct{}, error) {
			if _, err := store.GetUser(s.DB, in.ID); err != nil {
				return nil, pinErr(err)
			}
			return nil, s.deleteUser(in.ID, in.DeleteFiles)
		})

	huma.Register(api, op("devices", "GET", "/v1/devices", "read", "Your signed-in devices"),
		func(ctx context.Context, in *struct {
			Authorization string `header:"Authorization"`
		}) (*struct{ Body []deviceOut }, error) {
			me, err := s.me(ctx)
			if err != nil {
				return nil, err
			}
			_, cfg, err := config.Load(s.CfgPath, nil)
			if err != nil {
				return nil, err
			}
			cur := HashToken(bearer(in.Authorization))
			out := []deviceOut{}
			for _, t := range cfg.Auth.Tokens {
				if n, ok := strings.CutPrefix(t.Name, devicePrefix(me.ID)); ok {
					out = append(out, deviceOut{n, t.Hash == cur})
				}
			}
			return body(out), nil
		})
	huma.Register(api, op("sign-out-device", "DELETE", "/v1/devices/{name}", "read", "Sign out one of your devices (also this one)"),
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
		}) (*struct{}, error) {
			me, err := s.me(ctx)
			if err != nil {
				return nil, err
			}
			full := devicePrefix(me.ID) + in.Name
			return nil, s.revoke(func(n string) bool { return n == full })
		})

	huma.Register(api, op("prefs", "GET", "/v1/prefs", "read", "Your preferences (favorites, watch progress, …) as one JSON object"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body map[string]any }, error) {
			me, err := s.me(ctx)
			if err != nil {
				return nil, err
			}
			p, err := store.Prefs(s.DB, me.ID)
			out := map[string]any{}
			if err == nil {
				err = json.Unmarshal([]byte(p), &out)
			}
			return body(out), err
		})
	type prefIn struct {
		Key string `path:"key" pattern:"^[A-Za-z0-9_-]{1,64}$"`
	}
	huma.Register(api, op("set-pref", "PUT", "/v1/prefs/{key}", "write", "Set one preference to any JSON value (all of them together at most 1 MiB)"),
		func(ctx context.Context, in *struct {
			prefIn
			Body any `required:"true"`
		}) (*struct{}, error) {
			me, err := s.me(ctx)
			if err != nil {
				return nil, err
			}
			v, err := json.Marshal(in.Body)
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			if err := store.SetPref(s.DB, me.ID, in.Key, v); errors.Is(err, store.ErrPrefsFull) {
				return nil, huma.Error413RequestEntityTooLarge(err.Error())
			} else if err != nil {
				return nil, err
			}
			return nil, nil
		})
	huma.Register(api, op("delete-pref", "DELETE", "/v1/prefs/{key}", "write", "Remove one preference"),
		func(ctx context.Context, in *prefIn) (*struct{}, error) {
			me, err := s.me(ctx)
			if err != nil {
				return nil, err
			}
			return nil, store.SetPref(s.DB, me.ID, in.Key, nil)
		})
}

// deleteUser stops and forgets a user's jobs, signs out their devices and removes the user. Their folder
// stays unless deleteFiles.
func (s *server) deleteUser(id string, deleteFiles bool) error {
	js, err := store.ListJobsOf(s.DB, id)
	if err != nil {
		return err
	}
	for _, j := range js {
		if err := s.Jobs.Cancel(j.ID, deleteFiles); err != nil && !errors.Is(err, jobs.ErrNotFound) {
			return err
		}
	}
	if err := s.revoke(func(n string) bool { return strings.HasPrefix(n, devicePrefix(id)) }); err != nil {
		return err
	}
	if err := store.DeleteUser(s.DB, id); err != nil {
		return err
	}
	if err := s.Cookies.User(id).Drop(); err != nil {
		return err
	}
	s.mu.Lock()
	if f := s.userFS[id]; f != nil {
		f.Close()
		delete(s.userFS, id)
	}
	s.mu.Unlock()
	if deleteFiles {
		if err := s.FS.Remove(jobs.UserDir(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// clientAddr is the request's remote host, for the sign-in limiter.
func clientAddr(remote string) string {
	if h, _, err := net.SplitHostPort(remote); err == nil {
		return h
	}
	return remote
}

// progressOf is the live progress of the user's own running jobs (all of them before users exist).
func (s *server) progressOf(user string) []jobs.Progress {
	ps := s.Jobs.Progress()
	if user == "" {
		return ps
	}
	js, _ := store.ListJobsOf(s.DB, user)
	mine := make(map[string]bool, len(js))
	for _, j := range js {
		mine[j.ID] = true
	}
	return slices.DeleteFunc(ps, func(p jobs.Progress) bool { return !mine[p.ID] })
}
