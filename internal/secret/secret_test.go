package secret

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSealOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := b.Seal("gdrive.token", `{"refresh_token":"rt"}`)
	if !IsSealed(s) || strings.Contains(s, "rt") {
		t.Fatalf("not sealed: %s", s)
	}
	b2, _ := Open(path) // same key from the file
	if p, err := b2.Open("gdrive.token", s); err != nil || p != `{"refresh_token":"rt"}` {
		t.Fatalf("%q %v", p, err)
	}
	if _, err := b2.Open("gdrive.clientSecret", s); err == nil {
		t.Fatal("value opened under another name")
	}
	if p, _ := b2.Open("x", "plain"); p != "plain" {
		t.Fatal("plain value changed")
	}
}
