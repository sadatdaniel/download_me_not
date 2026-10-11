// Package secret encrypts small secrets in the config file (provider tokens, client secrets) with
// AES-GCM, so the config can be backed up or shown without leaking them (scope §5.12). The key is a
// 32-byte file in the config folder, mode 0600; the cookie store uses the same key file.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const prefix = "enc:v1:"

type Box struct{ gcm cipher.AEAD }

// Open loads the key at path, creating it on first use.
func Open(path string) (*Box, error) {
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		rand.Read(key)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("secret key %s: want 32 bytes", path)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{gcm: gcm}, nil
}

// Seal encrypts plain; name (e.g. "gdrive.token") is bound in, so values can't be swapped.
func (b *Box) Seal(name, plain string) string {
	nonce := make([]byte, b.gcm.NonceSize())
	rand.Read(nonce)
	return prefix + base64.RawStdEncoding.EncodeToString(b.gcm.Seal(nonce, nonce, []byte(plain), []byte(name)))
}

// Open decrypts a sealed value. Plain values (not yet encrypted) are returned as they are.
func (b *Box) Open(name, v string) (string, error) {
	enc, ok := strings.CutPrefix(v, prefix)
	if !ok {
		return v, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(enc)
	n := b.gcm.NonceSize()
	if err != nil || len(raw) < n {
		return "", errors.New("secret corrupted")
	}
	plain, err := b.gcm.Open(nil, raw[:n], raw[n:], []byte(name))
	if err != nil {
		return "", errors.New("secret cannot be decrypted (wrong key or tampered)")
	}
	return string(plain), nil
}

func IsSealed(v string) bool { return strings.HasPrefix(v, prefix) }
