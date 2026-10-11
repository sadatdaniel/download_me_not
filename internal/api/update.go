package api

import (
	"context"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/sadatdaniel/download_me_not/internal/update"
)

// Updates is the server-update state shared with the daily checker.
type Updates struct {
	Updater update.Updater
	Version string
	Restart func() // exit cleanly so the supervisor starts the new binary

	mu   sync.Mutex
	last update.Check
}

func (u *Updates) Check(ctx context.Context) (update.Check, error) {
	c, err := u.Updater.Check(ctx, u.Version)
	if err == nil {
		u.mu.Lock()
		u.last = c
		u.mu.Unlock()
	}
	return c, err
}

func (u *Updates) Last() update.Check {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.last
}

func (s *server) updateRoutes(api huma.API) {
	if s.Updates == nil {
		return
	}
	huma.Register(api, op("update-check", "GET", "/v1/update", "read", "Server version and whether a newer signed release exists"),
		func(ctx context.Context, in *struct {
			Refresh bool `query:"refresh" doc:"ask GitHub now instead of using the daily check"`
		}) (*struct{ Body update.Check }, error) {
			if !in.Refresh {
				if c := s.Updates.Last(); c.Current != "" {
					return body(c), nil
				}
			}
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			c, err := s.Updates.Check(ctx)
			if err != nil {
				return nil, huma.Error502BadGateway(err.Error())
			}
			return body(c), nil
		})
	huma.Register(api, op("update-apply", "POST", "/v1/update/apply", "admin",
		"Install the latest signed release and restart; a release that fails to start is rolled back automatically"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body map[string]string }, error) {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
			v, err := s.Updates.Updater.Apply(ctx, s.Updates.Version)
			if err != nil {
				return nil, huma.Error502BadGateway(err.Error())
			}
			if v != s.Updates.Version && s.Updates.Restart != nil {
				go func() { time.Sleep(time.Second); s.Updates.Restart() }() // let this response go out
			}
			return body(map[string]string{"version": v}), nil
		})
}
