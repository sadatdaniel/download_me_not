package api

import (
	"context"
	"database/sql"

	"github.com/danielgtaylor/huma/v2"

	"github.com/sadatdaniel/download_me_not/internal/config"
	"github.com/sadatdaniel/download_me_not/internal/provider"
	"github.com/sadatdaniel/download_me_not/internal/secret"
	"github.com/sadatdaniel/download_me_not/internal/store"
)

// Google Drive per user: the app's Google registration (client ID) is the server's, but each user connects their
// own Drive; the token is sealed and stored for that user only. Before users existed it lived in the gdrive
// provider settings, which is still where the user "" keeps it.

// DriveTokens reads and saves Drive tokens.
type DriveTokens struct {
	DB      *sql.DB
	Box     *secret.Box
	Reg     *provider.Registry
	CfgPath string
}

func driveSealName(user string) string { return "gdrive.token/" + user } // binds a sealed token to its user

// Get returns the user's Drive token ("" = not connected).
func (d DriveTokens) Get(user string) (string, error) {
	if user == "" {
		v, _ := d.Reg.Setting("gdrive", "token").(string)
		return d.Box.Open("gdrive.token", v)
	}
	v, err := store.UserSecret(d.DB, user, "gdrive.token")
	if err != nil || v == "" {
		return "", err
	}
	return d.Box.Open(driveSealName(user), v)
}

// Set saves (or with "" forgets) the user's Drive token.
func (d DriveTokens) Set(user, tok string) error {
	if user == "" {
		v := ""
		if tok != "" {
			v = d.Box.Seal("gdrive.token", tok)
		}
		d.Reg.SetSetting("gdrive", "token", v)
		return config.Set(d.CfgPath, "providers.gdrive.settings.token", v)
	}
	if tok == "" {
		return store.SetUserSecret(d.DB, user, "gdrive.token", "")
	}
	return store.SetUserSecret(d.DB, user, "gdrive.token", d.Box.Seal(driveSealName(user), tok))
}

func (s *server) drive() DriveTokens {
	return DriveTokens{DB: s.DB, Box: s.Secret, Reg: s.Providers, CfgPath: s.CfgPath}
}

type driveOut struct {
	Connected  bool   `json:"connected" doc:"your own Google Drive is connected"`
	Configured bool   `json:"configured" doc:"the server has a Google client ID (needed to connect)"`
	Status     string `json:"status" doc:"the gdrive provider: ready, disabled, missing-tool, ..."`
	Reason     string `json:"reason,omitempty"`
}

func (s *server) driveRoutes(api huma.API) {
	huma.Register(api, op("my-drive", "GET", "/v1/me/drive", "read", "Whether your own Google Drive is connected"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body driveOut }, error) {
			tok, err := s.drive().Get(userOf(ctx))
			if err != nil {
				return nil, err
			}
			out := driveOut{Connected: tok != ""}
			id, _ := s.Providers.Setting("gdrive", "clientId").(string)
			out.Configured = id != ""
			for _, p := range s.Providers.List() {
				if p.Name == "gdrive" {
					out.Status, out.Reason = string(p.Status), p.Reason
				}
			}
			return body(out), nil
		})
	huma.Register(api, op("my-drive-disconnect", "DELETE", "/v1/me/drive", "write", "Disconnect your Google Drive (copies to it stop)"),
		func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			return nil, s.drive().Set(userOf(ctx), "")
		})
}
