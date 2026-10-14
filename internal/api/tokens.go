package api

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/knadh/koanf/v2"

	"github.com/sadatdaniel/getaload/internal/config"
)

// AddToken creates a token, stores only its hash in the config file, and returns it. A token with
// the same name is replaced, so signing in again on the same device revokes its old token.
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
