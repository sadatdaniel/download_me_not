// Package update replaces the server binary with a newer signed release and rolls back a release
// that installs but does not start (scope §5.8).
//
// go-selfupdate (MIT) finds the latest GitHub release, checks the binary against checksums.txt and
// checksums.txt against its ECDSA signature (made in CI with a key kept as a GitHub secret), and
// replaces the executable. The only custom part is "confirm after boot", the A/B pattern of OS
// updaters: keep the previous binary, write a pending marker, clear it once the new version is
// healthy, and revert on the next start if it is still there.
package update

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	_ "embed"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/creativeprojects/go-selfupdate"
)

// PublicKey verifies release signatures. The private half exists only as a CI secret.
//
//go:embed release-key.pub
var PublicKey []byte

type Updater struct {
	Repo    string // owner/name
	Token   string // GitHub token, needed while the repo is private
	DataDir string // holds the pending marker
	Exe     string // the running binary; default os.Executable
	Source  selfupdate.Source
}

func (u Updater) updater() (*selfupdate.Updater, error) {
	src := u.Source
	if src == nil {
		var err error
		if src, err = selfupdate.NewGitHubSource(selfupdate.GitHubConfig{APIToken: u.Token}); err != nil {
			return nil, err
		}
	}
	return selfupdate.NewUpdater(selfupdate.Config{
		Source:    src,
		Validator: validator(PublicKey),
	})
}

type Check struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	Notes     string `json:"notes,omitempty"`
}

func (u Updater) Check(ctx context.Context, current string) (Check, error) {
	up, err := u.updater()
	if err != nil {
		return Check{}, err
	}
	rel, found, err := up.DetectLatest(ctx, selfupdate.ParseSlug(u.Repo))
	if err != nil || !found {
		return Check{Current: current}, err
	}
	return Check{Current: current, Latest: rel.Version(), Available: current == "dev" || rel.GreaterThan(current), Notes: rel.ReleaseNotes}, nil
}

type marker struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Attempts int    `json:"attempts"`
}

func (u Updater) markerPath() string { return filepath.Join(u.DataDir, "update-pending.json") }

func (u Updater) exe() (string, error) {
	if u.Exe != "" {
		return u.Exe, nil
	}
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

// Apply installs the latest release if it is newer. The caller then restarts the process (exit
// cleanly; systemd or Docker starts the new binary).
func (u Updater) Apply(ctx context.Context, current string) (string, error) {
	if os.Getenv("DMN_IMAGE") != "" { // set by our Docker image; a native install inside another container may update itself
		return "", errors.New("running in Docker: update by pulling the new image (docker compose pull && docker compose up -d)")
	}
	up, err := u.updater()
	if err != nil {
		return "", err
	}
	rel, found, err := up.DetectLatest(ctx, selfupdate.ParseSlug(u.Repo))
	if err != nil {
		return "", err
	}
	if !found || (current != "dev" && !rel.GreaterThan(current)) {
		return current, nil
	}
	exe, err := u.exe()
	if err != nil {
		return "", err
	}
	if err := copyFile(exe, exe+".prev"); err != nil {
		return "", fmt.Errorf("keeping the current binary: %w", err)
	}
	if err := u.writeMarker(marker{From: current, To: rel.Version()}); err != nil {
		return "", err
	}
	if err := up.UpdateTo(ctx, rel, exe); err != nil {
		os.Remove(u.markerPath()) // nothing changed; go-selfupdate restores on a failed replace
		return "", err
	}
	return rel.Version(), nil
}

func (u Updater) writeMarker(m marker) error {
	b, _ := json.Marshal(m)
	return os.WriteFile(u.markerPath(), b, 0o600)
}

// Boot runs at every start. If a pending update already had one start without being confirmed,
// it restores the previous binary and returns rolledBack=true: the caller must exit so the
// supervisor starts the old version. Otherwise it counts this start; call Confirm once healthy.
func (u Updater) Boot() (rolledBack bool, err error) {
	b, err := os.ReadFile(u.markerPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var m marker
	if json.Unmarshal(b, &m) != nil || m.Attempts >= 1 {
		exe, err := u.exe()
		if err != nil {
			return false, err
		}
		if err := os.Rename(exe+".prev", exe); err != nil {
			return false, fmt.Errorf("rollback to %s failed: %w", m.From, err)
		}
		os.Remove(u.markerPath())
		return true, nil
	}
	m.Attempts++
	return false, u.writeMarker(m)
}

// Confirm marks the running version as good.
func (u Updater) Confirm() error {
	err := os.Remove(u.markerPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Rollback restores the previous binary by hand (`dmn update rollback`).
func (u Updater) Rollback() error {
	exe, err := u.exe()
	if err != nil {
		return err
	}
	if _, err := os.Stat(exe + ".prev"); err != nil {
		return errors.New("no previous version kept")
	}
	os.Remove(u.markerPath())
	return os.Rename(exe+".prev", exe)
}

// ConfirmWhenHealthy waits until healthy() passes, then confirms. A version that crashes or never
// gets healthy stays unconfirmed and is rolled back on the next start.
func (u Updater) ConfirmWhenHealthy(ctx context.Context, healthy func() error) {
	if _, err := os.Stat(u.markerPath()); err != nil {
		return
	}
	for i := 0; i < 30; i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		if healthy() == nil {
			u.Confirm()
			return
		}
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// validator checks each asset against checksums.txt and checksums.txt against its ECDSA signature.
// (go-selfupdate's helper wants a certificate; we ship a bare public key, so build it here.)
func validator(pubPEM []byte) selfupdate.Validator {
	ecdsaV := &selfupdate.ECDSAValidator{}
	if block, _ := pem.Decode(pubPEM); block != nil {
		if k, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
			ecdsaV.PublicKey, _ = k.(*ecdsa.PublicKey) // nil key: every signature fails, nothing installs
		}
	}
	return new(selfupdate.PatternValidator).
		Add("checksums.txt", ecdsaV).
		Add("*", &selfupdate.ChecksumValidator{UniqueFilename: "checksums.txt"}).
		SkipValidation("*.sig")
}
