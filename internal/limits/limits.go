// Package limits holds the global bandwidth limiters shared by web downloads and the torrent
// engine. Limits change at runtime; 0 means unlimited.
package limits

import (
	"sync"

	"golang.org/x/time/rate"
)

// Burst must cover the largest single read or write (64 KiB in httpdl, 16 KiB torrent chunks).
const burst = 256 << 10

type Limits struct {
	Down, Up *rate.Limiter

	mu       sync.Mutex
	down, up int64
}

func New(down, up int64) *Limits {
	l := &Limits{Down: rate.NewLimiter(rate.Inf, burst), Up: rate.NewLimiter(rate.Inf, burst)}
	l.Set(down, up)
	return l
}

func limit(bps int64) rate.Limit {
	if bps <= 0 {
		return rate.Inf
	}
	return rate.Limit(bps)
}

// Set applies new limits in bytes per second immediately.
func (l *Limits) Set(down, up int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.down, l.up = max(down, 0), max(up, 0)
	l.Down.SetLimit(limit(down))
	l.Up.SetLimit(limit(up))
}

func (l *Limits) Get() (down, up int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.down, l.up
}
