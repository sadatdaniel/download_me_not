package acme

// Offline: the CA points at a closed local port, so nothing is requested from Let's Encrypt.
// ponytail: a full issuance test against Pebble (Let's Encrypt's test CA) in an isolated Docker
// network is still to do; CertMagic itself is tested upstream and runs every Caddy server.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestSetup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Not t.TempDir: certmagic's background maintenance may still write here after the test, which made the
	// strict TempDir cleanup fail now and then. Best-effort removal instead.
	dir, err := os.MkdirTemp("", "acme-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	tc, h, err := Setup(ctx, Options{Domain: "dl.example.com", StorageDir: dir, CA: "https://127.0.0.1:1/directory"})
	if err != nil {
		t.Fatal(err)
	}
	if tc.GetCertificate == nil || tc.NextProtos[0] != "h2" {
		t.Fatalf("tls config: %+v", tc.NextProtos)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://dl.example.com/v1/health?x=1", nil))
	if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != "https://dl.example.com/v1/health?x=1" {
		t.Fatalf("redirect: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	for _, o := range []Options{
		{Domain: "", StorageDir: t.TempDir()},
		{Domain: "https://x.com", StorageDir: t.TempDir()},
		{Domain: "x.com", DNSProvider: "route53", StorageDir: t.TempDir()},
		{Domain: "x.com", DNSProvider: "duckdns", StorageDir: t.TempDir()}, // no token
	} {
		if _, _, err := Setup(ctx, o); err == nil {
			t.Errorf("accepted %+v", o)
		}
	}
}
