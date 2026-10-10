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
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/sadatdaniel/download_me_not/internal/api"
	"github.com/sadatdaniel/download_me_not/internal/config"
	"github.com/sadatdaniel/download_me_not/internal/fsapi"
	"github.com/sadatdaniel/download_me_not/internal/provider"
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
		lsCmd(), statCmd(), mkdirCmd(), mvCmd(), rmCmd(), watchCmd())
	for name, ms := range map[string]string{
		"add": "M1", "pause": "M1", "resume": "M1", "mode": "M1", "copy": "M3", "formats": "M3",
		"peers": "M2", "pieces": "M2", "trackers": "M2", "limits": "M2", "recheck": "M2",
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
		// ponytail: the torrent engine is created by the job layer in M1/M2, so a bare `serve`
		// never joins the public DHT (AGENTS.md rule 7).
		h, _ := api.New(api.Deps{Version: version, Cfg: cfg, CfgPath: cfgPath, DB: db, FS: fsys,
			Providers: provider.NewRegistry(pcfg, cfg.Paths.Data)})
		srv := &http.Server{Addr: cfg.Server.Listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		go func() {
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			srv.Shutdown(sctx)
		}()
		fmt.Fprintf(os.Stderr, "dmn %s listening on %s, downloads in %s\n", version, cfg.Server.Listen, cfg.Paths.Downloads)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
	return c
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
	return &cobra.Command{Use: "doctor", Short: "Report helper tools and the features they affect", RunE: func(*cobra.Command, []string) error {
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
					rows := [][]string{{"ID", "KIND", "STATE", "NAME"}}
					for _, j := range jobs {
						rows = append(rows, []string{j.ID, j.Kind, j.State, j.Name})
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

func rmCmd() *cobra.Command {
	return &cobra.Command{Use: "rm <path>", Short: "Delete a file or folder on the server", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, a []string) error {
			return call("DELETE", "/v1/fs?path="+url.QueryEscape(a[0]), nil, nil)
		}}
}

func watchCmd() *cobra.Command {
	return &cobra.Command{Use: "watch <path>", Short: "Print a short-lived stream URL for a player", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, a []string) error {
			var out map[string]string
			if err := call("POST", "/v1/fs/sign", map[string]any{"path": a[0]}, &out); err != nil {
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
