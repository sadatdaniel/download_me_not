// Command dmn is the server and its CLI in one static binary. ("dmn" is a placeholder name.)
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"

	"github.com/sadatdaniel/download_me_not/internal/acme"
	"github.com/sadatdaniel/download_me_not/internal/api"
	"github.com/sadatdaniel/download_me_not/internal/config"
	"github.com/sadatdaniel/download_me_not/internal/cookies"
	"github.com/sadatdaniel/download_me_not/internal/engine"
	"github.com/sadatdaniel/download_me_not/internal/fsapi"
	"github.com/sadatdaniel/download_me_not/internal/gdrive"
	"github.com/sadatdaniel/download_me_not/internal/jobs"
	"github.com/sadatdaniel/download_me_not/internal/limits"
	"github.com/sadatdaniel/download_me_not/internal/netguard"
	"github.com/sadatdaniel/download_me_not/internal/provider"
	"github.com/sadatdaniel/download_me_not/internal/rclone"
	"github.com/sadatdaniel/download_me_not/internal/secret"
	"github.com/sadatdaniel/download_me_not/internal/selftest"
	"github.com/sadatdaniel/download_me_not/internal/store"
	"github.com/sadatdaniel/download_me_not/internal/tlsutil"
	"github.com/sadatdaniel/download_me_not/internal/tools"
	"github.com/sadatdaniel/download_me_not/internal/update"
	"github.com/sadatdaniel/download_me_not/internal/video"
)

var version = "dev" // set by -ldflags "-X main.version=..."

// updateRepo is where signed releases are published.
var updateRepo = "sadatdaniel/download_me_not"

var (
	cfgPath  string
	jsonOut  bool
	srvURL   string
	srvToken string
	srvFP    string
)

