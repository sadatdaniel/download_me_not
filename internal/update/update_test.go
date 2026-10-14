package update

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The release that installs but never gets healthy: first start counts, second start rolls back.
func TestConfirmAfterBoot(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "getaload")
	os.WriteFile(exe, []byte("new"), 0o755)
	os.WriteFile(exe+".prev", []byte("old"), 0o755)
	u := Updater{DataDir: dir, Exe: exe}
	u.writeMarker(marker{From: "v1.0.0", To: "v1.1.0"})

	if rb, err := u.Boot(); err != nil || rb {
		t.Fatalf("first start: rolledBack=%v err=%v", rb, err)
	}
	// It crashed before Confirm. Next start:
	if rb, err := u.Boot(); err != nil || !rb {
		t.Fatalf("second start should roll back: %v %v", rb, err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("binary is %q", b)
	}
	if rb, _ := u.Boot(); rb {
		t.Fatal("rolled back again with no pending update")
	}
}

func TestConfirmed(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "getaload")
	os.WriteFile(exe, []byte("new"), 0o755)
	os.WriteFile(exe+".prev", []byte("old"), 0o755)
	u := Updater{DataDir: dir, Exe: exe}
	u.writeMarker(marker{From: "v1.0.0", To: "v1.1.0"})
	u.Boot()
	if err := u.Confirm(); err != nil {
		t.Fatal(err)
	}
	if rb, _ := u.Boot(); rb {
		t.Fatal("a confirmed version was rolled back")
	}
	if err := u.Rollback(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatal("manual rollback failed")
	}
	if len(PublicKey) == 0 {
		t.Fatal("no release public key embedded")
	}
}

// CI signs checksums.txt with `openssl dgst -sha256 -sign`; that signature must verify here.
func TestOpenSSLSignatureVerifies(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not installed")
	}
	dir := t.TempDir()
	key, pub, sums := filepath.Join(dir, "k.pem"), filepath.Join(dir, "p.pem"), filepath.Join(dir, "checksums.txt")
	os.WriteFile(sums, []byte("abc123  getaload_1.0.0_linux_amd64.tar.gz\n"), 0o644)
	for _, args := range [][]string{
		{"ecparam", "-name", "prime256v1", "-genkey", "-noout", "-out", key},
		{"ec", "-in", key, "-pubout", "-out", pub},
		{"dgst", "-sha256", "-sign", key, "-out", sums + ".sig", sums},
	} {
		if out, err := exec.Command("openssl", args...).CombinedOutput(); err != nil {
			t.Fatalf("openssl %v: %v %s", args, err, out)
		}
	}
	pubPEM, _ := os.ReadFile(pub)
	data, _ := os.ReadFile(sums)
	sig, _ := os.ReadFile(sums + ".sig")
	v := validator(pubPEM)
	if err := v.Validate("checksums.txt", data, sig); err != nil {
		t.Fatalf("CI signature rejected: %v", err)
	}
	if err := v.Validate("checksums.txt", append(data, 'x'), sig); err == nil {
		t.Fatal("tampered checksums accepted")
	}
	if err := validator(PublicKey).Validate("checksums.txt", data, sig); err == nil {
		t.Fatal("a signature by another key was accepted")
	}
	if b, _ := pem.Decode(PublicKey); b == nil {
		t.Fatal("embedded key is not PEM")
	} else if _, err := x509.ParsePKIXPublicKey(b.Bytes); err != nil {
		t.Fatalf("embedded key: %v", err)
	}
}

// install.sh verifies releases with its own copy of the public key; it must match.
func TestInstallScriptKey(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSpace(strings.ReplaceAll(string(PublicKey), "\r", ""))
	if !strings.Contains(string(b), "PUBKEY='"+key+"'") {
		t.Fatal("scripts/install.sh has a different release key than internal/update/release-key.pub")
	}
}
