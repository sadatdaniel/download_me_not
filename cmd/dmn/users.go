package main

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/sadatdaniel/download_me_not/internal/api"
	"github.com/sadatdaniel/download_me_not/internal/config"
	"github.com/sadatdaniel/download_me_not/internal/store"
	"github.com/sadatdaniel/download_me_not/internal/tlsutil"
)

// withDB opens the server's database (also while the service runs: SQLite WAL).
func withDB(f func(db *sql.DB) error) error {
	_, cfg, err := config.Load(cfgPath, nil)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.Paths.Data, 0o700); err != nil {
		return err
	}
	dbPath := filepath.Join(cfg.Paths.Data, "dmn.db")
	db, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	err = f(db)
	db.Close()
	// `sudo dmn user ...` must not leave root-owned database files that lock the service out.
	if fi, e := os.Stat(cfg.Paths.Data); e == nil && os.Geteuid() == 0 {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
				os.Lchown(p, int(st.Uid), int(st.Gid))
			}
		}
	}
	return err
}

// userCmd manages household users on the server itself (like setup and pair): it works while the server is
// stopped, so the installer can create the first user, and an owner who forgot the PIN can reset it over SSH.
// From other machines, the app does this through the API.
func userCmd() *cobra.Command {
	c := &cobra.Command{Use: "user", Short: "Household users (name + 4-digit PIN); run on the server"}

	pinFlag := func(cmd *cobra.Command) *string {
		return cmd.Flags().String("pin", "", "4-digit PIN (default: env DMN_PIN, else asked)")
	}

	add := &cobra.Command{Use: "add <name>", Short: "Add a user (the first one becomes the owner)", Args: cobra.ExactArgs(1)}
	addPIN := pinFlag(add)
	add.RunE = func(_ *cobra.Command, a []string) error {
		pin := askPIN(*addPIN)
		return withDB(func(db *sql.DB) error {
			u, err := store.CreateUser(db, a[0], pin)
			if err != nil {
				return err
			}
			return show(u, func() {
				role := ""
				if u.Owner {
					role = " (owner)"
				}
				fmt.Printf("added %s%s; sign in with this name and PIN in the app\n", u.Name, role)
			})
		})
	}

	list := &cobra.Command{Use: "list", RunE: func(*cobra.Command, []string) error {
		return withDB(func(db *sql.DB) error {
			us, err := store.ListUsers(db)
			if err != nil {
				return err
			}
			return show(us, func() {
				rows := [][]string{{"ID", "NAME", "OWNER", "LOCKED"}}
				for _, u := range us {
					rows = append(rows, []string{u.ID, u.Name, fmt.Sprint(u.Owner), fmt.Sprint(u.Locked > 0)})
				}
				table(rows)
			})
		})
	}}

	pin := &cobra.Command{Use: "pin <name>", Short: "Set a new PIN (also unlocks the user)", Args: cobra.ExactArgs(1)}
	pinPIN := pinFlag(pin)
	pin.RunE = func(_ *cobra.Command, a []string) error {
		p := askPIN(*pinPIN)
		return withDB(func(db *sql.DB) error {
			u, err := store.UserByName(db, a[0])
			if err != nil {
				return err
			}
			if err := store.SetPIN(db, u.ID, p); err != nil {
				return err
			}
			fmt.Println("PIN changed for", u.Name)
			return nil
		})
	}

	rm := &cobra.Command{Use: "remove <name>", Short: "Remove a user without downloads and sign out their devices (files stay)", Args: cobra.ExactArgs(1)}
	rm.RunE = func(_ *cobra.Command, a []string) error {
		return withDB(func(db *sql.DB) error {
			u, err := store.UserByName(db, a[0])
			if err != nil {
				return err
			}
			if js, err := store.ListJobsOf(db, u.ID); err != nil {
				return err
			} else if len(js) > 0 {
				// The running server owns the downloads; it stops them when the owner removes the user in the app.
				return fmt.Errorf("%s still has %d downloads: remove the user in the app (Settings, Users), or their downloads first", u.Name, len(js))
			}
			if _, err := api.RemoveTokens(cfgPath, func(t config.Token) bool { return strings.HasPrefix(t.Name, "u:"+u.ID+":") }); err != nil {
				return err
			}
			if err := store.DeleteUser(db, u.ID); err != nil {
				return err
			}
			fmt.Println("removed", u.Name, "(their folder stays in users/"+u.ID+")")
			return nil
		})
	}
	c.AddCommand(add, list, pin, rm)
	return c
}

