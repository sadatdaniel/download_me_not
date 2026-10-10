// Package tools finds the optional helper programs and reports what is missing (`doctor`).
package tools

import (
	"os"
	"os/exec"
	"path/filepath"
)

type Tool struct {
	Name     string `json:"name"`
	Found    bool   `json:"found"`
	Path     string `json:"path,omitempty"`
	Features string `json:"features"` // what switches off without it
	Managed  bool   `json:"managed"`  // the server downloads and updates it itself (M3)
}

var known = []Tool{
	{Name: "yt-dlp", Features: "video sites (YouTube and others)", Managed: true},
	{Name: "deno", Features: "YouTube formats (yt-dlp JS challenges)", Managed: true},
	{Name: "ffmpeg", Features: "merging separate video and audio streams"},
	{Name: "rclone", Features: "Google Drive and other drive destinations", Managed: true},
}

// Find looks in <data>/bin first (managed copies), then PATH.
func Find(name, dataDir string) (string, bool) {
	p := filepath.Join(dataDir, "bin", name)
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
		return p, true
	}
	p, err := exec.LookPath(name)
	return p, err == nil
}

func Doctor(dataDir string) []Tool {
	out := make([]Tool, len(known))
	for i, t := range known {
		t.Path, t.Found = Find(t.Name, dataDir)
		if !t.Found {
			t.Path = ""
		}
		out[i] = t
	}
	return out
}
