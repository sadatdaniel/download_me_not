package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/sadatdaniel/download_me_not/internal/config"
	"github.com/sadatdaniel/download_me_not/internal/gdrive"
)

// signIns holds the current Google Drive sign-in attempt (one at a time is plenty for one user).
type signIns struct {
	mu  sync.Mutex
	cur *gdrive.SignIn
}

type settingsOut struct {
	Body struct {
		Values  map[string]any `json:"values" doc:"secrets show only true/false (set or not)"`
		Restart []string       `json:"restart,omitempty" doc:"changed settings that apply after a restart"`
	}
}

func (s *server) providerRoutes(api huma.API) {
	huma.Register(api, op("provider-settings", "GET", "/v1/providers/{name}/settings", "admin", "A provider's settings (secrets are never returned)"),
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
		}) (*settingsOut, error) {
			v, err := s.Providers.View(in.Name)
			if err != nil {
				return nil, huma.Error404NotFound(err.Error())
			}
			out := &settingsOut{}
			out.Body.Values = v
			return out, nil
		})
	huma.Register(api, op("set-provider-settings", "PUT", "/v1/providers/{name}/settings", "admin",
		"Change some of a provider's settings; secrets are encrypted before they are saved"),
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
			Body map[string]any
		}) (*settingsOut, error) {
			// Validate everything first, so a bad field changes nothing.
			secrets := map[string]bool{}
			for k, v := range in.Body {
				sec, err := s.Providers.Check(in.Name, k, v)
				if err != nil {
					return nil, huma.Error422UnprocessableEntity(err.Error())
				}
				secrets[k] = sec
			}
			out := &settingsOut{}
			for k, v := range in.Body {
				if secrets[k] {
					v = s.Secret.Seal(in.Name+"."+k, v.(string))
				}
				if err := config.Set(s.CfgPath, "providers."+in.Name+".settings."+k, v); err != nil {
					return nil, err
				}
				s.Providers.SetSetting(in.Name, k, v)
				if s.Providers.Restart(in.Name, k) {
					out.Body.Restart = append(out.Body.Restart, k)
				}
			}
			out.Body.Values, _ = s.Providers.View(in.Name)
			return out, nil
		})

	huma.Register(api, op("gdrive-signin", "POST", "/v1/providers/gdrive/signin", "admin",
		"Connect Google Drive: returns a code to enter at the verification URL on any device"),
		func(ctx context.Context, _ *struct{}) (*struct {
			Status int
			Body   *gdrive.SignIn
		}, error) {
			id, _ := s.Providers.Setting("gdrive", "clientId").(string)
			sealed, _ := s.Providers.Setting("gdrive", "clientSecret").(string)
			secret, err := s.Secret.Open("gdrive.clientSecret", sealed)
			if err != nil {
				return nil, err
			}
			ep := gdrive.GoogleEndpoint
			if s.GoogleEndpoint != nil {
				ep = *s.GoogleEndpoint
			}
			sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			si, err := gdrive.Start(sctx, gdrive.Client{ID: id, Secret: secret, Endpoint: ep}, func(tok string) error {
				v := s.Secret.Seal("gdrive.token", tok)
				if err := config.Set(s.CfgPath, "providers.gdrive.settings.token", v); err != nil {
					return err
				}
				s.Providers.SetSetting("gdrive", "token", v)
				return nil
			})
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			s.signin.mu.Lock()
			s.signin.cur = si
			s.signin.mu.Unlock()
			return &struct {
				Status int
				Body   *gdrive.SignIn
			}{http.StatusAccepted, si}, nil
		})
	huma.Register(api, op("gdrive-signin-status", "GET", "/v1/providers/gdrive/signin", "admin", "State of the current Google Drive sign-in"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body gdrive.Status }, error) {
			s.signin.mu.Lock()
			cur := s.signin.cur
			s.signin.mu.Unlock()
			if cur == nil {
				return nil, huma.Error404NotFound("no sign-in started")
			}
			return body(cur.Status()), nil
		})
}
