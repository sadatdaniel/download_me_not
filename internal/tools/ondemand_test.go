package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestOnDemand(t *testing.T) {
	t.Setenv("PATH", "") // only managed copies count here
	v1 := script("2026.01.01", true)
	rel := &fakeRelease{tag: "2026.01.01", files: map[string][]byte{
		"yt-dlp_linux": v1, "SHA2-256SUMS": []byte(sum(v1) + "  yt-dlp_linux\n")}}
	gh := fakeGitHub(t, map[string]*fakeRelease{"yt-dlp/yt-dlp": rel})
	no := false
	od := &OnDemand{Inst: &Installer{Client: gh.Client(), API: gh.URL, DataDir: t.TempDir(), Arch: "amd64", Musl: &no, check: exitsOK}}
	ctx := context.Background()

	// Asked without waiting: the install starts and the caller hears that it is running.
	if _, err := od.Ensure(ctx, "yt-dlp", 0); err == nil || !strings.Contains(err.Error(), "installing yt-dlp") {
		t.Fatalf("want installing, got %v", err)
	}
	// Asked again with a wait: the same install finishes and the path comes back.
	if p, err := od.Ensure(ctx, "yt-dlp", time.Minute); err != nil || !strings.HasSuffix(p, "/bin/yt-dlp") {
		t.Fatalf("ensure: %q %v", p, err)
	}
	if busy, failed := od.State("yt-dlp"); busy || failed != "" {
		t.Fatalf("state after install: %v %q", busy, failed)
	}

	// A broken release: the failure is reported, not an endless "installing".
	rel2 := &fakeRelease{tag: "x", files: map[string][]byte{"rclone-current-linux-amd64.zip": []byte("not a zip")}}
	gh2 := fakeGitHub(t, map[string]*fakeRelease{"rclone/rclone": rel2})
	od2 := &OnDemand{Inst: &Installer{Client: gh2.Client(), API: gh2.URL, DataDir: t.TempDir(), Arch: "amd64", Musl: &no, check: exitsOK}}
	if _, err := od2.Ensure(ctx, "rclone", time.Minute); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("want a failure, got %v", err)
	}
	if _, err := od2.Ensure(ctx, "rclone", 0); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("a failed install must not restart at once: %v", err)
	}
	if busy, _ := od2.State("rclone"); busy {
		t.Fatal("a failed install was retried immediately")
	}

	// No installer (tools come from the system): a plain "not installed".
	if _, err := (&OnDemand{}).Ensure(ctx, "yt-dlp", 0); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("want not installed, got %v", err)
	}
	if _, err := (*OnDemand)(nil).Ensure(ctx, "yt-dlp", 0); err == nil {
		t.Fatal("nil OnDemand found a tool with no PATH")
	}
}
