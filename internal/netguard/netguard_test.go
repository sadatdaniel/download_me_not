package netguard

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestSafe(t *testing.T) {
	blocked := []string{
		"127.0.0.1:80", "10.0.0.1:443", "172.16.0.1:80", "192.168.1.1:80", "169.254.169.254:80",
		"100.64.0.1:80", "0.0.0.0:80", "[::1]:80", "[fe80::1]:80", "[fd00::1]:80", "[::ffff:127.0.0.1]:80",
	}
	for _, a := range blocked {
		nw := "tcp4"
		if a[0] == '[' {
			nw = "tcp6"
		}
		if Safe(nw, a, nil) == nil {
			t.Errorf("%s allowed", a)
		}
	}
	for _, a := range []string{"93.184.215.14:443", "1.1.1.1:21", "8.8.8.8:6881"} {
		if err := Safe("tcp4", a, nil); err != nil {
			t.Errorf("%s blocked: %v", a, err)
		}
	}
}

func TestClientRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("internal")) }))
	defer srv.Close()
	if _, err := Client().Get(srv.URL); err == nil {
		t.Fatal("guarded client reached a loopback server")
	}
}

func TestProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) }))
	defer target.Close()

	// Guarded proxy: CONNECT and plain requests to loopback are refused.
	guarded := httptest.NewServer(NewProxy())
	defer guarded.Close()
	if code := connect(t, guarded.URL, target.Listener.Addr().String()); code != http.StatusForbidden {
		t.Fatalf("guarded CONNECT got %d", code)
	}
	pu, _ := url.Parse(guarded.URL)
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	if resp, err := c.Get(target.URL); err == nil && resp.StatusCode == 200 {
		t.Fatal("guarded proxy fetched a loopback URL")
	}

	// Same proxy with an unguarded dialer proves the forwarding itself works (no public network offline).
	open := httptest.NewServer(&Proxy{Dial: (&net.Dialer{}).DialContext})
	defer open.Close()
	pu, _ = url.Parse(open.URL)
	c = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := c.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "hi" {
		t.Fatalf("plain proxy got %q", b)
	}
	if code := connect(t, open.URL, target.Listener.Addr().String()); code != 200 {
		t.Fatalf("CONNECT got %d", code)
	}
}

func connect(t *testing.T, proxyURL, host string) int {
	t.Helper()
	u, _ := url.Parse(proxyURL)
	c, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", host, host)
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}