// loginCmd signs this CLI in as a user and prints the device token to use with --token / DMN_TOKEN.
func loginCmd() *cobra.Command {
	c := &cobra.Command{Use: "login <name>", Short: "Sign in as a user; prints this device's token", Args: cobra.ExactArgs(1)}
	p := c.Flags().String("pin", "", "4-digit PIN (default: env DMN_PIN, else asked)")
	dev := c.Flags().String("device", "cli", "device name; signing in again with it replaces its token")
	c.RunE = func(_ *cobra.Command, a []string) error {
		var out struct {
			Token string `json:"token"`
		}
		if err := call("POST", "/v1/login", map[string]string{"name": a[0], "pin": askPIN(*p), "device": *dev}, &out); err != nil {
			return err
		}
		return show(out, func() { fmt.Printf("Signed in. Use it with: export DMN_TOKEN=%s\n", out.Token) })
	}
	return c
}

func askPIN(flag string) string {
	if flag != "" {
		return flag
	}
	if p := os.Getenv("DMN_PIN"); p != "" {
		return p
	}
	fmt.Fprint(os.Stderr, "PIN (4 digits): ")
	var p string
	fmt.Scanln(&p)
	return p
}

// connectCmd explains how to reach this server: the address to type in the app, the certificate fingerprint to
// compare, the ports to open and how people sign in. The installer prints it; `sudo dmn connect` shows it again.
func connectCmd() *cobra.Command {
	return &cobra.Command{Use: "connect", Short: "How to connect the app and the web panel to this server (run on the server)",
		RunE: func(*cobra.Command, []string) error {
			_, cfg, err := config.Load(cfgPath, nil)
			if err != nil {
				return err
			}
			mode, fp := tlsMode(cfg), ""
			if mode == "self-signed" {
				cert, err := tlsutil.SelfSigned(filepath.Join(cfg.Paths.Data, "tls"))
				if err != nil {
					return err
				}
				fp, _ = tlsutil.Fingerprint(cert)
			}
			_, port, _ := net.SplitHostPort(cfg.Server.Listen)
			scheme := map[bool]string{true: "http", false: "https"}[mode == "off"]
			addr := scheme + "://<this server's public IP>:" + port
			if mode == "acme" && cfg.Server.Domain != "" {
				addr = "https://" + cfg.Server.Domain
			}
			var users []string
			withDB(func(db *sql.DB) error {
				us, err := store.ListUsers(db)
				for _, u := range us {
					users = append(users, u.Name+map[bool]string{true: " (owner)", false: ""}[u.Owner])
				}
				return err
			})
			out := map[string]any{"address": addr, "lan": scheme + "://" + lanAddr(cfg.Server.Listen), "fingerprint": fp, "users": users}
			return show(out, func() {
				fmt.Printf("\nHow to connect\n\n  Address:     %s\n", addr)
				if mode != "acme" {
					fmt.Printf("               (on the same network: %s)\n", out["lan"])
				}
				if fp != "" {
					fmt.Printf("  Certificate: %s\n               the app and the browser show it once; it must match\n", fp)
				}
				if mode == "off" && !loopback(cfg.Server.Listen) {
					fmt.Println("  Warning: TLS is off. Only use this behind a reverse proxy, tunnel or VPN.")
				}
				fmt.Printf(`
  1. Open the ports: TCP %s (app and web panel) and TCP+UDP 42069 (torrents, optional but faster),
     in this machine's firewall and, on a cloud VM, also in the provider's console
     (Oracle Cloud: VCN, Security List, Add Ingress Rules). Details: docs/deploy.md.
  2. In the app: Add server, type the address, accept the certificate, pick your name, enter your PIN.
     In a browser: open the address and sign in the same way.
  3. Everyone gets their own name and PIN and sees only their own downloads.
     Add people in the app (Settings, Users) or here: sudo dmn user add NAME
`, port)
				if len(users) == 0 {
					fmt.Println("\n  No users yet. Add the first one (the owner): sudo dmn user add NAME")
				} else {
					fmt.Printf("\n  Users: %s\n", strings.Join(users, ", "))
				}
			})
		}}
}
