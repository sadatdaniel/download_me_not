package config

import (
	"path/filepath"
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

	k, c, err := Load(path, fs)
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
	k, _, _ = Load(path, nil)
	if Effective(k)["auth.signkey"] != "<redacted>" {
		t.Error("secret not redacted")
	}
}
