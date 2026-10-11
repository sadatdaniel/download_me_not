// Package gdrive signs in to Google Drive with the OAuth device flow and describes the rclone
// remote that uses the token. The device flow needs no redirect URL, so it works for any
// self-hosted address: the web UI or CLI shows a code, the user approves on google.com/device.
//
// Scope drive.file: the app sees only files it created. It is a non-sensitive scope (no paid
// security review) and is all "copy my downloads to Drive" needs (scope §19).
package gdrive

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const Scope = "https://www.googleapis.com/auth/drive.file"

var GoogleEndpoint = oauth2.Endpoint{
	AuthURL:       "https://accounts.google.com/o/oauth2/auth",
	TokenURL:      "https://oauth2.googleapis.com/token",
	DeviceAuthURL: "https://oauth2.googleapis.com/device/code",
	AuthStyle:     oauth2.AuthStyleInParams,
}

type Client struct {
	ID, Secret string
	Endpoint   oauth2.Endpoint // GoogleEndpoint in production; a fake server in tests
}

func (c Client) config() *oauth2.Config {
	return &oauth2.Config{ClientID: c.ID, ClientSecret: c.Secret, Endpoint: c.Endpoint, Scopes: []string{Scope}}
}

// SignIn is one device-flow attempt: show UserCode and VerificationURL, then wait for approval.
type SignIn struct {
	UserCode        string    `json:"userCode"`
	VerificationURL string    `json:"verificationUrl"`
	Expires         time.Time `json:"expires"`

	mu    sync.Mutex
	state string // pending, done, error
	err   error
	done  chan struct{}
}

type Status struct {
	State string `json:"state"` // pending, done, error
	Error string `json:"error,omitempty"`
}

func (s *SignIn) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{State: s.state}
	if s.err != nil {
		st.Error = s.err.Error()
	}
	return st
}

// Done is closed when the attempt finishes (approved, denied or expired).
func (s *SignIn) Done() <-chan struct{} { return s.done }

// Start asks Google for a device code and polls in the background until the user approves.
// onToken receives the token JSON to store (encrypted) for rclone.
func Start(ctx context.Context, c Client, onToken func(tokenJSON string) error) (*SignIn, error) {
	if c.ID == "" {
		return nil, errors.New("no Google OAuth client ID configured (gdrive settings: clientId, clientSecret)")
	}
	conf := c.config()
	da, err := conf.DeviceAuth(ctx)
	if err != nil {
		return nil, err
	}
	url := da.VerificationURI
	if da.VerificationURIComplete != "" {
		url = da.VerificationURIComplete
	}
	s := &SignIn{UserCode: da.UserCode, VerificationURL: url, Expires: da.Expiry, state: "pending", done: make(chan struct{})}
	go func() {
		defer close(s.done)
		pctx, cancel := context.WithDeadline(context.Background(), da.Expiry)
		defer cancel()
		tok, err := conf.DeviceAccessToken(pctx, da)
		if err == nil {
			var b []byte
			b, err = json.Marshal(tok) // rclone's token format is oauth2.Token as JSON
			if err == nil {
				err = onToken(string(b))
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil {
			s.state, s.err = "error", err
			return
		}
		s.state = "done"
	}()
	return s, nil
}

// Remote returns rclone remote parameters for Drive with the stored token.
func Remote(c Client, tokenJSON, rootFolderID string) map[string]string {
	p := map[string]string{"type": "drive", "scope": "drive.file", "client_id": c.ID, "client_secret": c.Secret, "token": tokenJSON}
	if rootFolderID != "" {
		p["root_folder_id"] = rootFolderID
	}
	return p
}
