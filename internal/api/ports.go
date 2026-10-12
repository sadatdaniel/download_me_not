package api

import (
	"context"
	"fmt"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

type portsOut struct {
	API            string     `json:"api" doc:"address the web panel and API listen on"`
	TorrentPort    int        `json:"torrentPort" doc:"TCP and UDP port for torrent peers"`
	TorrentRunning bool       `json:"torrentRunning" doc:"the torrent engine starts with the first torrent job"`
	Peers          int        `json:"peers" doc:"peers connected now"`
	Incoming       int        `json:"incoming" doc:"of those, peers that connected to us"`
	LastIncoming   *time.Time `json:"lastIncoming,omitempty" doc:"when a peer last connected to us"`
	Verdict        string     `json:"verdict" enum:"open,probably-closed,unknown,not-running"`
	Hint           string     `json:"hint"`
}

// Reachability can't be proven from inside the server without an outside helper, so this reports evidence:
// a peer that connected to us proves the port is open; many peers and none incoming suggests it is closed.
// The app adds the outside view by connecting to the port from the phone.
func (s *server) portRoutes(api huma.API) {
	huma.Register(api, op("ports", "GET", "/v1/ports", "read", "Listening ports and whether the torrent port is reachable from outside"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body portsOut }, error) {
			o := portsOut{API: s.Cfg.Server.Listen, TorrentPort: s.configuredTorrentPort()}
			port, running, peers, in, last := s.Jobs.TorrentStatus()
			o.TorrentRunning, o.Peers, o.Incoming = running, peers, in
			if running {
				o.TorrentPort = port
			}
			if !last.IsZero() {
				o.LastIncoming = &last
			}
			open := fmt.Sprintf("Open TCP and UDP %d in your cloud firewall (security list) and on the server (see docs/deploy.md).", o.TorrentPort)
			switch {
			case !running:
				o.Verdict, o.Hint = "not-running", "The torrent engine starts with the first torrent download; check again while one runs."
			case in > 0 || (!last.IsZero() && time.Since(last) < 24*time.Hour):
				o.Verdict, o.Hint = "open", "Peers connect to this server, so the torrent port is reachable."
			case peers >= 10:
				o.Verdict, o.Hint = "probably-closed", fmt.Sprintf("%d peers, none connected to us. Downloads still work, but slower. %s", peers, open)
			default:
				o.Verdict, o.Hint = "unknown", "Not enough peers yet to tell. "+open
			}
			return body(o), nil
		})
}

func (s *server) configuredTorrentPort() int {
	if p, ok := s.Cfg.Providers["torrent"]; ok {
		switch v := p.Settings["port"].(type) {
		case int:
			return v
		case int64:
			return int(v)
		case float64:
			return int(v)
		}
	}
	return 42069
}
