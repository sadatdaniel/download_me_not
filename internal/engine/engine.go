// Package engine is the torrent engine interface and its anacrolix/torrent implementation.
// Everything torrent-specific stays behind Engine so libtorrent could replace anacrolix (scope §14).
// Lessons from the spike (docs/engine-spike.md) are handled here:
//   - nothing downloads until asked: Start waits for metadata, then downloads all;
//   - v2-only torrents panic in v1.61: rejected with ErrV2Only, and adds run under recover;
//   - after a restart progress shows 0 until Recheck, so the job layer rechecks unfinished torrents.
package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

type Engine interface {
	// Add takes a magnet URI or .torrent bytes. Adding a known infohash returns the existing torrent and false.
	Add(magnetOrTorrent []byte) (Torrent, bool, error)
	Get(infohash string) (Torrent, bool)
	Close() error
}

type Torrent interface {
	InfoHash() string
	Name() string
	WaitInfo(ctx context.Context) error // the "fetching metadata" state; the caller sets the timeout
	Start()                             // download everything once metadata is known
	Done() <-chan struct{}              // closed when all wanted data is complete and moved from .part
	Files() []File                      // nil until metadata is known
	SetFilePriority(index int, p Priority)
	Pause()
	Resume()
	Recheck(ctx context.Context) error
	Stats() Stats
	Peers() []Peer
	Pieces() []PieceRun
	// Reader for one file; reading and seeking move download priority (stream mode).
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
	Path      string `json:"path"`
	Length    int64  `json:"length"`
	Completed int64  `json:"completed"`
}

type Stats struct {
	Downloaded int64   `json:"downloaded"`
	Uploaded   int64   `json:"uploaded"`
	DownRate   float64 `json:"downRate"` // bytes/s since the previous Stats call
	UpRate     float64 `json:"upRate"`
	Peers      int     `json:"peers"`
	Seeders    int     `json:"seeders"`
	Completed  int64   `json:"completed"`
	Total      int64   `json:"total"` // 0 until metadata is known
}

// Peer: progress, rates and direction come in M2 (peer view).
type Peer struct {
	Addr   string `json:"addr"`
	Client string `json:"client"`
}

type PieceRun struct {
	Length   int  `json:"length"`
	Complete bool `json:"complete"`
	Partial  bool `json:"partial"`
	Checking bool `json:"checking"`
}

var ErrV2Only = errors.New("BitTorrent v2-only torrents are not supported yet (hybrid v1+v2 torrents work)")

type Options struct {
	DataDir    string
	ListenHost string // "" = all interfaces; bind to a VPN interface address here
	ListenPort int    // 0 = random
	NoDHT      bool
	NoPEX      bool
	NoUpload   bool
	// Control checks web seed, tracker and metadata HTTP dials (netguard.Safe). nil = unchecked.
	Control func(network, address string, c syscall.RawConn) error
}

type engine struct {
	cl      *torrent.Client
	dataDir string
}

func New(o Options) (Engine, error) {
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = o.DataDir
	cfg.ListenPort = o.ListenPort
	if o.ListenHost != "" {
		h := o.ListenHost
		cfg.ListenHost = func(string) string { return h }
		// A literal address pins the family; otherwise anacrolix also tries the other one and fails.
		if ip := net.ParseIP(h); ip != nil {
			cfg.DisableIPv6 = ip.To4() != nil
			cfg.DisableIPv4 = ip.To4() == nil
		}
	}
	cfg.NoDHT = o.NoDHT
	cfg.DisablePEX = o.NoPEX
	cfg.NoUpload = o.NoUpload
	cfg.Seed = !o.NoUpload
	cfg.NoDefaultPortForwarding = true // UPnP is opt-in (scope §5.9)
	if o.Control != nil {
		d := &net.Dialer{Timeout: 30 * time.Second, Control: o.Control}
		cfg.HTTPDialContext = d.DialContext
		cfg.TrackerDialContext = d.DialContext
	}
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &engine{cl: cl, dataDir: o.DataDir}, nil
}

func (e *engine) Add(b []byte) (_ Torrent, isNew bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("adding torrent: %v", r)
		}
	}()
	var t *torrent.Torrent
	if bytes.HasPrefix(b, []byte("magnet:")) {
		before := len(e.cl.Torrents())
		if t, err = e.cl.AddMagnet(string(b)); err != nil {
			return nil, false, err
		}
		isNew = len(e.cl.Torrents()) > before
	} else {
		mi, err := metainfo.Load(bytes.NewReader(b))
		if err != nil {
			return nil, false, fmt.Errorf("reading .torrent: %w", err)
		}
		info, err := mi.UnmarshalInfo()
		if err != nil {
			return nil, false, fmt.Errorf("reading .torrent info: %w", err)
		}
		if !info.HasV1() {
			return nil, false, ErrV2Only
		}
		spec, err := torrent.TorrentSpecFromMetaInfoErr(mi)
		if err != nil {
			return nil, false, err
		}
		if t, isNew, err = e.cl.AddTorrentSpec(spec); err != nil {
			return nil, false, err
		}
	}
	return &tor{t: t, dataDir: e.dataDir}, isNew, nil
}

func (e *engine) Get(infohash string) (Torrent, bool) {
	var h metainfo.Hash
	if err := h.FromHexString(infohash); err != nil {
		return nil, false
	}
	t, ok := e.cl.Torrent(h)
	if !ok {
		return nil, false
	}
	return &tor{t: t, dataDir: e.dataDir}, true
}

