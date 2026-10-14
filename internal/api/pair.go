package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"

	"github.com/danielgtaylor/huma/v2"
	"github.com/knadh/koanf/v2"
	"rsc.io/qr"

	"github.com/sadatdaniel/getaload/internal/config"
)

// AddToken creates a token, stores only its hash in the config file, and returns it. A token with
// the same name is replaced, so pairing the same device again revokes its old token.
func AddToken(cfgPath, name, scope string) (string, error) {
	if rank[scope] == 0 {
		return "", fmt.Errorf("scope must be read, write or admin")
	}
	b := make([]byte, 32)
	rand.Read(b)
	tok := "getaload_" + base64.RawURLEncoding.EncodeToString(b)
	_, err := editTokens(cfgPath, func(t config.Token) bool { return t.Name == name },
		map[string]any{"name": name, "hash": HashToken(tok), "scope": scope})
	return tok, err
}

// PairPayload is what the pairing QR code carries: where the server is, a fresh token for this
// device and the certificate fingerprint to pin.
func PairPayload(serverURL, token, fingerprint string) string {
	v := url.Values{"v": {"1"}, "url": {serverURL}, "token": {token}}
	if fingerprint != "" {
		v.Set("fp", fingerprint)
	}
	return "getaload://pair?" + v.Encode()
}

type pairOut struct {
	Payload     string `json:"payload" doc:"getaload://pair link for the app (contains the new token: shown once)"`
	QR          string `json:"qr" doc:"the payload as a PNG data: URL"`
	Fingerprint string `json:"fingerprint,omitempty" doc:"SHA-256 of the self-signed certificate"`
}

// pairRoutes lets the owner pair a phone from the web panel, without SSH (same as `getaload pair`).
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
			s.reloadTokens() // the replaced token stops working immediately
			p := PairPayload(in.Body.URL, tok, s.Fingerprint)
			code, err := qr.Encode(p, qr.L)
			if err != nil {
				return nil, err
			}
			return body(pairOut{Payload: p, QR: "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()), Fingerprint: s.Fingerprint}), nil
		})
}

// RemoveTokens deletes the tokens that match from the config file and returns their hashes.
func RemoveTokens(cfgPath string, drop func(config.Token) bool) ([]string, error) {
	return editTokens(cfgPath, drop, nil)
}

// editTokens drops the matching tokens and appends add (if any) in one locked read-change-write of the config
// file, read fresh from disk (the CLI may have added tokens meanwhile). It returns the dropped hashes.
func editTokens(cfgPath string, drop func(config.Token) bool, add map[string]any) ([]string, error) {
	var gone []string
	err := config.Update(cfgPath, func(k *koanf.Koanf) error {
		var ts []config.Token
		if err := k.Unmarshal("auth.tokens", &ts); err != nil {
			return err
		}
		toks := []map[string]any{}
		for _, t := range ts {
			if drop(t) {
				gone = append(gone, t.Hash)
			} else {
				toks = append(toks, map[string]any{"name": t.Name, "hash": t.Hash, "scope": t.Scope})
			}
		}
		if add != nil {
			toks = append(toks, add)
		}
		return k.Set("auth.tokens", toks)
	})
	return gone, err
}