func main() {
	root := &cobra.Command{Use: "dmn", Short: "Self-hosted download server and CLI", SilenceUsage: true}
	pf := root.PersistentFlags()
	pf.StringVar(&cfgPath, "config", config.DefaultPath(), "config file (env DMN_CONFIG)")
	pf.BoolVar(&jsonOut, "json", false, "JSON output for scripting")
	pf.StringVar(&srvURL, "url", envOr("DMN_URL", "http://127.0.0.1:8080"), "server URL (env DMN_URL)")
	pf.StringVar(&srvToken, "token", os.Getenv("DMN_TOKEN"), "API token (env DMN_TOKEN)")
	pf.StringVar(&srvFP, "fingerprint", os.Getenv("DMN_FINGERPRINT"), "pin a self-signed server certificate by SHA-256 (env DMN_FINGERPRINT; shown by `dmn pair`)")

	root.AddCommand(serveCmd(), setupCmd(), configCmd(), doctorCmd(), versionCmd(), providersCmd(),
		lsCmd(), statCmd(), mkdirCmd(), mvCmd(), cpCmd(), zipCmd(), rmCmd(), watchCmd(), addCmd(), modeCmd(), cancelCmd(),
		jobAction("pause"), jobAction("resume"), jobAction("recheck"), limitsCmd(), peersCmd(), piecesCmd(), trackersCmd(), priorityCmd(), filesCmd(), learnedCmd(), formatsCmd(), cookiesCmd(), pairCmd(), copyCmd(), toolsCmd(), updateCmd(), uninstallCmd(), healthCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// show prints v as JSON with --json, otherwise calls human.
func show(v any, human func()) error {
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	human()
	return nil
}

func table(rows [][]string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	w.Flush()
}

// ---- server-side commands ----

func serveCmd() *cobra.Command {
	c := &cobra.Command{Use: "serve", Short: "Start the server"}
	c.Flags().String("listen", "", "listen address (server.listen)")
	c.Flags().String("data", "", "data folder (paths.data)")
	c.Flags().String("downloads", "", "download folder (paths.downloads)")
	c.RunE = func(c *cobra.Command, _ []string) error {
		_, cfg, err := config.Load(cfgPath, c.Flags())
		if err != nil {
			return err
		}
		// Safe by default (scope §4.5): without a token, only loopback is allowed.
		if len(cfg.Auth.Tokens) == 0 && !loopback(cfg.Server.Listen) {
			return fmt.Errorf("no API token yet: run `dmn setup` first, or listen on 127.0.0.1")
		}
		if cfg.Auth.Signkey == "" {
			return fmt.Errorf("no signing key: run `dmn setup` first")
		}
		if err := os.MkdirAll(cfg.Paths.Data, 0o700); err != nil {
			return err
		}
		// Confirm-after-boot (scope §5.8): a new version that never got healthy is replaced by the
		// previous one here; exiting lets the supervisor start it.
		upd := update.Updater{Repo: updateRepo, DataDir: cfg.Paths.Data}
		if rb, err := upd.Boot(); err != nil {
			fmt.Fprintln(os.Stderr, "update:", err)
		} else if rb {
			return errors.New("the updated version did not start correctly; the previous version was restored and will start next")
		}
		db, err := store.Open(filepath.Join(cfg.Paths.Data, "dmn.db"))
		if err != nil {
			return err
		}
		defer db.Close()
		fsys, err := fsapi.New(cfg.Paths.Downloads, []byte(cfg.Auth.Signkey))
		if err != nil {
			return err
		}
		defer fsys.Close()
		pcfg := map[string]provider.Settings{}
		for n, p := range cfg.Providers {
			pcfg[n] = provider.Settings{Enabled: p.Enabled, Settings: p.Settings}
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		reg := provider.NewRegistry(pcfg, cfg.Paths.Data)
		// The torrent engine starts with the first torrent job, so a server that only does web
		// downloads never opens a torrent port or joins the DHT.
		// The cookie key sits next to the config file, apart from the data folder (scope §5.6).
		keyPath := filepath.Join(filepath.Dir(cfgPath), "secret.key")
		box, err := secret.Open(keyPath)
		if err != nil {
			return fmt.Errorf("secrets: %w", err)
		}
		ck, err := cookies.Open(filepath.Join(cfg.Paths.Data, "cookies"), keyPath)
		if err != nil {
			return fmt.Errorf("cookies: %w", err)
		}
		lim := limits.New(cfg.Limits.Down, cfg.Limits.Up)
		bl := &engine.Blocklist{}
		if src := settingStr(cfg, "torrent", "blocklist", ""); src != "" {
			go loadBlocklist(ctx, src, bl)
		}
		jm := jobs.New(jobs.Options{DB: db, Downloads: cfg.Paths.Downloads, Client: netguard.Client(), Allowed: reg.Enabled,
			Connections: setting(cfg, "web", "connections", 16), Limiter: lim.Down,
			MaxActive: cfg.Jobs.MaxActive, DiskReserve: cfg.Jobs.Reserve,
			Post:          jobs.Post{Extract: cfg.Jobs.Extract, DeleteArchive: cfg.Jobs.DeleteArchive},
			LearnTrackers: settingBool(cfg, "torrent", "learnTrackers", true),
			Seed: jobs.SeedPolicy{Policy: settingStr(cfg, "torrent", "seed", "stop"),
				Ratio: settingFloat(cfg, "torrent", "seedRatio", 1), Minutes: setting(cfg, "torrent", "seedMinutes", 60)},
			NewEngine:   func() (engine.Engine, error) { return newEngine(cfg, lim, bl) },
			TorrentGate: killSwitch(settingStr(cfg, "torrent", "bind", "")),
			Video:       videoRunner(ctx, cfg.Paths.Data),
			Cookies:     ck,
			Destination: destinations(cfg, reg, box),
		})
		inst := &tools.Installer{Client: netguard.Client(), DataDir: cfg.Paths.Data}
		if cfg.Updates.Tools != "off" {
			go maintainTools(ctx, inst, reg)
		}
		jobsDone := make(chan error, 1)
		go func() { jobsDone <- jm.Run(ctx) }()
		updTok, _ := box.Open("updates.token", cfg.Updates.Token)
		if updTok == "" {
			updTok = os.Getenv("GITHUB_TOKEN")
		} else if !secret.IsSealed(cfg.Updates.Token) {
			config.Set(cfgPath, "updates.token", box.Seal("updates.token", updTok))
		}
		upd.Token = updTok
		ups := &api.Updates{Updater: upd, Version: version, Restart: stop}
		go upd.ConfirmWhenHealthy(ctx, func() error {
			for _, c := range selftest.Run(ctx) {
				if !c.OK {
					return errors.New(c.Name + ": " + c.Detail)
				}
			}
			return nil
		})
		if cfg.Updates.Server != "off" {
			go checkUpdates(ctx, ups, jm, cfg.Updates.Server == "auto")
		}
		mode := tlsMode(cfg)
		var cert tls.Certificate
		fp := ""
		if mode == "self-signed" {
			if cert, err = tlsutil.SelfSigned(filepath.Join(cfg.Paths.Data, "tls")); err != nil {
				return err
			}
			fp, _ = tlsutil.Fingerprint(cert)
		}
		h, _ := api.New(api.Deps{Version: version, Cfg: cfg, CfgPath: cfgPath, DB: db, FS: fsys, Providers: reg, Jobs: jm, Limits: lim, Cookies: ck, Secret: box, Tools: inst, Updates: ups, Fingerprint: fp})
		srv := &http.Server{Addr: cfg.Server.Listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			srv.Shutdown(sctx)
		}()
		defer func() { stop(); <-jobsDone }() // runners save their resume state before the DB closes
		fmt.Fprintf(os.Stderr, "dmn %s listening on %s (tls: %s), downloads in %s\n", version, cfg.Server.Listen, mode, cfg.Paths.Downloads)
		switch mode {
		case "self-signed":
			srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
			if err := srv.ListenAndServeTLS("", ""); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
		case "off":
			if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
		case "acme":
			tok, err := box.Open("server.dnstoken", cfg.Server.DNSToken)
			if err != nil {
				return err
			}
			if cfg.Server.DNSToken != "" && !secret.IsSealed(cfg.Server.DNSToken) { // seal a hand-written token
				if err := config.Set(cfgPath, "server.dnstoken", box.Seal("server.dnstoken", tok)); err != nil {
					return err
				}
			}
			tc, h80, err := acme.Setup(ctx, acme.Options{Domain: cfg.Server.Domain, Email: cfg.Server.Email,
				DNSProvider: cfg.Server.DNSProvider, DNSToken: tok, StorageDir: filepath.Join(cfg.Paths.Data, "acme"), CA: cfg.Server.ACMECA, CARoot: cfg.Server.ACMECARoot})
			if err != nil {
				return err
			}
			s80 := &http.Server{Addr: cfg.Server.HTTPListen, Handler: h80, ReadHeaderTimeout: 10 * time.Second}
			go func() {
				if err := s80.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
					fmt.Fprintf(os.Stderr, "acme: %s unavailable (%v): HTTP-01 is off; TLS-ALPN on the HTTPS port or DNS-01 (server.dnsprovider) still work\n", cfg.Server.HTTPListen, err)
				}
			}()
			go func() { <-ctx.Done(); s80.Close() }()
			srv.TLSConfig = tc
			if err := srv.ListenAndServeTLS("", ""); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
		default:
			return fmt.Errorf("server.tls %q: use auto, off, self-signed or acme", mode)
		}
		return nil
	}
	return c
}

// tlsMode resolves "auto": plain HTTP on loopback (local CLI, reverse proxy), self-signed otherwise.
func tlsMode(cfg *config.Config) string {
	if cfg.Server.TLS == "" || cfg.Server.TLS == "auto" {
		if loopback(cfg.Server.Listen) {
			return "off"
		}
		return "self-signed"
	}
	return cfg.Server.TLS
}

// killSwitch returns a gate that closes while the bound interface (e.g. wg0) is down or has no
// address, so torrent traffic never falls back to the normal connection. nil when not bound to an
// interface by name.
func killSwitch(bind string) func() string {
	if _, err := net.InterfaceByName(bind); bind == "" || err != nil {
		return nil
	}
	return func() string {
		ifc, err := net.InterfaceByName(bind)
		if err != nil || ifc.Flags&net.FlagUp == 0 {
			return "kill switch: " + bind + " is down"
		}
		if addrs, _ := ifc.Addrs(); len(addrs) == 0 {
			return "kill switch: " + bind + " has no address"
		}
		return ""
	}
}

// loadBlocklist reads a P2P-format list from a file or an http(s) URL (gzip allowed) and refreshes
// a URL daily. Errors are logged; the previous list stays in force.
func loadBlocklist(ctx context.Context, src string, bl *engine.Blocklist) {
	load := func() {
		var r io.ReadCloser
		var err error
		if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
			var resp *http.Response
			if resp, err = netguard.Client().Get(src); err == nil {
				if resp.StatusCode != 200 {
					resp.Body.Close()
					err = fmt.Errorf("HTTP %d", resp.StatusCode)
				}
				r = resp.Body
			}
		} else {
			r, err = os.Open(src)
		}
		if err == nil {
			defer r.Close()
			br := bufio.NewReader(r)
			var in io.Reader = br
			if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
				var gz *gzip.Reader
				if gz, err = gzip.NewReader(br); err == nil {
					in = gz
				}
			}
			if err == nil {
				var n int
				if n, err = bl.Load(in); err == nil {
					fmt.Fprintf(os.Stderr, "blocklist: %d ranges loaded\n", n)
				}
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "blocklist:", err)
		}
	}
	load()
	if !strings.HasPrefix(src, "http") {
		return
	}
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			load()
		}
	}
}

// newEngine builds the torrent engine from the torrent provider settings (scope §5.9).
func newEngine(cfg *config.Config, lim *limits.Limits, bl *engine.Blocklist) (engine.Engine, error) {
	bind := settingStr(cfg, "torrent", "bind", "")
	if ifc, err := net.InterfaceByName(bind); err == nil { // an interface name such as wg0
		addrs, _ := ifc.Addrs()
		if len(addrs) == 0 {
			return nil, fmt.Errorf("interface %s has no address (VPN down?)", bind)
		}
		bind = addrs[0].(*net.IPNet).IP.String()
	}
	return engine.New(engine.Options{
		DataDir: cfg.Paths.Downloads, ListenHost: bind, ListenPort: setting(cfg, "torrent", "port", 42069),
		NoDHT: !settingBool(cfg, "torrent", "dht", true), NoPEX: !settingBool(cfg, "torrent", "pex", true),
		NoUTP: !settingBool(cfg, "torrent", "utp", true), NoIPv6: !settingBool(cfg, "torrent", "ipv6", true),
		Encryption: settingStr(cfg, "torrent", "encryption", "prefer"), MaxPeers: setting(cfg, "torrent", "maxPeers", 0),
		Anonymous:   settingBool(cfg, "torrent", "anonymous", false),
		DownLimiter: lim.Down, UpLimiter: lim.Up, Blocklist: bl, Control: netguard.Safe,
		DHTNodesFile: filepath.Join(cfg.Paths.Data, "dht-nodes.dat"),
		Socks5:       settingStr(cfg, "torrent", "socks5", ""),
		UPnP:         settingBool(cfg, "torrent", "upnp", false),
	})
}

