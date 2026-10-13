package tools

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// OnDemand installs a missing managed tool in the background the first time something needs it (a provider
// switched on, a copy to Drive, a video download), so the feature works a minute later instead of waiting for
// the daily maintenance. One install per tool at a time.
type OnDemand struct {
	Inst *Installer // nil: tools are never installed here (they must come from the system)
	Dir  string     // data folder, where managed copies live (used when Inst is nil)

	mu       sync.Mutex
	busy     map[string]chan struct{}
	failed   map[string]string
	failedAt map[string]time.Time // a failed install is retried after 10 minutes, not on every request
}

// Ensure returns the tool's path. When it is missing, it starts installing it and waits up to wait for it; with
// wait 0 it only starts the install and reports that it is running.
func (o *OnDemand) Ensure(ctx context.Context, name string, wait time.Duration) (string, error) {
	if p, ok := Find(name, o.dataDir()); ok {
		return p, nil
	}
	if o == nil || o.Inst == nil || sources[name].repo == "" {
		return "", fmt.Errorf("%s is not installed on the server", name)
	}
	o.mu.Lock()
	ch := o.busy[name]
	if ch == nil && time.Since(o.failedAt[name]) < 10*time.Minute {
		why := o.failed[name]
		o.mu.Unlock()
		return "", fmt.Errorf("installing %s on the server failed: %s", name, why)
	}
	if ch == nil {
		if o.busy == nil {
			o.busy, o.failed, o.failedAt = map[string]chan struct{}{}, map[string]string{}, map[string]time.Time{}
		}
		ch = make(chan struct{})
		o.busy[name] = ch
		delete(o.failed, name)
		go o.install(name, ch)
	}
	o.mu.Unlock()
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-ch:
		case <-t.C:
		case <-ctx.Done():
		}
	}
	if p, ok := Find(name, o.dataDir()); ok {
		return p, nil
	}
	_, why := o.State(name)
	if why != "" {
		return "", fmt.Errorf("installing %s on the server failed: %s", name, why)
	}
	return "", fmt.Errorf("installing %s on the server, this takes about a minute", name)
}

func (o *OnDemand) install(name string, done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	_, err := o.Inst.Install(ctx, name)
	o.mu.Lock()
	if err != nil {
		o.failed[name], o.failedAt[name] = err.Error(), time.Now()
	}
	delete(o.busy, name)
	o.mu.Unlock()
	close(done)
}

// State says whether a tool is being installed right now, or why its last install failed.
func (o *OnDemand) State(name string) (installing bool, failed string) {
	if o == nil {
		return false, ""
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.busy[name] != nil, o.failed[name]
}

func (o *OnDemand) dataDir() string {
	switch {
	case o == nil:
		return ""
	case o.Inst != nil:
		return o.Inst.DataDir
	}
	return o.Dir
}
