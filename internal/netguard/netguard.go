// Package netguard blocks SSRF: every outgoing connection for a user-supplied URL is
// checked at connect time (after DNS, so rebinding is covered) against private,
// loopback, link-local and other internal ranges (scope §4.5).
package netguard

import (
	"context"
	"io"
	"net"
	"net/http"
	"time"

	"code.dny.dev/ssrf"
)

var guard = ssrf.New(ssrf.WithAnyPort(), ssrf.WithNetworks("tcp4", "tcp6", "udp4", "udp6"))

// Safe is the net.Dialer Control hook; exported for the torrent engine's dialers.
var Safe = guard.Safe

func Dialer() *net.Dialer {
	return &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: guard.Safe}
}

// Client is the HTTP client for fetching user-supplied URLs. Ignores proxy env vars on purpose.
func Client() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DialContext = Dialer().DialContext
	return &http.Client{Transport: t}
}

// Proxy is a forward HTTP/CONNECT proxy for helper tools that make their own
// connections (yt-dlp --proxy http://127.0.0.1:PORT). Every upstream dial goes through the guard.
// ponytail: listen on loopback only; any local process may use it, but it can only reach public addresses.
type Proxy struct {
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

func NewProxy() *Proxy { return &Proxy{Dial: Dialer().DialContext} }

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if !r.URL.IsAbs() {
		http.Error(w, "proxy needs an absolute URL", http.StatusBadRequest)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Connection")
	out.Header.Del("Proxy-Authorization")
	t := &http.Transport{DialContext: p.Dial, Proxy: nil}
	resp, err := t.RoundTrip(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	up, err := p.Dial(r.Context(), "tcp", r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	down, buf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	down.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	go func() {
		io.Copy(up, buf) // buf holds anything the client sent after the CONNECT line
		up.Close()
	}()
	io.Copy(down, up)
	down.Close()
}
