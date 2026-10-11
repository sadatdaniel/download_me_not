package config

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/spf13/pflag"
)

func TestPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Set(path, "server.listen", "file:1"); err != nil {
		t.Fatal(err)
	}
	Set(path, "paths.data", "/file/data")
	Set(path, "paths.downloads", "/file/dl")
	t.Setenv("DMN_PATHS_DATA", "/env/data")
	t.Setenv("DMN_PATHS_DOWNLOADS", "/env/dl")

	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	fs.String("listen", "", "")
	fs.String("downloads", "", "")
	fs.Parse([]string{"--downloads", "/flag/dl"})

	_, c, err := Load(path, fs)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ got, want string }{
		{c.Server.Listen, "file:1"},     // file beats default; unset flag doesn't override
		{c.Paths.Data, "/env/data"},     // env beats file
		{c.Paths.Downloads, "/flag/dl"}, // flag beats env
	} {
		if tc.got != tc.want {
			t.Errorf("got %q want %q", tc.got, tc.want)
		}
	}
	Set(path, "auth.signkey", "s3cret")
	k, _, _ := Load(path, nil)
	if Effective(k)["auth.signkey"] != "<redacted>" {
		t.Error("secret not redacted")
	}
}

// As root (the dev and CI containers), writing must keep the file's owner: the service user has to
// keep reading the config after `sudo dmn pair` or `sudo dmn setup`.
func TestSetKeepsOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	Set(path, "server.listen", "a:1")
	os.Chown(path, 1234, 1234)
	if err := Set(path, "server.listen", "b:2"); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if st := fi.Sys().(*syscall.Stat_t); st.Uid != 1234 || st.Gid != 1234 {
		t.Fatalf("owner changed to %d:%d", st.Uid, st.Gid)
	}
}
