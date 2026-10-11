package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"

	"github.com/danielgtaylor/huma/v2"
	"rsc.io/qr"

	"github.com/sadatdaniel/download_me_not/internal/config"
)

// AddToken creates a token, stores only its hash in the config file, and returns it. A token with
// the same name is replaced, so pairing the same device again revokes its old token.
func AddToken(cfgPath, name, scope string) (string, error) {
	if rank[scope] == 0 {
		return "", fmt.Errorf("scope must be read, write or admin")
	}
	_, cfg, err := config.Load(cfgPath, nil) // fresh from disk: the CLI may have added tokens meanwhile
	if err != nil {
		return "", err
	}
	b := make([]byte, 32)
	rand.Read(b)
	tok := "dmn_" + base64.RawURLEncoding.EncodeToString(b)
	toks := []map[string]any{}
	for _, t := range cfg.Auth.Tokens {
		if t.Name != name {
			toks = append(toks, map[string]any{"name": t.Name, "hash": t.Hash, "scope": t.Scope})
		}
	}
	toks = append(toks, map[string]any{"name": name, "hash": HashToken(tok), "scope": scope})
	return tok, config.Set(cfgPath, "auth.tokens", toks)
}

// PairPayload is what the pairing QR code carries: where the server is, a fresh token for this
// device and the certificate fingerprint to pin.
func PairPayload(serverURL, token, fingerprint string) string {
	v := url.Values{"v": {"1"}, "url": {serverURL}, "token": {token}}
	if fingerprint != "" {
		v.Set("fp", fingerprint)
	}
	return "dmn://pair?" + v.Encode()
}

type pairOut struct {
	Payload     string `json:"payload" doc:"dmn://pair link for the app (contains the new token: shown once)"`
	QR          string `json:"qr" doc:"the payload as a PNG data: URL"`
	Fingerprint string `json:"fingerprint,omitempty" doc:"SHA-256 of the self-signed certificate"`
}

// pairRoutes lets the owner pair a phone from the web panel, without SSH (same as `dmn pair`).
func (s *server) pairRoutes(api huma.API) {
	huma.Register(api, op("pair", "POST", "/v1/pair", "admin", "Create a device token and its pairing QR code"),
		func(ctx context.Context, in *struct {
			Body struct {
				Name  string `json:"name" minLength:"1" maxLength:"64" doc:"device name; pairing again with the same name replaces its token"`
				Scope string `json:"scope,omitempty" enum:"read,write,admin" default:"write"`
				URL   string `json:"url" format:"uri" doc:"server URL as the phone sees it (the web panel sends its own origin)"`
			}
		}) (*struct{ Body pairOut }, error) {
			if u, err := url.Parse(in.Body.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return nil, huma.Error400BadRequest("url must be http(s)://host[:port]")
			}
			tok, err := AddToken(s.CfgPath, in.Body.Name, in.Body.Scope)
			if err != nil {
				return nil, err
			}
			s.mu.Lock() // take the new list now: the replaced token stops working immediately
			if fi, err := os.Stat(s.CfgPath); err == nil {
				if _, c, err := config.Load(s.CfgPath, nil); err == nil {
					s.cfgMtime = fi.ModTime()
					s.setTokens(c.Auth.Tokens)
				}
			}
			s.mu.Unlock()
			p := PairPayload(in.Body.URL, tok, s.Fingerprint)
			code, err := qr.Encode(p, qr.L)
			if err != nil {
				return nil, err
			}
			return body(pairOut{Payload: p, QR: "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()), Fingerprint: s.Fingerprint}), nil
		})
}