func settingFloat(cfg *config.Config, prov, key string, def float64) float64 {
	switch v := cfg.Providers[prov].Settings[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func settingStr(cfg *config.Config, prov, key, def string) string {
	if v, ok := cfg.Providers[prov].Settings[key].(string); ok {
		return v
	}
	return def
}

func settingBool(cfg *config.Config, prov, key string, def bool) bool {
	switch v := cfg.Providers[prov].Settings[key].(type) {
	case bool:
		return v
	case string:
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

// setting reads an integer provider setting (YAML and env give different number types).
func setting(cfg *config.Config, prov, key string, def int) int {
	switch v := cfg.Providers[prov].Settings[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

func randomToken(prefix string) string {
	b := make([]byte, 32)
	rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

func setupCmd() *cobra.Command {
	c := &cobra.Command{Use: "setup", Short: "First-run setup: signing key and an API token (safe to re-run)"}
	name := c.Flags().String("name", "owner", "token name")
	scope := c.Flags().String("scope", "admin", "token scope: read, write or admin")
	c.RunE = func(*cobra.Command, []string) error {
		if *scope != "read" && *scope != "write" && *scope != "admin" {
			return fmt.Errorf("scope must be read, write or admin")
		}
		_, cfg, err := config.Load(cfgPath, nil)
		if err != nil {
			return err
		}
		if cfg.Auth.Signkey == "" {
			if err := config.Set(cfgPath, "auth.signkey", randomToken("")); err != nil {
				return err
			}
		}
		tok, err := api.AddToken(cfgPath, *name, *scope)
		if err != nil {
			return err
		}
		return show(map[string]string{"config": cfgPath, "name": *name, "scope": *scope, "token": tok}, func() {
			fmt.Printf("Config: %s\nToken %q (%s), shown once:\n\n  %s\n\nUse it with: export DMN_TOKEN=%s\n", cfgPath, *name, *scope, tok, tok)
		})
	}
	return c
}

func configCmd() *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "View and change settings"}
	get := &cobra.Command{Use: "get <key>", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, a []string) error {
		k, _, err := config.Load(cfgPath, nil)
		if err != nil {
			return err
		}
		v, ok := config.Effective(k)[a[0]]
		if !ok {
			return fmt.Errorf("%s is not set", a[0])
		}
		return show(v, func() { fmt.Println(v) })
	}}
	set := &cobra.Command{Use: "set <key> <value>", Args: cobra.ExactArgs(2), RunE: func(_ *cobra.Command, a []string) error {
		if strings.HasPrefix(a[0], "auth.") {
			return fmt.Errorf("auth settings are managed by `dmn setup`")
		}
		var v any = a[1]
		if a[1] == "true" || a[1] == "false" {
			v = a[1] == "true"
		}
		return config.Set(cfgPath, a[0], v)
	}}
	showC := &cobra.Command{Use: "show", Short: "Show settings (--effective: merged from flags, env, file, defaults)"}
	eff := showC.Flags().Bool("effective", false, "include env and defaults")
	showC.RunE = func(*cobra.Command, []string) error {
		k, _, err := config.Load(cfgPath, nil)
		if err != nil {
			return err
		}
		all := config.Effective(k)
		if !*eff {
			fk, _, _ := config.LoadFile(cfgPath)
			for key := range all {
				if !fk.Exists(key) {
					delete(all, key)
				}
			}
		}
		return show(all, func() {
			keys := make([]string, 0, len(all))
			for k := range all {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Printf("%s = %v\n", k, all[k])
			}
		})
	}
	c.AddCommand(get, set, showC)
	return c
}

func doctorCmd() *cobra.Command {
	var self bool
	c := &cobra.Command{Use: "doctor", Short: "Report helper tools; --selftest proves downloads work (local fixtures only)", RunE: func(*cobra.Command, []string) error {
		if self {
			cs := selftest.Run(context.Background())
			failed := 0
			for _, c := range cs {
				if !c.OK {
					failed++
				}
			}
			if err := show(cs, func() {
				rows := [][]string{{"CHECK", "RESULT", "TIME", "DETAIL"}}
				for _, c := range cs {
					r := "ok"
					if !c.OK {
						r = "FAIL"
					}
					rows = append(rows, []string{c.Name, r, c.Took, c.Detail})
				}
				table(rows)
			}); err != nil {
				return err
			}
			if failed > 0 {
				return fmt.Errorf("%d self-test check(s) failed", failed)
			}
			return nil
		}
		_, cfg, err := config.Load(cfgPath, nil)
		if err != nil {
			return err
		}
		ts := tools.Doctor(cfg.Paths.Data)
		return show(ts, func() {
			rows := [][]string{{"TOOL", "STATUS", "AFFECTS"}}
			for _, t := range ts {
				st := "missing"
				if t.Found {
					st = "ok " + t.Path
				}
				rows = append(rows, []string{t.Name, st, t.Features})
			}
			table(rows)
		})
	}}
	c.Flags().BoolVar(&self, "selftest", false, "run an offline end-to-end download, stream and torrent test")
	return c
}

func versionCmd() *cobra.Command {
	return &cobra.Command{Use: "version", RunE: func(*cobra.Command, []string) error {
		return show(map[string]string{"version": version}, func() { fmt.Println(version) })
	}}
}

// ---- API client commands ----

func call(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimRight(srvURL, "/")+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if srvToken != "" {
		req.Header.Set("Authorization", "Bearer "+srvToken)
	}
	resp, err := httpClient(30 * time.Second).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct{ Detail string }
		if json.Unmarshal(b, &e) == nil && e.Detail != "" {
			return fmt.Errorf("%s (HTTP %d)", e.Detail, resp.StatusCode)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(b))
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

func lsCmd() *cobra.Command {
	return &cobra.Command{Use: "ls [path]", Short: "List downloads, or a folder when a path is given", Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, a []string) error {
			if len(a) == 0 {
				var jobs []store.Job
				if err := call("GET", "/v1/jobs", nil, &jobs); err != nil {
					return err
				}
				return show(jobs, func() {
					rows := [][]string{{"ID", "KIND", "STATE", "PROGRESS", "NAME"}}
					for _, j := range jobs {
						pct := "-"
						if j.Total > 0 {
							pct = fmt.Sprintf("%.1f%%", 100*float64(j.Done)/float64(j.Total))
						}
						name := j.Name
						if j.Error != "" {
							name += "  (" + j.Error + ")"
						}
						rows = append(rows, []string{j.ID, j.Kind, j.State, pct, name})
					}
					table(rows)
				})
			}
			var es []fsapi.Entry
			if err := call("GET", "/v1/fs?path="+url.QueryEscape(a[0]), nil, &es); err != nil {
				return err
			}
			return show(es, func() {
				rows := [][]string{{"NAME", "SIZE", "MODIFIED"}}
				for _, e := range es {
					n, size := e.Name, fmt.Sprint(e.Size)
					if e.Dir {
						n, size = n+"/", "-"
					}
					rows = append(rows, []string{n, size, e.ModTime.Format(time.DateTime)})
				}
				table(rows)
			})
		}}
}

func statCmd() *cobra.Command {
	return &cobra.Command{Use: "stat <path>", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, a []string) error {
		var e fsapi.Entry
		if err := call("GET", "/v1/fs/stat?path="+url.QueryEscape(a[0]), nil, &e); err != nil {
			return err
		}
		return show(e, func() { fmt.Printf("%s\n  dir: %v\n  size: %d\n  modified: %s\n", e.Path, e.Dir, e.Size, e.ModTime) })
	}}
}

func mkdirCmd() *cobra.Command {
	return &cobra.Command{Use: "mkdir <path>", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, a []string) error {
		return call("POST", "/v1/fs/mkdir", map[string]string{"path": a[0]}, nil)
	}}
}

func mvCmd() *cobra.Command {
	return &cobra.Command{Use: "mv <from> <to>", Args: cobra.ExactArgs(2), RunE: func(_ *cobra.Command, a []string) error {
		return call("POST", "/v1/fs/move", map[string]string{"from": a[0], "to": a[1]}, nil)
	}}
}

func cpCmd() *cobra.Command {
	return &cobra.Command{Use: "cp <from> <to>", Short: "Copy a file or folder on the server (never overwrites)", Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, a []string) error {
			return call("POST", "/v1/fs/copy", map[string]string{"from": a[0], "to": a[1]}, nil)
		}}
}

func zipCmd() *cobra.Command {
	c := &cobra.Command{Use: "zip <folder>", Short: "Download a server folder as a ZIP", Args: cobra.ExactArgs(1)}
	out := c.Flags().StringP("output", "o", "", "output file (default: <folder>.zip)")
	c.RunE = func(_ *cobra.Command, a []string) error {
		name := *out
		if name == "" {
			name = filepath.Base(strings.TrimRight(a[0], "/")) + ".zip"
			if name == ".zip" || name == "/.zip" {
				name = "downloads.zip"
			}
		}
		req, err := http.NewRequest("GET", strings.TrimRight(srvURL, "/")+"/v1/zip/"+strings.TrimPrefix(a[0], "/"), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+srvToken)
		resp, err := httpClient(0).Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(b))
		}
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // never clobber a local file
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, resp.Body); err != nil {
			f.Close()
			return err
		}
		return show(map[string]string{"file": name}, func() { fmt.Println(name) })
	}
	return c
}

