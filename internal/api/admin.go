package api

import (
	"context"
	"errors"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/knadh/koanf/v2"

	"github.com/sadatdaniel/getaload/internal/config"
	"github.com/sadatdaniel/getaload/internal/store"
)

// Server control (scope §17): a sign-in with name + PIN never controls the server. That takes an admin key, which
// a device gets by typing a one-time code from `getaload admin` (run on the server). Admin keys carry no user, so
// they reach server-wide routes only, never anyone's files.

// adminPrefix names admin keys in the config file: "admin:<device name>".
const adminPrefix = "admin:"

// DowngradeSignIns turns sign-in tokens that still carry admin rights (the owner's, before admin keys) into
// plain user tokens. Run at server start; a no-op once done.
func DowngradeSignIns(cfgPath string) error {
	errNone := errors.New("nothing to change") // skips the write: most starts have nothing to do
	err := config.Update(cfgPath, func(k *koanf.Koanf) error {
		var ts []config.Token
		if err := k.Unmarshal("auth.tokens", &ts); err != nil {
			return err
		}
		toks, changed := []map[string]any{}, false
		for _, t := range ts {
			if strings.HasPrefix(t.Name, "u:") && t.Scope == "admin" {
				t.Scope, changed = "write", true
			}
			toks = append(toks, map[string]any{"name": t.Name, "hash": t.Hash, "scope": t.Scope})
		}
		if !changed {
			return errNone
		}
		return k.Set("auth.tokens", toks)
	})
	if errors.Is(err, errNone) {
		return nil
	}
	return err
}

type adminDevice struct {
	Name    string `json:"name"`
	Current bool   `json:"current,omitempty" doc:"the device making this request"`
}

func (s *server) adminRoutes(api huma.API) {
	huma.Register(api, op("admin-claim", "POST", "/v1/admin/claim", "", "Trade a one-time code from `getaload admin` for this device's admin key"),
		func(ctx context.Context, in *struct {
			Body struct {
				Code   string `json:"code" minLength:"8" maxLength:"12"`
				Device string `json:"device" minLength:"1" maxLength:"64" doc:"device name; claiming again with it replaces its key"`
			}
		}) (*struct{ Body map[string]string }, error) {
			addr, _ := ctx.Value(addrKey{}).(string)
			if !s.loginAllowed(addr) { // the same limit as sign-ins: guessing codes is slow
				return nil, huma.Error429TooManyRequests("too many tries; wait a little")
			}
			if err := store.UseAdminCode(s.DB, in.Body.Code); errors.Is(err, store.ErrBadCode) {
				return nil, huma.Error403Forbidden(err.Error())
			} else if err != nil {
				return nil, err
			}
			tok, err := AddToken(s.CfgPath, adminPrefix+strings.TrimSpace(in.Body.Device), "admin")
			if err != nil {
				return nil, err
			}
			s.reloadTokens()
			return body(map[string]string{"token": tok}), nil
		})

	huma.Register(api, op("admin-devices", "GET", "/v1/admin/devices", "admin", "Devices with an admin key"),
		func(ctx context.Context, in *struct {
			Authorization string `header:"Authorization"`
		}) (*struct{ Body []adminDevice }, error) {
			_, cfg, err := config.Load(s.CfgPath, nil)
			if err != nil {
				return nil, err
			}
			cur := HashToken(bearer(in.Authorization))
			out := []adminDevice{}
			for _, t := range cfg.Auth.Tokens {
				if n, ok := strings.CutPrefix(t.Name, adminPrefix); ok {
					out = append(out, adminDevice{n, t.Hash == cur})
				}
			}
			return body(out), nil
		})
	huma.Register(api, op("admin-device-remove", "DELETE", "/v1/admin/devices/{name}", "admin", "Take the admin key from a device (also this one)"),
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
		}) (*struct{}, error) {
			return nil, s.revoke(func(n string) bool { return n == adminPrefix+in.Name })
		})

	huma.Register(api, op("admin-reset-pin", "PUT", "/v1/admin/users/{id}/pin", "admin", "Set anyone's PIN (also unlocks them)"),
		func(ctx context.Context, in *struct {
			ID   string `path:"id"`
			Body struct {
				PIN string `json:"pin" minLength:"4" maxLength:"4"`
			}
		}) (*struct{}, error) {
			if err := store.SetPIN(s.DB, in.ID, in.Body.PIN); err != nil {
				return nil, pinErr(err)
			}
			return nil, nil
		})
}
