// Package engine is the step-1 spike: a draft of the torrent engine interface
// plus tests (spike_test.go) that check anacrolix/torrent against scope §5.9.
// Throwaway: step 2 moves the interface to internal/engine.
package engine

import (
	"context"
	"io"
	"time"
)

// Engine is what the rest of the server sees. Everything torrent-specific stays behind it,
// so libtorrent could replace anacrolix without touching callers (scope §14).
type Engine interface {
	// Add takes a magnet URI or .torrent bytes. Adding an existing infohash returns the existing torrent.
	Add(ctx context.Context, magnetOrTorrent []byte) (Torrent, bool, error)
	Get(infohash string) (Torrent, bool)
	Close() error
}

type Torrent interface {
	InfoHash() string
	WaitInfo(ctx context.Context) error // "fetching metadata" state
	Files() []File
	SetFilePriority(index int, p Priority)
	Pause()
	Resume()
	Recheck(ctx context.Context) error
	Stats() Stats
	Peers() []Peer
	Pieces() []PieceRun // piece map bar
	// Reader for one file. Reads and seeks move download priority (stream mode).
	Reader(index int) io.ReadSeekCloser
	Drop(deleteData bool) error
}

type Priority int

const (
	Skip Priority = iota
	Normal
	High
	Now
)

type File struct {
	Path      string
	Length    int64
	Completed int64
}

type Stats struct {
	Downloaded, Uploaded int64
	DownRate, UpRate     float64 // bytes/s
	Peers, Seeders       int
	Completed, Total     int64
	ETA                  time.Duration
}

type Peer struct {
	Addr, Client       string
	Progress           float64
	DownRate, UpRate   float64
	Outgoing, Encrypted bool
}

type PieceRun struct {
	Length   int
	Complete bool
	Priority Priority
}