func rmCmd() *cobra.Command {
	return &cobra.Command{Use: "rm <path>", Short: "Delete a file or folder on the server", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, a []string) error {
			return call("DELETE", "/v1/fs?path="+url.QueryEscape(a[0]), nil, nil)
		}}
}

func addCmd() *cobra.Command {
	c := &cobra.Command{Use: "add <url | magnet | file.torrent>", Short: "Add a download", Args: cobra.ExactArgs(1)}
	mode := c.Flags().String("mode", "standard", "standard or stream (watch while it downloads)")
	headers := c.Flags().StringArrayP("header", "H", nil, `request header, e.g. -H "Cookie: a=b" (repeatable)`)
	seed := c.Flags().String("seed", "", "torrents: stop, ratio:1.5, time:60 (minutes) or forever")
	prio := c.Flags().Int("priority", 0, "queue priority: higher starts first")
	format := c.Flags().StringP("format", "f", "", "video pages: preset (best, 1080p, 720p, 480p, m4a, opus, mp3) or a format id from `dmn formats`")
	unpack := c.Flags().Bool("extract", false, "unpack archives when done (default from jobs.extract)")
	delArc := c.Flags().Bool("delete-archive", false, "with --extract: delete the archive after unpacking")
	c.RunE = func(_ *cobra.Command, a []string) error {
		req := jobs.AddRequest{Source: a[0], Mode: *mode, Header: map[string]string{}, Priority: *prio, Format: *format}
		if c.Flags().Changed("extract") || *delArc {
			req.Post = &jobs.Post{Extract: *unpack || *delArc, DeleteArchive: *delArc}
		}
		if *seed != "" {
			p, err := parseSeed(*seed)
			if err != nil {
				return err
			}
			req.Seed = &p
		}
		if strings.HasSuffix(strings.ToLower(a[0]), ".torrent") {
			if b, err := os.ReadFile(a[0]); err == nil { // a local file; otherwise treat it as a URL
				req.Source, req.Torrent = "", b
			}
		}
		for _, h := range *headers {
			k, v, ok := strings.Cut(h, ":")
			if !ok {
				return fmt.Errorf("header %q: want \"Name: value\"", h)
			}
			req.Header[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		var j store.Job
		if err := call("POST", "/v1/jobs", req, &j); err != nil {
			return err
		}
		return show(j, func() { fmt.Printf("%s  %s  %s\n", j.ID, j.Kind, j.Source) })
	}
	return c
}

// parseRate reads "0", "500K", "5M", "1.5G" (binary units, bytes per second).
func parseRate(s string) (int64, error) {
	u := strings.ToUpper(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(s), "/s"), "B"))
	mult := 1.0
	if u != "" {
		switch u[len(u)-1] {
		case 'K':
			mult, u = 1<<10, u[:len(u)-1]
		case 'M':
			mult, u = 1<<20, u[:len(u)-1]
		case 'G':
			mult, u = 1<<30, u[:len(u)-1]
		}
	}
	f, err := strconv.ParseFloat(u, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("bad rate %q: use e.g. 0, 500K, 5M", s)
	}
	return int64(f * mult), nil
}

func limitsCmd() *cobra.Command {
	c := &cobra.Command{Use: "limits", Short: "Show or set global bandwidth limits (e.g. --down 5M --up 500K; 0 = unlimited)"}
	down := c.Flags().String("down", "", "download limit for all downloads together")
	up := c.Flags().String("up", "", "torrent upload limit")
	c.RunE = func(*cobra.Command, []string) error {
		var cur struct{ Down, Up int64 }
		if err := call("GET", "/v1/limits", nil, &cur); err != nil {
			return err
		}
		if *down != "" || *up != "" {
			var err error
			if *down != "" {
				if cur.Down, err = parseRate(*down); err != nil {
					return err
				}
			}
			if *up != "" {
				if cur.Up, err = parseRate(*up); err != nil {
					return err
				}
			}
			if err := call("PUT", "/v1/limits", map[string]int64{"down": cur.Down, "up": cur.Up}, nil); err != nil {
				return err
			}
		}
		h := func(n int64) string {
			if n == 0 {
				return "unlimited"
			}
			return fmt.Sprintf("%.1f MiB/s", float64(n)/(1<<20))
		}
		return show(map[string]int64{"down": cur.Down, "up": cur.Up}, func() { fmt.Printf("down: %s\nup:   %s\n", h(cur.Down), h(cur.Up)) })
	}
	return c
}

