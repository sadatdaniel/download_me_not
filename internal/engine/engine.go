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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/iplist"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"golang.org/x/net/proxy"
	"golang.org/x/time/rate"
)

type Engine interface {
	// Add takes a magnet URI or .torrent bytes. Adding a known infohash returns the existing torrent and false.
	Add(magnetOrTorrent []byte) (Torrent, bool, error)
	// AddIn is Add with the data kept in dir (a user's folder) instead of the engine's data directory.
	AddIn(magnetOrTorrent []byte, dir string) (Torrent, bool, error)
	Get(infohash string) (Torrent, bool)
	Port() int // listening port for peers (for doctor and firewall hints)
	// PeerCounts: peers connected now, how many of them dialled us, and when that last happened. An incoming
	// peer proves the torrent port is reachable from the internet.
	PeerCounts() (total, incoming int, lastIncoming time.Time)
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
	Err() error // why data stopped: a failed disk write (nil = fine)
	Stats() Stats
	Peers() []Peer
	Pieces() []PieceRun
	Swarm() Swarm
	Trackers() [][]string
	SetTrackers(tiers [][]string)
	Private() bool                // BEP 27: never add outside trackers or record its tracker URLs (they carry passkeys)
	PrioritizeEnds(fileIndex int) // stream mode: first and last pieces first
	// StreamWindow (stream mode, called periodically) fetches the next window bytes after the first missing
	// piece of a file first, so playback from the start never waits behind rarest-first pieces.
	StreamWindow(fileIndex int, window int64)
	StreamOff(fileIndex int) // back to standard: pieces parked by StreamWindow download normally again
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

// Peer is one connected peer. Direction and encryption are not exposed by anacrolix v1.61.
type Peer struct {
	Addr     string  `json:"addr"`
	Client   string  `json:"client"`
	Source   string  `json:"source"`   // how we found it: tracker, DHT, PEX, ...
	Progress float64 `json:"progress"` // 0..1 of the torrent the peer has
	DownRate float64 `json:"downRate"` // bytes/s from this peer
	UpRate   float64 `json:"upRate"`
}

// Swarm summarises availability for the piece view and the "why is it slow" hint.
type Swarm struct {
	Peers        int     `json:"peers"`
	Seeders      int     `json:"seeders"`
	Pieces       int     `json:"pieces"`
	Complete     int     `json:"complete"`
	Availability float64 `json:"availability"` // distributed copies among connected peers (qBittorrent's definition)
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
	NoUTP      bool
	NoIPv6     bool
	Encryption string // "prefer" (default), "require" or "off"
	MaxPeers   int    // established connections per torrent; 0 = library default (50)
	// Anonymous turns off DHT and PEX and hides the client name. It does not hide the IP address;
	// that needs a VPN or proxy (scope §5.9).
	Anonymous bool
	// Shared limiters (also used by web downloads); change them at runtime with SetLimit. nil = unlimited.
	DownLimiter, UpLimiter *rate.Limiter
	Blocklist              *Blocklist
	// DHTNodesFile keeps the DHT routing table across restarts so peers are found faster (swarm memory).
	DHTNodesFile string
	// DHTBootstrap replaces the public bootstrap routers; a non-nil empty slice means none (tests, private DHT).
	DHTBootstrap []string
	// Socks5 ("host:port", optional "user:pass@host:port") sends peer connections, HTTP trackers and web seeds
	// through a proxy. UDP cannot go through it, so DHT, uTP and UDP trackers are switched off (no IP leak).
	Socks5 string
	UPnP   bool // ask the router to forward the listening port (off by default, scope §5.9)
	// Control checks web seed, tracker and metadata HTTP dials (netguard.Safe). nil = unchecked.
	Control func(network, address string, c syscall.RawConn) error
}

type engine struct {
	cl            *torrent.Client
	dataDir       string
	nodesFile     string
	noUDPTrackers bool         // proxy mode
	lastIncoming  atomic.Int64 // unix seconds when a peer last connected to us (proof the port is reachable)

	mu       sync.Mutex
	dirs     map[string]string                   // infohash → folder, for torrents added with AddIn
	werr     sync.Map                            // infohash → the write error that stopped it (see wrap)
	storages map[string]storage.ClientImplCloser // one per folder: each keeps a piece-completion database there
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
	cfg.NoDefaultPortForwarding = !o.UPnP // UPnP is opt-in (scope §5.9)
	cfg.DisableUTP = o.NoUTP
	if o.NoIPv6 {
		cfg.DisableIPv6 = true
	}
	switch o.Encryption {
	case "require":
		cfg.HeaderObfuscationPolicy = torrent.HeaderObfuscationPolicy{Preferred: true, RequirePreferred: true}
	case "off":
		cfg.HeaderObfuscationPolicy = torrent.HeaderObfuscationPolicy{}
	default:
		cfg.HeaderObfuscationPolicy = torrent.HeaderObfuscationPolicy{Preferred: true}
	}
	if o.MaxPeers > 0 {
		cfg.EstablishedConnsPerTorrent = o.MaxPeers
	}
	if o.Anonymous {
		cfg.NoDHT, cfg.DisablePEX = true, true
		cfg.Bep20 = "-XX0000-"
		cfg.ExtendedHandshakeClientVersion = ""
		cfg.HTTPUserAgent = ""
	}
	if o.DownLimiter != nil {
		cfg.DownloadRateLimiter = o.DownLimiter
	}
	if o.UpLimiter != nil {
		cfg.UploadRateLimiter = o.UpLimiter
	}
	if o.Blocklist != nil {
		cfg.IPBlocklist = o.Blocklist
	}
	if o.Control != nil {
		d := &net.Dialer{Timeout: 30 * time.Second, Control: o.Control}
		cfg.HTTPDialContext = d.DialContext
		cfg.TrackerDialContext = d.DialContext
	}
	if o.DHTBootstrap != nil {
		boot := o.DHTBootstrap
		cfg.DhtStartingNodes = func(network string) dht.StartingNodesGetter {
			return func() ([]dht.Addr, error) {
				var out []dht.Addr
				for _, b := range boot {
					if ua, err := net.ResolveUDPAddr(network, b); err == nil {
						out = append(out, dht.NewAddr(ua))
					}
				}
				return out, nil
			}
		}
	}
	var socks proxy.ContextDialer
	if o.Socks5 != "" {
		u, err := url.Parse("socks5://" + o.Socks5)
		if err != nil {
			return nil, fmt.Errorf("socks5: %w", err)
		}
		var auth *proxy.Auth
		if u.User != nil {
			pw, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: pw}
		}
		d, err := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("socks5: %w", err)
		}
		socks = d.(proxy.ContextDialer)
		cfg.DialForPeerConns = false
		cfg.NoDHT, cfg.DisableUTP = true, true
		cfg.HTTPProxy = http.ProxyURL(u)
		cfg.HTTPDialContext, cfg.TrackerDialContext = nil, nil // they would block a LAN proxy; the proxy decides
	}
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	e := &engine{cl: cl, dataDir: o.DataDir, nodesFile: o.DHTNodesFile, noUDPTrackers: socks != nil,
		dirs: map[string]string{}, storages: map[string]storage.ClientImplCloser{}}
	if socks != nil {
		cl.AddDialer(torrent.NetworkDialer{Network: "tcp", Dialer: socks})
	}
	if o.DHTNodesFile != "" {
		for _, s := range cl.DhtServers() {
			if w, ok := s.(torrent.AnacrolixDhtServerWrapper); ok {
				w.Server.AddNodesFromFile(o.DHTNodesFile) // missing file on first start is fine
			}
		}
	}
	return e, nil
}