func (e *engine) Close() error { return errors.Join(e.cl.Close()...) }

type tor struct {
	t       *torrent.Torrent
	dataDir string

	mu       sync.Mutex
	lastAt   time.Time
	lastDown int64
	lastUp   int64
}

func (t *tor) InfoHash() string { return t.t.InfoHash().HexString() }
func (t *tor) Name() string     { return t.t.Name() }

func (t *tor) WaitInfo(ctx context.Context) error {
	select {
	case <-t.t.GotInfo():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *tor) Start() {
	go func() {
		<-t.t.GotInfo() // DownloadAll before metadata panics
		t.t.DownloadAll()
	}()
}

func (t *tor) Done() <-chan struct{} { return t.t.Complete().On() }

func (t *tor) Files() []File {
	if t.t.Info() == nil {
		return nil
	}
	fs := t.t.Files()
	out := make([]File, len(fs))
	for i, f := range fs {
		out[i] = File{Path: f.Path(), Length: f.Length(), Completed: f.BytesCompleted()}
	}
	return out
}

func (t *tor) SetFilePriority(i int, p Priority) {
	prio := map[Priority]torrent.PiecePriority{
		Skip: torrent.PiecePriorityNone, Normal: torrent.PiecePriorityNormal,
		High: torrent.PiecePriorityHigh, Now: torrent.PiecePriorityNow,
	}[p]
	if fs := t.t.Files(); i >= 0 && i < len(fs) {
		fs[i].SetPriority(prio)
	}
}

func (t *tor) Pause()  { t.t.DisallowDataDownload() }
func (t *tor) Resume() { t.t.AllowDataDownload() }

func (t *tor) Recheck(ctx context.Context) error { return t.t.VerifyDataContext(ctx) }

func (t *tor) Stats() Stats {
	s := t.t.Stats()
	out := Stats{
		Downloaded: s.BytesReadData.Int64(),
		Uploaded:   s.BytesWrittenData.Int64(),
		Peers:      s.ActivePeers,
		Seeders:    s.ConnectedSeeders,
		Completed:  t.t.BytesCompleted(),
	}
	if t.t.Info() != nil {
		out.Total = t.t.Length()
	}
	t.mu.Lock()
	now := time.Now()
	if dt := now.Sub(t.lastAt).Seconds(); !t.lastAt.IsZero() && dt > 0 {
		out.DownRate = float64(out.Downloaded-t.lastDown) / dt
		out.UpRate = float64(out.Uploaded-t.lastUp) / dt
	}
	t.lastAt, t.lastDown, t.lastUp = now, out.Downloaded, out.Uploaded
	t.mu.Unlock()
	return out
}

func (t *tor) Peers() []Peer {
	pcs := t.t.PeerConns()
	out := make([]Peer, 0, len(pcs))
	for _, pc := range pcs {
		name, _ := pc.PeerClientName.Load().(string)
		out = append(out, Peer{Addr: pc.RemoteAddr.String(), Client: name})
	}
	return out
}

func (t *tor) Pieces() []PieceRun {
	if t.t.Info() == nil {
		return nil
	}
	runs := t.t.PieceStateRuns()
	out := make([]PieceRun, len(runs))
	for i, r := range runs {
		out[i] = PieceRun{Length: r.Length, Complete: r.Complete, Partial: r.Partial, Checking: r.Checking}
	}
	return out
}

func (t *tor) Reader(i int) io.ReadSeekCloser {
	r := t.t.Files()[i].NewReader()
	r.SetResponsive()       // return data as soon as it arrives instead of waiting for readahead
	r.SetReadahead(4 << 20) // ponytail: fixed 4 MiB; tune per bitrate in M1 stream mode
	return r
}

// Drop removes the torrent; with deleteData it also removes its files (and .part files),
// confined to the data folder.
func (t *tor) Drop(deleteData bool) error {
	var paths []string
	if deleteData && t.t.Info() != nil {
		for _, f := range t.t.Files() {
			paths = append(paths, f.Path(), f.Path()+".part")
		}
	}
	t.t.Drop()
	if len(paths) == 0 {
		return nil
	}
	root, err := os.OpenRoot(t.dataDir)
	if err != nil {
		return err
	}
	defer root.Close()
	var errs []error
	for _, p := range paths {
		if err := root.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	// Remove now-empty parent folders of multi-file torrents (Remove fails on non-empty ones).
	if d := filepath.Dir(paths[0]); d != "." {
		root.Remove(d)
	}
	return errors.Join(errs...)
}

// InfoHash returns the v1 infohash (hex) of a magnet URI or .torrent bytes without adding it,
// for duplicate detection before the engine is even started.
func InfoHash(b []byte) (string, error) {
	if bytes.HasPrefix(b, []byte("magnet:")) {
		m, err := metainfo.ParseMagnetV2Uri(string(b))
		if err != nil {
			return "", err
		}
		if !m.InfoHash.Ok {
			return "", ErrV2Only
		}
		return m.InfoHash.Value.HexString(), nil
	}
	mi, err := metainfo.Load(bytes.NewReader(b))
	if err != nil {
		return "", fmt.Errorf("reading .torrent: %w", err)
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return "", err
	}
	if !info.HasV1() {
		return "", ErrV2Only
	}
	return mi.HashInfoBytes().HexString(), nil
}