func peersCmd() *cobra.Command {
	return &cobra.Command{Use: "peers <id>", Short: "Connected peers of a running torrent", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, a []string) error {
			var ps []engine.Peer
			if err := call("GET", "/v1/jobs/"+url.PathEscape(a[0])+"/peers", nil, &ps); err != nil {
				return err
			}
			return show(ps, func() {
				rows := [][]string{{"ADDRESS", "CLIENT", "SOURCE", "HAS", "DOWN", "UP"}}
				for _, p := range ps {
					rows = append(rows, []string{p.Addr, p.Client, p.Source, fmt.Sprintf("%.0f%%", p.Progress*100),
						fmt.Sprintf("%.0f KiB/s", p.DownRate/1024), fmt.Sprintf("%.0f KiB/s", p.UpRate/1024)})
				}
				table(rows)
			})
		}}
}

func piecesCmd() *cobra.Command {
	return &cobra.Command{Use: "pieces <id>", Short: "Piece map, availability and why a torrent is slow", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, a []string) error {
			var v jobs.PieceView
			if err := call("GET", "/v1/jobs/"+url.PathEscape(a[0])+"/pieces", nil, &v); err != nil {
				return err
			}
			return show(v, func() {
				fmt.Printf("%s\n", pieceBar(v.Runs, v.Pieces, 64))
				fmt.Printf("pieces %d/%d  peers %d  seeders %d  availability %.2f\n", v.Complete, v.Pieces, v.Peers, v.Seeders, v.Availability)
				if v.Hint != "" {
					fmt.Println(v.Hint)
				}
			})
		}}
}

// pieceBar draws the piece map in width cells: █ complete, ▒ partly, ░ missing.
func pieceBar(runs []engine.PieceRun, n, width int) string {
	if n == 0 {
		return ""
	}
	state := make([]byte, 0, n) // 2 complete, 1 partial, 0 missing
	for _, r := range runs {
		s := byte(0)
		if r.Complete {
			s = 2
		} else if r.Partial {
			s = 1
		}
		for range r.Length {
			state = append(state, s)
		}
	}
	var b strings.Builder
	for c := range width {
		lo, hi := c*len(state)/width, max((c+1)*len(state)/width, c*len(state)/width+1)
		done, part := 0, 0
		for _, s := range state[lo:min(hi, len(state))] {
			done += int(s / 2)
			part += int(s % 2)
		}
		switch {
		case done == hi-lo:
			b.WriteString("█")
		case done+part > 0:
			b.WriteString("▒")
		default:
			b.WriteString("░")
		}
	}
	return b.String()
}

func trackersCmd() *cobra.Command {
	c := &cobra.Command{Use: "trackers <id>", Short: "Show trackers of a running torrent; --add or --remove to change them", Args: cobra.ExactArgs(1)}
	add := c.Flags().StringArray("add", nil, "tracker URL to add (repeatable)")
	rm := c.Flags().StringArray("remove", nil, "tracker URL to remove (repeatable)")
	c.RunE = func(_ *cobra.Command, a []string) error {
		path := "/v1/jobs/" + url.PathEscape(a[0]) + "/trackers"
		var tiers [][]string
		if err := call("GET", path, nil, &tiers); err != nil {
			return err
		}
		if len(*add) > 0 || len(*rm) > 0 {
			var next [][]string
			for _, tier := range tiers {
				var keep []string
				for _, u := range tier {
					if !slices.Contains(*rm, u) {
						keep = append(keep, u)
					}
				}
				if len(keep) > 0 {
					next = append(next, keep)
				}
			}
			for _, u := range *add {
				next = append(next, []string{u}) // each added tracker gets its own tier
			}
			if err := call("PUT", path, next, nil); err != nil {
				return err
			}
			tiers = next
		}
		return show(tiers, func() {
			for i, tier := range tiers {
				for _, u := range tier {
					fmt.Printf("tier %d  %s\n", i, u)
				}
			}
		})
	}
	return c
}

func jobAction(name string) *cobra.Command {
	return &cobra.Command{Use: name + " <id>", Short: name + " a download", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, a []string) error {
			return call("POST", "/v1/jobs/"+url.PathEscape(a[0])+"/"+name, nil, nil)
		}}
}

func modeCmd() *cobra.Command {
	return &cobra.Command{Use: "mode <id> <standard|stream>", Short: "Switch download mode while it runs", Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, a []string) error {
			return call("PUT", "/v1/jobs/"+url.PathEscape(a[0])+"/mode", map[string]string{"mode": a[1]}, nil)
		}}
}

func cancelCmd() *cobra.Command {
	c := &cobra.Command{Use: "cancel <id>", Short: "Stop and forget a download", Args: cobra.ExactArgs(1)}
	del := c.Flags().Bool("delete-files", false, "also delete its files")
	c.RunE = func(_ *cobra.Command, a []string) error {
		return call("DELETE", "/v1/jobs/"+url.PathEscape(a[0])+"?deleteFiles="+strconv.FormatBool(*del), nil, nil)
	}
	return c
}

func watchCmd() *cobra.Command {
	return &cobra.Command{Use: "watch <id | /path>", Short: "Print a short-lived stream URL for a player (a job also while it downloads)", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, a []string) error {
			var out map[string]string
			var err error
			if strings.HasPrefix(a[0], "/") {
				err = call("POST", "/v1/fs/sign", map[string]any{"path": a[0]}, &out)
			} else {
				err = call("POST", "/v1/jobs/"+url.PathEscape(a[0])+"/sign", nil, &out)
			}
			if err != nil {
				return err
			}
			u := strings.TrimRight(srvURL, "/") + out["url"]
			return show(map[string]string{"url": u}, func() { fmt.Println(u) })
		}}
}

func providersCmd() *cobra.Command {
	c := &cobra.Command{Use: "providers", Short: "Sources and destinations"}
	list := &cobra.Command{Use: "list", RunE: func(*cobra.Command, []string) error {
		var ps []provider.Info
		if err := call("GET", "/v1/providers", nil, &ps); err != nil {
			return err
		}
		return show(ps, func() {
			rows := [][]string{{"NAME", "KIND", "STATUS", "NOTE"}}
			for _, p := range ps {
				rows = append(rows, []string{p.Name, string(p.Kind), string(p.Status), p.Reason})
			}
			table(rows)
		})
	}}
	toggle := func(use string, on bool) *cobra.Command {
		return &cobra.Command{Use: use + " <name>", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, a []string) error {
			return call("PUT", "/v1/providers/"+url.PathEscape(a[0])+"/enabled", map[string]bool{"enabled": on}, nil)
		}}
	}
	cfgC := providerConfigCmd()
	c.AddCommand(list, toggle("enable", true), toggle("disable", false), cfgC)
	return c
}

// parseSeed reads stop, forever, ratio:1.5 or time:60.
func parseSeed(s string) (jobs.SeedPolicy, error) {
	kind, arg, _ := strings.Cut(s, ":")
	p := jobs.SeedPolicy{Policy: kind}
	var err error
	switch kind {
	case "stop", "forever":
		return p, nil
	case "ratio":
		p.Ratio, err = strconv.ParseFloat(arg, 64)
	case "time":
		p.Minutes, err = strconv.Atoi(arg)
	default:
		err = errors.New("unknown policy")
	}
	if err != nil {
		return p, fmt.Errorf("--seed %q: use stop, ratio:1.5, time:60 or forever", s)
	}
	return p, nil
}