func (e *engine) Add(b []byte) (Torrent, bool, error) { return e.AddIn(b, "") }

func (e *engine) AddIn(b []byte, dir string) (_ Torrent, isNew bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("adding torrent: %v", r)
		}
	}()
	var spec *torrent.TorrentSpec
	if bytes.HasPrefix(b, []byte("magnet:")) {
		if spec, err = torrent.TorrentSpecFromMagnetUri(string(b)); err != nil {
			return nil, false, err
		}
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
		if spec, err = torrent.TorrentSpecFromMetaInfoErr(mi); err != nil {
			return nil, false, err
		}
	}
	dataDir := e.dataDir
	if dir != "" && dir != e.dataDir {
		dataDir = dir
		if _, known := e.cl.Torrent(spec.InfoHash); !known { // storage only applies to a new torrent
			if spec.Storage, err = e.storageFor(dir); err != nil {
				return nil, false, err
			}
		}
	}
	t, isNew, err := e.cl.AddTorrentSpec(spec)
	if err != nil {
		return nil, false, err
	}
	if isNew {
		e.mu.Lock()
		e.dirs[t.InfoHash().HexString()] = dataDir
		e.mu.Unlock()
	}
	tt := e.wrap(t)
	if tt.noUDP {
		tt.SetTrackers(tt.Trackers()) // drops udp:// trackers in proxy mode
	}
	return tt, isNew, nil
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
	return e.wrap(t), true
}

