// Package acme gets and renews public certificates for a domain with CertMagic (Caddy's ACME
// library): HTTP-01 and TLS-ALPN-01 when ports 80/443 are reachable, DNS-01 through Cloudflare or
// DuckDNS otherwise (scope §5.10 mode 2). Public certificates appear in transparency logs, so the
// host name becomes discoverable.
package acme

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/cloudflare"
	"github.com/libdns/duckdns"
)

type Options struct {
	Domain      string
	Email       string // for expiry notices from the CA; optional
	DNSProvider string // "", "cloudflare" or "duckdns"
	DNSToken    string
	StorageDir  string // certificates and ACME account
	CA          string // directory URL; "" = Let's Encrypt production
}

// Setup starts managing the certificate in the background (CertMagic retries with backoff and
// renews well before expiry) and returns the TLS config for the HTTPS server plus a handler for
// port 80 that answers HTTP-01 challenges and redirects everything else to HTTPS.
func Setup(ctx context.Context, o Options) (*tls.Config, http.Handler, error) {
	if o.Domain == "" || strings.ContainsAny(o.Domain, "/: ") {
		return nil, nil, fmt.Errorf("server.domain %q: set a plain domain name", o.Domain)
	}
	magic := certmagic.NewDefault()
	magic.Storage = &certmagic.FileStorage{Path: o.StorageDir}
	tmpl := certmagic.ACMEIssuer{CA: o.CA, Email: o.Email, Agreed: true}
	if tmpl.CA == "" {
		tmpl.CA = certmagic.LetsEncryptProductionCA
	}
	switch o.DNSProvider {
	case "":
	case "cloudflare":
		tmpl.DNS01Solver = &certmagic.DNS01Solver{DNSManager: certmagic.DNSManager{DNSProvider: &cloudflare.Provider{APIToken: o.DNSToken}}}
	case "duckdns":
		tmpl.DNS01Solver = &certmagic.DNS01Solver{DNSManager: certmagic.DNSManager{DNSProvider: &duckdns.Provider{APIToken: o.DNSToken}}}
	default:
		return nil, nil, fmt.Errorf("server.dnsprovider %q: use cloudflare or duckdns", o.DNSProvider)
	}
	if o.DNSProvider != "" && o.DNSToken == "" {
		return nil, nil, fmt.Errorf("server.dnstoken is required for %s", o.DNSProvider)
	}
	issuer := certmagic.NewACMEIssuer(magic, tmpl)
	magic.Issuers = []certmagic.Issuer{issuer}
	if err := magic.ManageAsync(ctx, []string{o.Domain}); err != nil {
		return nil, nil, err
	}
	tc := magic.TLSConfig()
	tc.NextProtos = append([]string{"h2", "http/1.1"}, tc.NextProtos...)
	tc.MinVersion = tls.VersionTLS12
	redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://"+o.Domain+r.URL.RequestURI(), http.StatusPermanentRedirect)
	})
	return tc, issuer.HTTPChallengeHandler(redirect), nil
}