func priorityCmd() *cobra.Command {
	return &cobra.Command{Use: "priority <id> <n>", Short: "Reorder the queue: higher starts first", Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, a []string) error {
			n, err := strconv.Atoi(a[1])
			if err != nil {
				return fmt.Errorf("priority must be a number")
			}
			return call("PUT", "/v1/jobs/"+url.PathEscape(a[0])+"/priority", map[string]int{"priority": n}, nil)
		}}
}

func filesCmd() *cobra.Command {
	c := &cobra.Command{Use: "files <id>", Short: "List a torrent's files; --set 2=skip changes one (skip, normal, high)", Args: cobra.ExactArgs(1)}
	set := c.Flags().StringArray("set", nil, "index=priority (repeatable)")
	c.RunE = func(_ *cobra.Command, a []string) error {
		base := "/v1/jobs/" + url.PathEscape(a[0]) + "/files"
		for _, s := range *set {
			i, p, ok := strings.Cut(s, "=")
			if !ok {
				return fmt.Errorf("--set %q: want index=priority", s)
			}
			if err := call("PUT", base+"/"+url.PathEscape(i)+"/priority", map[string]string{"priority": p}, nil); err != nil {
				return err
			}
		}
		var fs []engine.File
		if err := call("GET", base, nil, &fs); err != nil {
			return err
		}
		return show(fs, func() {
			rows := [][]string{{"#", "DONE", "SIZE", "PATH"}}
			for i, f := range fs {
				pct := 100.0
				if f.Length > 0 {
					pct = 100 * float64(f.Completed) / float64(f.Length)
				}
				rows = append(rows, []string{fmt.Sprint(i), fmt.Sprintf("%.0f%%", pct), fmt.Sprint(f.Length), f.Path})
			}
			table(rows)
		})
	}
	return c
}

func learnedCmd() *cobra.Command {
	c := &cobra.Command{Use: "learned", Short: "Swarm memory: trackers remembered from completed public torrents"}
	forget := c.Flags().Bool("forget", false, "forget them all")
	c.RunE = func(*cobra.Command, []string) error {
		if *forget {
			return call("DELETE", "/v1/swarm/trackers", nil, nil)
		}
		var ts []store.LearnedTracker
		if err := call("GET", "/v1/swarm/trackers", nil, &ts); err != nil {
			return err
		}
		return show(ts, func() {
			rows := [][]string{{"WORKED", "FAILED", "LAST OK", "TRACKER"}}
			for _, t := range ts {
				rows = append(rows, []string{fmt.Sprint(t.Success), fmt.Sprint(t.Failure), time.Unix(t.LastOK, 0).Format(time.DateOnly), t.URL})
			}
			table(rows)
		})
	}
	return c
}

// videoRunner starts the SSRF-guarded proxy that yt-dlp must use (it makes its own connections)
// and returns a lookup that finds yt-dlp, ffmpeg and deno at the time of each job.
func videoRunner(ctx context.Context, dataDir string) func() *video.Runner {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "video: no proxy, video downloads disabled:", err)
		return nil
	}
	px := &http.Server{Handler: netguard.NewProxy(), ReadHeaderTimeout: 10 * time.Second}
	go px.Serve(ln)
	go func() { <-ctx.Done(); px.Close() }()
	proxyURL := "http://" + ln.Addr().String()
	return func() *video.Runner {
		bin, ok := tools.Find("yt-dlp", dataDir)
		if !ok {
			return nil
		}
		r := &video.Runner{Bin: bin, Proxy: proxyURL}
		if p, ok := tools.Find("ffmpeg", dataDir); ok {
			r.FFmpeg = p
		}
		if p, ok := tools.Find("deno", dataDir); ok {
			r.Deno = p
		}
		return r
	}
}

func formatsCmd() *cobra.Command {
	return &cobra.Command{Use: "formats <url>", Short: "Show formats and presets for a video page", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, a []string) error {
			var res jobs.ProbeResult
			if err := call("POST", "/v1/probe", map[string]string{"url": a[0]}, &res); err != nil {
				return err
			}
			return show(res, func() {
				if res.Video == nil {
					fmt.Printf("%s: %s %s\n", res.Kind, res.Name, sizeStr(res.Size))
					return
				}
				fmt.Printf("%s (%s)\n\npresets: ", res.Video.Title, res.Video.Extractor)
				for _, p := range res.Presets {
					fmt.Print(p.Name, " ")
				}
				fmt.Println("\n\nuse one with: dmn add --format <preset or id> <url>")
				rows := [][]string{{"ID", "EXT", "RESOLUTION", "FPS", "VIDEO", "AUDIO", "SIZE"}}
				for _, f := range res.Video.Formats {
					rows = append(rows, []string{f.ID, f.Ext, f.Resolution, fmt.Sprint(f.FPS), f.VCodec, f.ACodec, sizeStr(f.Size)})
				}
				table(rows)
			})
		}}
}

func sizeStr(n int64) string {
	if n <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

func cookiesCmd() *cobra.Command {
	c := &cobra.Command{Use: "cookies", Short: "Browser cookies for sites that need a login (stored encrypted, sent over HTTPS only)"}
	imp := &cobra.Command{Use: "import <cookies.txt>", Short: "Import a Netscape cookies.txt for one domain", Args: cobra.ExactArgs(1)}
	domain := imp.Flags().String("domain", "", "domain the cookies are for, e.g. example.com (required)")
	ua := imp.Flags().String("user-agent", "", "the browser's User-Agent, sent together with the cookies")
	imp.MarkFlagRequired("domain")
	imp.RunE = func(_ *cobra.Command, a []string) error {
		b, err := os.ReadFile(a[0])
		if err != nil {
			return err
		}
		var out map[string]int
		if err := call("POST", "/v1/cookies", map[string]string{"domain": *domain, "content": string(b), "userAgent": *ua}, &out); err != nil {
			return err
		}
		return show(out, func() { fmt.Printf("%d cookies imported for %s\n", out["imported"], *domain) })
	}
	list := &cobra.Command{Use: "list", RunE: func(*cobra.Command, []string) error {
		var l []cookies.Summary
		if err := call("GET", "/v1/cookies", nil, &l); err != nil {
			return err
		}
		return show(l, func() {
			rows := [][]string{{"DOMAIN", "COOKIES", "EXPIRED", "USER-AGENT"}}
			for _, s := range l {
				rows = append(rows, []string{s.Domain, fmt.Sprint(s.Count), fmt.Sprint(s.Expired), fmt.Sprint(s.UserAgent)})
			}
			table(rows)
		})
	}}
	rm := &cobra.Command{Use: "rm <domain>", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, a []string) error {
		return call("DELETE", "/v1/cookies/"+url.PathEscape(a[0]), nil, nil)
	}}
	c.AddCommand(imp, list, rm)
	return c
}