// wrap is a torrent's handle. A chunk that can't be written (read-only or full disk, missing folder) stops its data
// download and is kept for Err, so the job fails with the reason. anacrolix's default only logs it and stops
// downloading, which looked like peers that never send.
func (e *engine) wrap(t *torrent.Torrent) *tor {
	ih := t.InfoHash().HexString()
	t.SetOnWriteChunkError(func(err error) {
		t.DisallowDataDownload()
		e.werr.LoadOrStore(ih, err)
	})
	return &tor{t: t, e: e, dataDir: e.dirOf(ih), noUDP: e.noUDPTrackers}
}

func (e *engine) Port() int { return e.cl.LocalPort() }

func (e *engine) PeerCounts() (total, incoming int, last time.Time) {
	for _, t := range e.cl.Torrents() {
		for _, pc := range t.PeerConns() {
			total++
			if pc.Discovery == torrent.PeerSourceIncoming {
				incoming++
			}
		}
	}
	if incoming > 0 {
		e.lastIncoming.Store(time.Now().Unix())
	}
	if s := e.lastIncoming.Load(); s > 0 {
		last = time.Unix(s, 0)
	}
	return total, incoming, last
}

// Close saves the DHT routing table (if configured) and stops the client.
func (e *engine) Close() error {
	var errs []error
	if e.nodesFile != "" {
		for _, s := range e.cl.DhtServers() {
			if w, ok := s.(torrent.AnacrolixDhtServerWrapper); ok {
				if ns := w.Server.Nodes(); len(ns) > 0 {
					errs = append(errs, dht.WriteNodesToFile(ns, e.nodesFile))
				}
			}
		}
	}
	errs = append(errs, e.cl.Close()...)
	e.mu.Lock()
	for _, s := range e.storages {
		errs = append(errs, s.Close())
	}
	e.mu.Unlock()
	return errors.Join(errs...)
}

