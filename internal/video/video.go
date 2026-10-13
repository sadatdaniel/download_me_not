// Package video runs yt-dlp for video pages (scope §5.2): probe formats, then download one.
// yt-dlp is an external, optional program; everything goes through its CLI, with the page URL
// after "--" so a URL can never be read as an option.
package video

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Format struct {
	ID         string  `json:"id"`
	Ext        string  `json:"ext"`
	Resolution string  `json:"resolution,omitempty"`
	Height     int     `json:"height,omitempty"`
	FPS        float64 `json:"fps,omitempty"`
	VCodec     string  `json:"vcodec,omitempty"`
	ACodec     string  `json:"acodec,omitempty"`
	Size       int64   `json:"size,omitempty"` // bytes, exact or estimated; 0 = unknown
	Note       string  `json:"note,omitempty"`
}

type Media struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Thumbnail string   `json:"thumbnail,omitempty"`
	Duration  float64  `json:"duration,omitempty"`
	Extractor string   `json:"extractor"`
	Formats   []Format `json:"formats"`
	Subtitles []string `json:"subtitles,omitempty" doc:"languages with real subtitles (not automatic captions)"`
}

// Preset is a named choice for the app and CLI; Format is a yt-dlp format selector.
type Preset struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Audio  string `json:"audio,omitempty"` // convert to this audio format (needs ffmpeg)
}

var Presets = []Preset{
	{Name: "best", Format: "bv*+ba/b"},
	{Name: "1080p", Format: "bv*[height<=1080]+ba/b[height<=1080]"},
	{Name: "720p", Format: "bv*[height<=720]+ba/b[height<=720]"},
	{Name: "480p", Format: "bv*[height<=480]+ba/b[height<=480]"},
	{Name: "m4a", Format: "ba[ext=m4a]/ba", Audio: "m4a"},
	{Name: "opus", Format: "ba[acodec=opus]/ba", Audio: "opus"},
	{Name: "mp3", Format: "ba", Audio: "mp3"},
}

// Runner holds how to call yt-dlp.
type Runner struct {
	Bin     string // path to yt-dlp
	Proxy   string // http://127.0.0.1:PORT of netguard's proxy; "" only in tests
	Cookies string // Netscape cookie file for this request; "" = none
	FFmpeg  string // directory or path of ffmpeg; "" = PATH
	Deno    string // path of deno for YouTube's JS challenges; "" = yt-dlp's default lookup
	UA      string // User-Agent that matches the imported cookies; "" = yt-dlp default
}

func (r Runner) base() []string {
	a := []string{"--no-playlist", "--no-warnings", "--no-colors", "--ignore-config"}
	if r.Proxy != "" {
		a = append(a, "--proxy", r.Proxy)
	}
	if r.Cookies != "" {
		a = append(a, "--cookies", r.Cookies)
	}
	if r.UA != "" {
		a = append(a, "--user-agent", r.UA)
	}
	if r.FFmpeg != "" {
		a = append(a, "--ffmpeg-location", r.FFmpeg)
	}
	if r.Deno != "" {
		a = append(a, "--js-runtimes", "deno:"+r.Deno)
	}
	return a
}

// Error carries yt-dlp's own message, which is usually the useful part ("Sign in to confirm ...").
type Error struct{ Msg string }

func (e Error) Error() string { return "yt-dlp: " + e.Msg }

func lastError(stderr []byte) error {
	lines := strings.Split(strings.TrimSpace(string(stderr)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); strings.HasPrefix(l, "ERROR:") {
			return Error{strings.TrimSpace(strings.TrimPrefix(l, "ERROR:"))}
		}
	}
	if len(lines) > 0 && lines[len(lines)-1] != "" {
		return Error{lines[len(lines)-1]}
	}
	return Error{"failed without a message"}
}