// lanAddr picks the address a phone on the same network can reach: the listen host when it is
// specific, else the first private IPv4 of an interface that is up.
func lanAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if ip := net.ParseIP(host); host != "" && (ip == nil || !ip.IsUnspecified()) {
		return listen
	}
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && ipn.IP.IsPrivate() {
				return net.JoinHostPort(ipn.IP.String(), port)
			}
		}
	}
	return listen
}

func pairCmd() *cobra.Command {
	c := &cobra.Command{Use: "pair", Short: "Show a QR code that connects the Android app (new token, URL, certificate fingerprint)"}
	name := c.Flags().String("name", "phone", "device name; pairing again with the same name replaces its token")
	scope := c.Flags().String("scope", "write", "token scope for the device: read, write or admin")
	override := c.Flags().String("url", "", "server URL as the phone sees it (default: this machine's LAN address)")
	c.RunE = func(*cobra.Command, []string) error {
		_, cfg, err := config.Load(cfgPath, nil)
		if err != nil {
			return err
		}
		mode := tlsMode(cfg)
		scheme, fp := "https", ""
		switch mode {
		case "off":
			scheme = "http"
		case "self-signed":
			cert, err := tlsutil.SelfSigned(filepath.Join(cfg.Paths.Data, "tls"))
			if err != nil {
				return err
			}
			fp, _ = tlsutil.Fingerprint(cert)
		}
		u := *override
		if u == "" {
			host := lanAddr(cfg.Server.Listen)
			if mode == "acme" && cfg.Server.Domain != "" {
				host = cfg.Server.Domain
			}
			u = scheme + "://" + host
		}
		tok, err := api.AddToken(cfgPath, *name, *scope)
		if err != nil {
			return err
		}
		payload := api.PairPayload(u, tok, fp)
		return show(map[string]string{"payload": payload, "url": u, "fingerprint": fp}, func() {
			qrterminal.GenerateHalfBlock(payload, qrterminal.L, os.Stdout)
			fmt.Printf("\nScan with the app. Server %s, device %q (%s).\n", u, *name, *scope)
			if fp != "" {
				fmt.Printf("Certificate fingerprint (SHA-256): %s\n", fp)
			}
			if mode == "off" && !loopback(cfg.Server.Listen) {
				fmt.Println("Warning: TLS is off. Only do this behind a reverse proxy, tunnel or VPN.")
			}
		})
	}
	return c
}

// httpClient talks to the server; with --fingerprint it accepts exactly that certificate (the same
// pinning the app does), otherwise normal TLS verification applies.
func httpClient(timeout time.Duration) *http.Client {
	c := &http.Client{Timeout: timeout}
	if srvFP != "" {
		want := strings.ToLower(strings.ReplaceAll(srvFP, ":", ""))
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true,
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 {
					return errors.New("no certificate")
				}
				sum := sha256.Sum256(raw[0])
				if hex.EncodeToString(sum[:]) != want {
					return errors.New("server certificate does not match --fingerprint")
				}
				return nil
			}}}
	}
	return c
}

// destinations builds cloud destinations from the provider settings at the time of each copy, so
// signing in or changing the folder needs no restart.
func destinations(cfg *config.Config, reg *provider.Registry, box *secret.Box) func(string) (*jobs.Dest, error) {
	return func(name string) (*jobs.Dest, error) {
		if name != "gdrive" {
			return nil, fmt.Errorf("unknown destination %q", name)
		}
		bin, ok := tools.Find("rclone", cfg.Paths.Data)
		if !ok {
			return nil, fmt.Errorf("rclone is not installed: run `dmn doctor`")
		}
		str := func(k string) string { s, _ := reg.Setting("gdrive", k).(string); return s }
		sec, err := box.Open("gdrive.clientSecret", str("clientSecret"))
		if err != nil {
			return nil, err
		}
		tok, err := box.Open("gdrive.token", str("token"))
		if err != nil {
			return nil, err
		}
		if tok == "" {
			return nil, fmt.Errorf("not connected to Google Drive: use Connect Google Drive (or `dmn providers config gdrive --signin`)")
		}
		folder := str("folder")
		if folder == "" {
			folder = "dmn"
		}
		return &jobs.Dest{
			Runner: rclone.Runner{Bin: bin},
			Remote: rclone.Remote{Name: "gdrive", Params: gdrive.Remote(gdrive.Client{ID: str("clientId"), Secret: sec}, tok, "")},
			Folder: folder,
			SaveToken: func(t string) error {
				v := box.Seal("gdrive.token", t)
				reg.SetSetting("gdrive", "token", v)
				return config.Set(cfgPath, "providers.gdrive.settings.token", v)
			},
		}, nil
	}
}

func copyCmd() *cobra.Command {
	c := &cobra.Command{Use: "copy <job id | /path>", Short: "Copy a finished download to Google Drive", Args: cobra.ExactArgs(1)}
	c.Flags().Bool("drive", true, "copy to Google Drive (the only destination for now)")
	move := c.Flags().Bool("move", false, "delete the local copy after the upload is verified")
	folder := c.Flags().String("folder", "", "Drive folder (default from the gdrive settings)")
	c.RunE = func(_ *cobra.Command, a []string) error {
		p := a[0]
		if !strings.HasPrefix(p, "/") { // a job id: copy what it downloaded
			var j store.Job
			if err := call("GET", "/v1/jobs/"+url.PathEscape(p), nil, &j); err != nil {
				return err
			}
			if j.Path == "" || j.State != "done" {
				return fmt.Errorf("job %s has no finished file yet (state %s)", p, j.State)
			}
			p = j.Path
		}
		var j store.Job
		if err := call("POST", "/v1/copy", jobs.CopyRequest{Path: p, Dest: "gdrive", Move: *move, Folder: *folder}, &j); err != nil {
			return err
		}
		return show(j, func() { fmt.Printf("%s  copy  %s\n", j.ID, j.Path) })
	}
	return c
}

func providerConfigCmd() *cobra.Command {
	c := &cobra.Command{Use: "config <name>", Short: "Show or change a provider's settings; --signin connects Google Drive", Args: cobra.ExactArgs(1)}
	set := c.Flags().StringArray("set", nil, "key=value (repeatable); numbers and true/false are converted")
	signin := c.Flags().Bool("signin", false, "gdrive: connect your Google account (device code)")
	c.RunE = func(_ *cobra.Command, a []string) error {
		base := "/v1/providers/" + url.PathEscape(a[0])
		if len(*set) > 0 {
			body := map[string]any{}
			for _, kv := range *set {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("--set %q: want key=value", kv)
				}
				var val any = v
				if b, err := strconv.ParseBool(v); err == nil {
					val = b
				} else if f, err := strconv.ParseFloat(v, 64); err == nil && k != "clientId" && k != "clientSecret" {
					val = f
				}
				body[k] = val
			}
			if err := call("PUT", base+"/settings", body, nil); err != nil {
				return err
			}
		}
		if *signin {
			var si struct {
				UserCode        string
				VerificationURL string
			}
			if err := call("POST", base+"/signin", nil, &si); err != nil {
				return err
			}
			fmt.Printf("Open %s on any device and enter the code:\n\n    %s\n\nWaiting for approval...\n", si.VerificationURL, si.UserCode)
			for {
				time.Sleep(3 * time.Second)
				var st struct{ State, Error string }
				if err := call("GET", base+"/signin", nil, &st); err != nil {
					return err
				}
				if st.State == "done" {
					fmt.Println("Google Drive connected.")
					break
				}
				if st.State == "error" {
					return fmt.Errorf("sign-in failed: %s", st.Error)
				}
			}
		}
		var out struct{ Values map[string]any }
		if err := call("GET", base+"/settings", nil, &out); err != nil {
			return err
		}
		return show(out.Values, func() {
			keys := make([]string, 0, len(out.Values))
			for k := range out.Values {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Printf("%s = %v\n", k, out.Values[k])
			}
		})
	}
	return c
}