type tor struct {
	t       *torrent.Torrent
	e       *engine
	dataDir string
	noUDP   bool

	mu       sync.Mutex
	lastAt   time.Time
	lastDown int64
	lastUp   int64

	parked  atomic.Bool  // stream mode: pieces beyond the window are set to wait (see StreamWindow)
	skipped map[int]bool // files the user skipped (under mu)
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

// Start is synchronous when metadata is known (so later priority changes stick), otherwise it
// waits in the background: DownloadAll before metadata panics.
func (t *tor) Start() {
	if t.t.Info() != nil {
		t.t.DownloadAll()
		return
	}
	go func() {
		<-t.t.GotInfo()
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
	fs := t.t.Files()
	if i < 0 || i >= len(fs) {
		return
	}
	fs[i].SetPriority(prio)
	t.mu.Lock()
	if t.skipped == nil {
		t.skipped = map[int]bool{}
	}
	t.skipped[i] = p == Skip
	t.mu.Unlock()
	// anacrolix takes the highest of file and piece priority, and Start (DownloadAll) raised every piece to
	// normal, so the file priority alone does not skip. Set the pieces that belong only to this file too;
	// pieces shared with a neighbouring file keep downloading.
	pl := t.t.Info().PieceLength
	off, end := fs[i].Offset(), fs[i].Offset()+fs[i].Length()
	for pc := fs[i].BeginPieceIndex(); pc < fs[i].EndPieceIndex(); pc++ {
		if int64(pc)*pl >= off && min(int64(pc+1)*pl, t.t.Length()) <= end {
			t.t.Piece(pc).SetPriority(prio)
		}
	}
}

func (t *tor) Pause() { t.t.DisallowDataDownload() }

// Resume also forgets a write error: the job is trying again (e.g. after the folder was fixed).
func (t *tor) Resume() {
	t.e.werr.Delete(t.InfoHash())
	t.t.AllowDataDownload()
}

func (t *tor) Err() error {
	if v, ok := t.e.werr.Load(t.InfoHash()); ok {
		return v.(error)
	}
	return nil
}

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
	n := 1
	if t.t.Info() != nil {
		n = max(t.t.NumPieces(), 1)
	}
	for _, pc := range pcs {
		name, _ := pc.PeerClientName.Load().(string)
		st := pc.Stats()
		out = append(out, Peer{Addr: pc.RemoteAddr.String(), Client: name, Source: string(pc.Discovery),
			Progress: min(1, float64(st.RemotePieceCount)/float64(n)), DownRate: st.DownloadRate, UpRate: st.LastWriteUploadRate})
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

// Create builds a v1 .torrent for a local file or folder (self-test, and creating torrents later).
func Create(path string, trackers []string) ([]byte, error) {
	info := metainfo.Info{PieceLength: 256 << 10}
	if err := info.BuildFromFilePath(path); err != nil {
		return nil, err
	}
	ib, err := bencode.Marshal(info)
	if err != nil {
		return nil, err
	}
	mi := metainfo.MetaInfo{InfoBytes: ib}
	if len(trackers) > 0 {
		mi.Announce = trackers[0]
		mi.AnnounceList = [][]string{trackers}
	}
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Blocklist is an iplist.Ranger whose list can be replaced while the engine runs
// (anacrolix only takes a blocklist at start).
type Blocklist struct{ p atomic.Pointer[iplist.IPList] }

func (b *Blocklist) Lookup(ip net.IP) (iplist.Range, bool) {
	if l := b.p.Load(); l != nil {
		return l.Lookup(ip)
	}
	return iplist.Range{}, false
}

func (b *Blocklist) NumRanges() int {
	if l := b.p.Load(); l != nil {
		return l.NumRanges()
	}
	return 0
}

// Load replaces the list with one in P2P/eMule text format ("desc:1.2.3.0-1.2.3.255").
func (b *Blocklist) Load(r io.Reader) (int, error) {
	l, err := iplist.NewFromReader(r)
	if err != nil {
		return 0, err
	}
	b.p.Store(l)
	return l.NumRanges(), nil
}

func (t *tor) Swarm() Swarm {
	g := t.t.Stats()
	s := Swarm{Peers: g.ActivePeers, Seeders: g.ConnectedSeeders, Complete: g.PiecesComplete}
	if t.t.Info() == nil {
		return s
	}
	s.Pieces = t.t.NumPieces()
	counts := make([]int, s.Pieces)
	for _, pc := range t.t.PeerConns() {
		it := pc.PeerPieces().Iterator()
		for it.HasNext() {
			if i := int(it.Next()); i < len(counts) {
				counts[i]++
			}
		}
	}
	// Distributed copies: the rarest piece's count plus the share of pieces above that count.
	if s.Pieces > 0 {
		lo := counts[0]
		for _, c := range counts {
			lo = min(lo, c)
		}
		above := 0
		for _, c := range counts {
			if c > lo {
				above++
			}
		}
		s.Availability = float64(lo) + float64(above)/float64(s.Pieces)
	}
	return s
}

func (t *tor) Trackers() [][]string {
	mi := t.t.Metainfo()
	return mi.UpvertedAnnounceList()
}

func (t *tor) SetTrackers(tiers [][]string) {
	if t.noUDP {
		var kept [][]string
		for _, tier := range tiers {
			var k []string
			for _, u := range tier {
				if !strings.HasPrefix(u, "udp:") {
					k = append(k, u)
				}
			}
			if len(k) > 0 {
				kept = append(kept, k)
			}
		}
		tiers = kept
	}
	t.t.ModifyTrackers(tiers)
}

// PrioritizeEnds fetches a file's first and last pieces first: players read the container index
// there (MP4 often keeps it at the end) before they can start (stream mode, scope §5.3).
func (t *tor) PrioritizeEnds(i int) {
	fs := t.t.Files()
	if i < 0 || i >= len(fs) {
		return
	}
	b, e := fs[i].BeginPieceIndex(), fs[i].EndPieceIndex()
	if e > b {
		t.t.Piece(b).SetPriority(torrent.PiecePriorityNow)
		t.t.Piece(e - 1).SetPriority(torrent.PiecePriorityNow)
	}
}

// Private reports the BEP 27 private flag. Unknown until metadata arrives (false).
func (t *tor) Private() bool {
	info := t.t.Info()
	return info != nil && info.Private != nil && *info.Private
}

// StreamWindow downloads a file in playback order, like WebTorrent or qBittorrent's sequential mode: the window
// after the first incomplete piece is high priority and pieces beyond it wait until the window reaches them.
// The file's first and last pieces keep PrioritizeEnds' priority. A player's read-ahead raises pieces on top of
// this (anacrolix takes the highest of piece, file and reader priority), so seeking ahead still works.
// Priorities are set, never read back: a piece reports "none" while it is hashed or while data is held.
func (t *tor) StreamWindow(i int, window int64) {
	b, e, ok := t.fileRange(i)
	if !ok {
		return
	}
	first := b
	for first < e && t.t.Piece(first).State().Complete {
		first++
	}
	end := min(e, first+max(1, int(window/t.t.Info().PieceLength)))
	if !t.parked.Swap(true) {
		for p := end; p < e-1; p++ {
			t.t.Piece(p).SetPriority(torrent.PiecePriorityNone)
		}
	}
	for p := max(first, b+1); p < end && p < e-1; p++ {
		t.t.Piece(p).SetPriority(torrent.PiecePriorityHigh)
	}
}

// StreamOff undoes StreamWindow when a download switches back to standard mode: every incomplete piece of the
// file returns to normal priority, so the rest downloads rarest-first again.
func (t *tor) StreamOff(i int) {
	b, e, ok := t.fileRange(i)
	if !ok || !t.parked.Swap(false) {
		return
	}
	for p := b + 1; p < e-1; p++ {
		if !t.t.Piece(p).State().Complete {
			t.t.Piece(p).SetPriority(torrent.PiecePriorityNormal)
		}
	}
}

// fileRange is the piece range of file i; ok is false before metadata, for a bad index, or for a skipped file.
func (t *tor) fileRange(i int) (b, e int, ok bool) {
	if t.t.Info() == nil {
		return 0, 0, false
	}
	fs := t.t.Files()
	t.mu.Lock()
	skip := t.skipped[i]
	t.mu.Unlock()
	if i < 0 || i >= len(fs) || skip {
		return 0, 0, false
	}
	return fs[i].BeginPieceIndex(), fs[i].EndPieceIndex(), true
}

// storageFor returns the storage client for a folder, creating it once.
func (e *engine) storageFor(dir string) (storage.ClientImplCloser, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if s, ok := e.storages[dir]; ok {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: dir})
	e.storages[dir] = s
	return s, nil
}

// dirOf is where a torrent's data lives: its AddIn folder, or the engine's data directory.
func (e *engine) dirOf(infohash string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if d, ok := e.dirs[infohash]; ok {
		return d
	}
	return e.dataDir
}
