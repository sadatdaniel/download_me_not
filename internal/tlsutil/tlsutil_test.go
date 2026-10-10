package tlsutil

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSelfSignedPinned(t *testing.T) {
	dir := t.TempDir()
	c, err := SelfSigned(dir)
	if err != nil {
		t.Fatal(err)
	}
	fp, _ := Fingerprint(c)
	c2, _ := SelfSigned(dir) // second call loads the same files
	if fp2, _ := Fingerprint(c2); fp2 != fp {
		t.Fatal("certificate changed on reload")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "key.pem")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v", fi.Mode().Perm())
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{c}}
	srv.StartTLS()
	defer srv.Close()
	// What the app does: skip CA checks, accept only the pinned fingerprint.
	pinned := func(want string) *http.Client {
		return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true,
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				sum := sha256.Sum256(raw[0])
				if hex.EncodeToString(sum[:]) != want {
					return errors.New("fingerprint mismatch")
				}
				return nil
			}}}}
	}
	if resp, err := pinned(fp).Get(srv.URL); err != nil || resp.StatusCode != 200 {
		t.Fatalf("pinned client refused the right cert: %v", err)
	}
	if _, err := pinned("00" + fp[2:]).Get(srv.URL); err == nil {
		t.Fatal("pinned client accepted a different fingerprint")
	}
}