// maintainTools installs helper tools that enabled providers need and updates managed copies once a
// day (checksum-verified, self-tested, previous copy kept). Failures are logged and retried next day.
func maintainTools(ctx context.Context, inst *tools.Installer, reg *provider.Registry) {
	need := map[string][]string{"video": {"yt-dlp", "deno"}, "gdrive": {"rclone"}}
	t := time.NewTimer(time.Minute) // let the server settle first
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for prov, names := range need {
			for _, name := range names {
				_, found := tools.Find(name, inst.DataDir)
				managed := inst.Installed(name) != ""
				if managed || (!found && reg.Enabled(prov)) {
					if tag, err := inst.Install(ctx, name); err != nil {
						fmt.Fprintf(os.Stderr, "tools: %s: %v\n", name, err)
					} else if !managed {
						fmt.Fprintf(os.Stderr, "tools: installed %s %s\n", name, tag)
					}
				}
			}
		}
		t.Reset(24 * time.Hour)
	}
}

func toolsCmd() *cobra.Command {
	c := &cobra.Command{Use: "tools", Short: "Install, update or roll back managed helper tools (yt-dlp, rclone, deno)"}
	local := c.PersistentFlags().String("data", "", "act on this data folder directly, without a running server (image builds)")
	for _, a := range []string{"install", "rollback"} {
		c.AddCommand(&cobra.Command{Use: a + " <name>", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
			if *local != "" {
				in := tools.Installer{Client: http.DefaultClient, DataDir: *local}
				if a == "rollback" {
					return in.Rollback(args[0])
				}
				tag, err := in.Install(context.Background(), args[0])
				if err == nil {
					fmt.Println(args[0], tag)
				}
				return err
			}
			var out map[string]string
			if err := call("POST", "/v1/tools/"+url.PathEscape(args[0])+"/"+a, nil, &out); err != nil {
				return err
			}
			return show(out, func() { fmt.Printf("%s %s\n", args[0], out["version"]) })
		}})
	}
	return c
}

// checkUpdates looks for a newer signed release daily. With auto it applies it when no download is
// running, then restarts; otherwise the app and web UI show "update available".
func checkUpdates(ctx context.Context, ups *api.Updates, jm *jobs.Manager, auto bool) {
	t := time.NewTimer(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, err := ups.Check(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "update check:", err)
		} else if c.Available && auto && len(jm.Progress()) == 0 {
			if v, err := ups.Updater.Apply(ctx, ups.Version); err != nil {
				fmt.Fprintln(os.Stderr, "update:", err)
			} else if v != ups.Version {
				fmt.Fprintf(os.Stderr, "update: installed %s, restarting\n", v)
				ups.Restart()
				return
			}
		}
		t.Reset(24 * time.Hour)
	}
}

func updateCmd() *cobra.Command {
	c := &cobra.Command{Use: "update", Short: "Check for, apply or undo server updates (signed releases)"}
	c.AddCommand(&cobra.Command{Use: "check", RunE: func(*cobra.Command, []string) error {
		var ch update.Check
		if err := call("GET", "/v1/update?refresh=true", nil, &ch); err != nil {
			return err
		}
		return show(ch, func() {
			if ch.Available {
				fmt.Printf("update available: %s -> %s (dmn update apply)\n", ch.Current, ch.Latest)
			} else {
				fmt.Printf("up to date (%s)\n", ch.Current)
			}
		})
	}})
	c.AddCommand(&cobra.Command{Use: "apply", RunE: func(*cobra.Command, []string) error {
		var out map[string]string
		if err := call("POST", "/v1/update/apply", nil, &out); err != nil {
			return err
		}
		return show(out, func() { fmt.Printf("now %s; the server restarts\n", out["version"]) })
	}})
	c.AddCommand(&cobra.Command{Use: "rollback", Short: "Put back the previous binary (run on the server; restart it afterwards)", RunE: func(*cobra.Command, []string) error {
		_, cfg, err := config.Load(cfgPath, nil)
		if err != nil {
			return err
		}
		if err := (update.Updater{DataDir: cfg.Paths.Data}).Rollback(); err != nil {
			return err
		}
		fmt.Println("previous version restored; restart the service (systemctl restart dmn)")
		return nil
	}})
	return c
}

func uninstallCmd() *cobra.Command {
	c := &cobra.Command{Use: "uninstall", Short: "Remove the service and binary (keeps config, data and downloads unless told otherwise)"}
	purge := c.Flags().Bool("purge", false, "also remove config and data")
	delDL := c.Flags().Bool("delete-downloads", false, "also delete the download folder (asks you to type its path)")
	c.RunE = func(*cobra.Command, []string) error {
		if os.Geteuid() != 0 {
			return errors.New("run as root (sudo dmn uninstall)")
		}
		_, cfg, err := config.Load(cfgPath, nil)
		if err != nil {
			return err
		}
		if *delDL {
			fmt.Printf("This deletes %s and everything in it.\nType the path to confirm: ", cfg.Paths.Downloads)
			var typed string
			fmt.Scanln(&typed)
			if typed != cfg.Paths.Downloads {
				return errors.New("not confirmed; nothing removed")
			}
		}
		if _, err := os.Stat("/run/systemd/system"); err == nil {
			exec.Command("systemctl", "disable", "--now", "dmn").Run()
			os.Remove("/etc/systemd/system/dmn.service")
			exec.Command("systemctl", "daemon-reload").Run()
		}
		os.Remove("/usr/local/bin/dmn")
		os.RemoveAll("/opt/dmn")
		if *purge {
			os.RemoveAll(filepath.Dir(cfgPath))
			os.RemoveAll(cfg.Paths.Data)
		}
		if *delDL {
			os.RemoveAll(cfg.Paths.Downloads)
		}
		fmt.Println("removed the service and binary" + map[bool]string{true: ", config and data", false: "; config and data kept"}[*purge])
		return nil
	}
	return c
}

// healthCmd checks the local server (Docker HEALTHCHECK). It talks to 127.0.0.1 on the configured
// port and accepts the self-signed certificate: it only proves this process answers.
func healthCmd() *cobra.Command {
	return &cobra.Command{Use: "health", Hidden: true, RunE: func(*cobra.Command, []string) error {
		_, cfg, err := config.Load(cfgPath, nil)
		if err != nil {
			return err
		}
		_, port, err := net.SplitHostPort(cfg.Server.Listen)
		if err != nil {
			return err
		}
		scheme := "https"
		if tlsMode(cfg) == "off" {
			scheme = "http"
		}
		c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
		resp, err := c.Get(scheme + "://127.0.0.1:" + port + "/v1/health")
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("health: HTTP %d", resp.StatusCode)
		}
		return nil
	}}
}
