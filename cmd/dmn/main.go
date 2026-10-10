// Command dmn is the server and its CLI in one static binary. ("dmn" is a placeholder name.)
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/sadatdaniel/download_me_not/internal/api"
	"github.com/sadatdaniel/download_me_not/internal/config"
	"github.com/sadatdaniel/download_me_not/internal/engine"
	"github.com/sadatdaniel/download_me_not/internal/fsapi"
	"github.com/sadatdaniel/download_me_not/internal/jobs"
	"github.com/sadatdaniel/download_me_not/internal/limits"
	"github.com/sadatdaniel/download_me_not/internal/netguard"
	"github.com/sadatdaniel/download_me_not/internal/provider"
	"github.com/sadatdaniel/download_me_not/internal/selftest"
	"github.com/sadatdaniel/download_me_not/internal/store"
	"github.com/sadatdaniel/download_me_not/internal/tools"
)

var version = "dev" // set by -ldflags "-X main.version=..."

var (
	cfgPath  string
	jsonOut  bool
	srvURL   string
	srvToken string
)

func main() {
	root := &cobra.Command{Use: "dmn", Short: "Self-hosted download server and CLI", SilenceUsage: true}
	pf := root.PersistentFlags()
	pf.StringVar(&cfgPath, "config", config.DefaultPath(), "config file (env DMN_CONFIG)")
	pf.BoolVar(&jsonOut, "json", false, "JSON output for scripting")
	pf.StringVar(&srvURL, "url", envOr("DMN_URL", "http://127.0.0.1:8080"), "server URL (env DMN_URL)")
	pf.StringVar(&srvToken, "token", os.Getenv("DMN_TOKEN"), "API token (env DMN_TOKEN)")

	root.AddCommand(serveCmd(), setupCmd(), configCmd(), doctorCmd(), versionCmd(), providersCmd(),
		lsCmd(), statCmd(), mkdirCmd(), mvCmd(), cpCmd(), zipCmd(), rmCmd(), watchCmd(), addCmd(), modeCmd(), cancelCmd(),
		jobAction("pause"), jobAction("resume"), jobAction("recheck"), limitsCmd(), peersCmd(), piecesCmd(), trackersCmd(), priorityCmd(), filesCmd(), learnedCmd())
	for name, ms := range map[string]string{
		"copy": "M3", "formats": "M3",
		"cookies": "M3", "update": "M3", "pair": "M3", "uninstall": "M3",
	} {
		root.AddCommand(&cobra.Command{Use: name, Short: "(not implemented yet: " + ms + ")",
			DisableFlagParsing: true,
			RunE: func(*cobra.Command, []string) error {
				return fmt.Errorf("%s is not implemented yet (milestone %s)", name, ms)
			}})
	}
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
		lim := limits.New(cfg.Limits.Down, cfg.Limits.Up)
		jm := jobs.New(jobs.Options{DB: db, Downloads: cfg.Paths.Downloads, Client: netguard.Client(), Allowed: reg.Enabled,
			Connections: setting(cfg, "web", "connections", 16), Limiter: lim.Down,
			MaxActive: cfg.Jobs.MaxActive, DiskReserve: cfg.Jobs.Reserve,
			Post:          jobs.Post{Extract: cfg.Jobs.Extract, DeleteArchive: cfg.Jobs.DeleteArchive},
			LearnTrackers: settingBool(cfg, "torrent", "learnTrackers", true),
			Seed: jobs.SeedPolicy{Policy: settingStr(cfg, "torrent", "seed", "stop"),
				Ratio: settingFloat(cfg, "torrent", "seedRatio", 1), Minutes: setting(cfg, "torrent", "seedMinutes", 60)},
			NewEngine: func() (engine.Engine, error) { return newEngine(cfg, lim) }})
		jobsDone := make(chan error, 1)
		go func() { jobsDone <- jm.Run(ctx) }()
		h, _ := api.New(api.Deps{Version: version, Cfg: cfg, CfgPath: cfgPath, DB: db, FS: fsys, Providers: reg, Jobs: jm, Limits: lim})
		srv := &http.Server{Addr: cfg.Server.Listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			srv.Shutdown(sctx)
		}()
		defer func() { stop(); <-jobsDone }() // runners save their resume state before the DB closes
		fmt.Fprintf(os.Stderr, "dmn %s listening on %s, downloads in %s\n", version, cfg.Server.Listen, cfg.Paths.Downloads)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
	return c
}

// newEngine builds the torrent engine from the torrent provider settings (scope §5.9).
func newEngine(cfg *config.Config, lim *limits.Limits) (engine.Engine, error) {
	bind := settingStr(cfg, "torrent", "bind", "")
	if ifc, err := net.InterfaceByName(bind); err == nil { // an interface name such as wg0
		addrs, _ := ifc.Addrs()
		if len(addrs) == 0 {
			return nil, fmt.Errorf("interface %s has no address (VPN down?)", bind)
		}
		bind = addrs[0].(*net.IPNet).IP.String()
	}
	bl := &engine.Blocklist{}
	if p := settingStr(cfg, "torrent", "blocklist", ""); p != "" {
		f, err := os.Open(p)
		if err != nil {
			return nil, fmt.Errorf("blocklist: %w", err)
		}
		_, err = bl.Load(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("blocklist: %w", err)
		}
	}
	return engine.New(engine.Options{
		DataDir: cfg.Paths.Downloads, ListenHost: bind, ListenPort: setting(cfg, "torrent", "port", 42069),
		NoDHT: !settingBool(cfg, "torrent", "dht", true), NoPEX: !settingBool(cfg, "torrent", "pex", true),
		NoUTP: !settingBool(cfg, "torrent", "utp", true), NoIPv6: !settingBool(cfg, "torrent", "ipv6", true),
		Encryption: settingStr(cfg, "torrent", "encryption", "prefer"), MaxPeers: setting(cfg, "torrent", "maxPeers", 0),
		Anonymous:   settingBool(cfg, "torrent", "anonymous", false),
		DownLimiter: lim.Down, UpLimiter: lim.Up, Blocklist: bl, Control: netguard.Safe,
		DHTNodesFile: filepath.Join(cfg.Paths.Data, "dht-nodes.dat"),
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
		tok := randomToken("dmn_")
		toks := []map[string]any{}
		for _, t := range cfg.Auth.Tokens {
			if t.Name != *name { // re-running replaces the token with the same name
				toks = append(toks, map[string]any{"name": t.Name, "hash": t.Hash, "scope": t.Scope})
			}
		}
		toks = append(toks, map[string]any{"name": *name, "hash": api.HashToken(tok), "scope": *scope})
		if err := config.Set(cfgPath, "auth.tokens", toks); err != nil {
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
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
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
		resp, err := http.DefaultClient.Do(req)
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
	unpack := c.Flags().Bool("extract", false, "unpack archives when done (default from jobs.extract)")
	delArc := c.Flags().Bool("delete-archive", false, "with --extract: delete the archive after unpacking")
	c.RunE = func(_ *cobra.Command, a []string) error {
		req := jobs.AddRequest{Source: a[0], Mode: *mode, Header: map[string]string{}, Priority: *prio}
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
	cfgC := &cobra.Command{Use: "config <name>", Short: "(not implemented yet: M3)", RunE: func(*cobra.Command, []string) error {
		return fmt.Errorf("providers config is not implemented yet (milestone M3)")
	}}
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
