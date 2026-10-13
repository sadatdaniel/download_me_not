package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/sadatdaniel/download_me_not/internal/api"
	"github.com/sadatdaniel/download_me_not/internal/config"
	"github.com/sadatdaniel/download_me_not/internal/store"
)

// userCmd manages household users on the server itself (like setup and pair): it works while the server is
// stopped, so the installer can create the first user, and an owner who forgot the PIN can reset it over SSH.
// From other machines, the app does this through the API.
func userCmd() *cobra.Command {
	c := &cobra.Command{Use: "user", Short: "Household users (name + 4-digit PIN); run on the server"}
	withDB := func(f func(db *sql.DB) error) error {
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