// Probe asks yt-dlp for title, thumbnail and formats without downloading.
func (r Runner) Probe(ctx context.Context, url string) (Media, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, r.Bin, append(r.base(), "-J", "--", url)...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Media{}, ctx.Err()
		}
		return Media{}, lastError(stderr.Bytes())
	}
	var raw struct {
		ID, Title, Thumbnail string
		Duration             float64
		Extractor            string `json:"extractor_key"`
		Subtitles            map[string]json.RawMessage
		Formats              []struct {
			FormatID       string  `json:"format_id"`
			Ext            string  `json:"ext"`
			Resolution     string  `json:"resolution"`
			Height         int     `json:"height"`
			FPS            float64 `json:"fps"`
			VCodec         string  `json:"vcodec"`
			ACodec         string  `json:"acodec"`
			Filesize       int64   `json:"filesize"`
			FilesizeApprox int64   `json:"filesize_approx"`
			FormatNote     string  `json:"format_note"`
		}
	}
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		return Media{}, fmt.Errorf("yt-dlp output: %w", err)
	}
	info := Media{ID: raw.ID, Title: raw.Title, Thumbnail: raw.Thumbnail, Duration: raw.Duration, Extractor: raw.Extractor}
	for lang := range raw.Subtitles {
		if lang != "live_chat" {
			info.Subtitles = append(info.Subtitles, lang)
		}
	}
	sort.Strings(info.Subtitles)
	for _, f := range raw.Formats {
		size := f.Filesize
		if size == 0 {
			size = f.FilesizeApprox
		}
		info.Formats = append(info.Formats, Format{ID: f.FormatID, Ext: f.Ext, Resolution: f.Resolution, Height: f.Height,
			FPS: f.FPS, VCodec: f.VCodec, ACodec: f.ACodec, Size: size, Note: f.FormatNote})
	}
	return info, nil
}

var formatRe = regexp.MustCompile(`^[A-Za-z0-9_.+/\[\]<=>*:!?-]{1,200}$`)

// Selector turns a preset name or a raw yt-dlp format id/selector into arguments.
func Selector(choice string) ([]string, error) {
	if choice == "" {
		choice = "best"
	}
	for _, p := range Presets {
		if p.Name == choice {
			a := []string{"-f", p.Format}
			if p.Audio != "" {
				a = append(a, "-x", "--audio-format", p.Audio)
			}
			return a, nil
		}
	}
	if !formatRe.MatchString(choice) || strings.HasPrefix(choice, "-") {
		return nil, fmt.Errorf("format %q: use a preset (best, 1080p, 720p, 480p, m4a, opus, mp3) or a format id from the probe", choice)
	}
	return []string{"-f", choice}, nil
}

// Options are the extra choices of a download: subtitle languages (saved as .srt next to the video, where the
// player finds them) and the container for video (mp4 or mkv; empty = what yt-dlp picks).
type Options struct {
	Subs      []string
	Container string
}

var langRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,19}$`)

// Args checks the options and turns them into yt-dlp arguments.
func (o Options) Args() ([]string, error) {
	var a []string
	switch o.Container {
	case "":
	case "mp4", "mkv":
		a = append(a, "--merge-output-format", o.Container) // ponytail: single-file formats keep their own container; remux if people ask
	default:
		return nil, fmt.Errorf("container must be mp4 or mkv, not %q", o.Container)
	}
	if len(o.Subs) > 0 {
		for _, l := range o.Subs {
			if !langRe.MatchString(l) {
				return nil, fmt.Errorf("bad subtitle language %q", l)
			}
		}
		a = append(a, "--write-subs", "--sub-langs", strings.Join(o.Subs, ","), "--convert-subs", "srt")
	}
	return a, nil
}

// Download fetches url into dir with the chosen format and returns the final file path.
// yt-dlp resumes its own .part files, so a cancelled download continues on the next call.
func (r Runner) Download(ctx context.Context, url, choice string, opt Options, dir string, progress func(done, total int64)) (string, error) {
	sel, err := Selector(choice)
	if err != nil {
		return "", err
	}
	extra, err := opt.Args()
	if err != nil {
		return "", err
	}
	args := append(append(r.base(), sel...), extra...)
	args = append(args,
		"--newline", "--progress", "--no-simulate", "--no-mtime",
		"--progress-template", "download:DMNPROG %(progress.downloaded_bytes)s %(progress.total_bytes)s %(progress.total_bytes_estimate)s",
		"--print", "after_move:DMNFILE %(filepath)s",
		"-P", dir, "-o", "%(title).150B [%(id)s].%(ext)s",
		"--", url)
	cmd := exec.CommandContext(ctx, r.Bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	var file string
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "DMNPROG "):
			f := strings.Fields(line)
			if len(f) == 4 && progress != nil {
				done, _ := strconv.ParseInt(f[1], 10, 64)
				total, err := strconv.ParseInt(f[2], 10, 64)
				if err != nil { // "NA": use the estimate
					est, _ := strconv.ParseFloat(f[3], 64)
					total = int64(est)
				}
				progress(done, total)
			}
		case strings.HasPrefix(line, "DMNFILE "):
			file = strings.TrimPrefix(line, "DMNFILE ")
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", lastError(stderr.Bytes())
	}
	if file == "" {
		return "", errors.New("yt-dlp finished but reported no file")
	}
	// The file must be inside dir (yt-dlp sanitizes titles; this is the belt to those braces).
	rel, err := filepath.Rel(dir, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("yt-dlp wrote outside the download folder: %s", file)
	}
	return file, nil
}
