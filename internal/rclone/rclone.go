// Package rclone copies finished downloads to cloud drives with the rclone program (an external,
// optional tool). Each run gets a private temporary rclone.conf built from our settings, so secrets
// stay encrypted in our config and rclone's refreshed OAuth token is handed back to be stored.
package rclone

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type Runner struct{ Bin string }

// Remote is one rclone remote: Params are rclone config keys (type, client_id, token, ...).
type Remote struct {
	Name   string
	Params map[string]string
}

func writeConf(dir string, rem Remote) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]\n", rem.Name)
	keys := make([]string, 0, len(rem.Params))
	for k := range rem.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := rem.Params[k]
		if strings.ContainsAny(v, "\r\n") || strings.ContainsAny(k, "\r\n=[]") {
			return "", fmt.Errorf("rclone setting %q contains a line break", k)
		}
		fmt.Fprintf(&b, "%s = %s\n", k, v)
	}
	p := filepath.Join(dir, "rclone.conf")
	return p, os.WriteFile(p, []byte(b.String()), 0o600)
}

func readToken(conf string) string {
	b, err := os.ReadFile(conf)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && strings.TrimSpace(k) == "token" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Copy uploads src (a file or a folder) to rem:dst. With move, rclone deletes the local copy only
// after the upload is verified. It returns the remote's token when rclone refreshed it ("" if not).
func (r Runner) Copy(ctx context.Context, rem Remote, src, dst string, move bool, progress func(done, total int64)) (string, error) {
	fi, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "getaload-rclone-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	conf, err := writeConf(tmp, rem)
	if err != nil {
		return "", err
	}
	verb := map[[2]bool]string{{false, false}: "copy", {false, true}: "move", {true, false}: "copyto", {true, true}: "moveto"}[[2]bool{!fi.IsDir(), move}]
	args := []string{"--config", conf, verb, src, rem.Name + ":" + dst,
		"--use-json-log", "--stats", "1s", "--stats-log-level", "NOTICE", "--retries", "3", "--low-level-retries", "10"}
	cmd := exec.CommandContext(ctx, r.Bin, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	var lastErr string
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var l struct {
			Level string `json:"level"`
			Msg   string `json:"msg"`
			Stats *struct {
				Bytes      int64 `json:"bytes"`
				TotalBytes int64 `json:"totalBytes"`
			} `json:"stats"`
		}
		line := sc.Bytes()
		if json.Unmarshal(line, &l) != nil {
			continue
		}
		if l.Stats != nil && progress != nil {
			progress(l.Stats.Bytes, l.Stats.TotalBytes)
		}
		if l.Level == "error" {
			lastErr = l.Msg
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if lastErr == "" {
			lastErr = err.Error()
		}
		return "", errors.New("rclone: " + strings.TrimSpace(lastErr))
	}
	if t := readToken(conf); t != "" && t != rem.Params["token"] {
		return t, nil
	}
	return "", nil
}

// Version reports the installed rclone version (for doctor).
func (r Runner) Version(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, r.Bin, "version").Output()
	if err != nil {
		return "", err
	}
	first, _, _ := bytes.Cut(out, []byte("\n"))
	return string(first), nil
}
