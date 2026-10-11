package gdrive

// A fake Google OAuth server: device code, one "authorization_pending" poll, then a token.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func fakeGoogle(t *testing.T, deny bool) *httptest.Server {
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/device/code", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("scope") != Scope || r.Form.Get("client_id") != "cid" {
			http.Error(w, "bad request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Google says verification_url (not _uri); x/oauth2 must handle it.
		w.Write([]byte(`{"device_code":"dev","user_code":"ABCD-EFGH","verification_url":"https://www.google.com/device","expires_in":60,"interval":1}`))
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("client_secret") != "csecret" || r.Form.Get("device_code") != "dev" {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		if polls.Add(1) == 1 {
			w.WriteHeader(428)
			w.Write([]byte(`{"error":"authorization_pending"}`))
			return
		}
		if deny {
			w.WriteHeader(403)
			w.Write([]byte(`{"error":"access_denied"}`))
			return
		}
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":3600}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func client(srv *httptest.Server) Client {
	return Client{ID: "cid", Secret: "csecret", Endpoint: oauth2.Endpoint{
		DeviceAuthURL: srv.URL + "/device/code", TokenURL: srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}}
}

func TestDeviceFlow(t *testing.T) {
	srv := fakeGoogle(t, false)
	var stored string
	s, err := Start(context.Background(), client(srv), func(tok string) error { stored = tok; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if s.UserCode != "ABCD-EFGH" || s.VerificationURL != "https://www.google.com/device" || s.Status().State != "pending" {
		t.Fatalf("%+v", s)
	}
	select {
	case <-s.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("never finished")
	}
	var tok oauth2.Token
	if s.Status().State != "done" || json.Unmarshal([]byte(stored), &tok) != nil || tok.RefreshToken != "rt" {
		t.Fatalf("status %+v, stored %q", s.Status(), stored)
	}
	if p := Remote(client(srv), stored, ""); p["scope"] != "drive.file" || p["type"] != "drive" || !strings.Contains(p["token"], "rt") {
		t.Fatalf("remote %v", p)
	}
}

func TestDeniedAndUnconfigured(t *testing.T) {
	srv := fakeGoogle(t, true)
	s, err := Start(context.Background(), client(srv), func(string) error { t.Fatal("token stored after denial"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	<-s.Done()
	if st := s.Status(); st.State != "error" || !strings.Contains(st.Error, "access_denied") {
		t.Fatalf("%+v", st)
	}
	if _, err := Start(context.Background(), Client{}, nil); err == nil {
		t.Fatal("started without a client ID")
	}
}
