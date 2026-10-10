// Package httpdl is the multi-connection, resumable HTTP downloader (scope §5.1, IDM-style).
//
// Dynamic segmentation (the IDM/aria2 technique): the file starts as N equal segments. When a
// connection finishes its segment it splits the largest remaining one in half and takes the second
// half, so no connection sits idle at the end. The segment map is the resume state; the job layer
// saves it, and If-Range makes the server refuse ranges from a file that changed meanwhile.
package httpdl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Segment covers [Start, End) of the file; Done bytes from Start are written. End < 0 = until EOF.
type Segment struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	Done  int64 `json:"done"`
}

// State is everything needed to resume. It is plain JSON for the jobs table.
type State struct {
	URL      string    `json:"url"`
	Size     int64     `json:"size"` // -1 = unknown
	Ranges   bool      `json:"ranges"`
	ETag     string    `json:"etag,omitempty"`
	LastMod  string    `json:"lastMod,omitempty"`
	Name     string    `json:"name"`
	Segments []Segment `json:"segments,omitempty"`
}

type Options struct {
	Client      *http.Client // the job layer passes netguard.Client(); tests use a loopback client
	Header      http.Header  // cookies, referer, user agent from the browser or app
	Connections int          // 1 = sequential (stream mode)
	MinSplit    int64        // smallest piece worth stealing; default 1 MiB
	Retries     int          // consecutive failures without progress per connection; default 8
	OnSave      func(State)  // called every SaveEvery and when Run returns
	SaveEvery   time.Duration
}

var (
	ErrChanged = errors.New("the file on the server changed; restart the download")
	ErrNoRange = errors.New("server stopped honouring ranges")
)

// HTTPError is a non-retryable status (403, 404, 410 ...). 403/410 often mean an expired signed link.
type HTTPError struct{ Status int }

func (e HTTPError) Error() string {
	return "server answered " + strconv.Itoa(e.Status) + " " + http.StatusText(e.Status)
}

func newReq(ctx context.Context, url string, h http.Header) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range h {
		req.Header[k] = append([]string(nil), vs...)
	}
	return req, nil
}

// Probe asks for the first byte to learn size, range support, validators and a file name.
func Probe(ctx context.Context, c *http.Client, url string, h http.Header) (State, error) {
	req, err := newReq(ctx, url, h)
	if err != nil {
		return State{}, err
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := c.Do(req)
	if err != nil {
		return State{}, err
	}
	resp.Body.Close()
	st := State{URL: url, Size: -1, ETag: resp.Header.Get("ETag"), LastMod: resp.Header.Get("Last-Modified")}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		// Content-Range: bytes 0-0/12345
		if i := strings.LastIndexByte(resp.Header.Get("Content-Range"), '/'); i >= 0 {
			if n, err := strconv.ParseInt(resp.Header.Get("Content-Range")[i+1:], 10, 64); err == nil {
				st.Size, st.Ranges = n, true
			}
		}
	case http.StatusOK:
		st.Size = resp.ContentLength
	default:
		return State{}, HTTPError{resp.StatusCode}
	}
	st.Name = fileName(resp)
	return st, nil
}

func fileName(resp *http.Response) string {
	name := ""
	if _, p, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
		name = p["filename"]
	}
	if name == "" {
		name = path.Base(resp.Request.URL.Path) // final URL after redirects
	}
	return SanitizeName(name)
}

// SanitizeName makes a server-supplied name safe as a single path element.
func SanitizeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 0x20 {
			return '_'
		}
		return r
	}, strings.TrimSpace(s))
	if len(s) > 200 {
		s = s[:200]
	}
	if s == "" || s == "." || s == ".." {
		return "download"
	}
	return s
}

type seg struct {
	Segment
	active bool
}

type Download struct {
	st   State
	file string
	o    Options

	mu   sync.Mutex
	segs []*seg
}

func New(st State, partFile string, o Options) *Download {
	if o.Connections < 1 {
		o.Connections = 1
	}
	if o.MinSplit <= 0 {
		o.MinSplit = 1 << 20
	}
	if o.Retries <= 0 {
		o.Retries = 8
	}
	if o.SaveEvery <= 0 {
		o.SaveEvery = 2 * time.Second
	}
	d := &Download{st: st, file: partFile, o: o}
	if !st.Ranges || st.Size < 0 {
		// No ranges: one sequential connection from zero; resuming means starting over.
		d.segs = []*seg{{Segment: Segment{0, st.Size, 0}}}
		return d
	}
	if len(st.Segments) > 0 {
		for _, s := range st.Segments {
			d.segs = append(d.segs, &seg{Segment: s})
		}
		return d
	}
	n := int64(o.Connections)
	if m := st.Size / o.MinSplit; m < n {
		n = max(m, 1)
	}
	step := st.Size / n
	for i := int64(0); i < n; i++ {
		end := (i + 1) * step
		if i == n-1 {
			end = st.Size
		}
		d.segs = append(d.segs, &seg{Segment: Segment{i * step, end, 0}})
	}
	return d
}

// State returns a resumable snapshot.
func (d *Download) State() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.st
	st.Segments = make([]Segment, len(d.segs))
	for i, s := range d.segs {
		st.Segments[i] = s.Segment
	}
	return st
}

func (d *Download) Completed() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int64
	for _, s := range d.segs {
		n += s.Done
	}
	return n
}

// Available returns how many bytes starting at off are already written (for streaming a growing file).
func (d *Download) Available(off int64) int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.segs {
		if off >= s.Start && off < s.Start+s.Done {
			return s.Start + s.Done - off
		}
	}
	return 0
}

// Run downloads until done, a fatal error, or ctx is cancelled (then the state is resumable).
func (d *Download) Run(ctx context.Context) error {
	flag := os.O_RDWR | os.O_CREATE
	if !d.st.Ranges {
		flag |= os.O_TRUNC
		d.segs[0].Done = 0
	}
	f, err := os.OpenFile(d.file, flag, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if d.st.Size > 0 {
		if err := f.Truncate(d.st.Size); err != nil { // sparse preallocation
			return err
		}
	}
	defer func() {
		if d.o.OnSave != nil {
			d.o.OnSave(d.State())
		}
	}()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if d.o.OnSave != nil {
		go func() {
			t := time.NewTicker(d.o.SaveEvery)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					d.o.OnSave(d.State())
				}
			}
		}()
	}

	conns := d.o.Connections
	if !d.st.Ranges {
		conns = 1
	}
	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	for range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := d.next(); s != nil; s = d.next() {
				if err := d.fetch(ctx, f, s); err != nil {
					errOnce.Do(func() { firstErr = err; cancel() })
					return
				}
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.Sync()
}

// next claims an idle unfinished segment, or splits the largest active one (work stealing).
func (d *Download) next() *seg {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.segs {
		if !s.active && (s.End < 0 || s.Done < s.End-s.Start) {
			s.active = true
			return s
		}
	}
	if !d.st.Ranges {
		return nil
	}
	var big *seg
	var bigRem int64
	for _, s := range d.segs {
		if rem := s.End - s.Start - s.Done; s.active && rem > bigRem {
			big, bigRem = s, rem
		}
	}
	if big == nil || bigRem < 2*d.o.MinSplit {
		return nil
	}
	mid := big.Start + big.Done + bigRem/2
	n := &seg{Segment: Segment{mid, big.End, 0}, active: true}
	big.End = mid
	d.segs = append(d.segs, n)
	return n
}

func (d *Download) fetch(ctx context.Context, f *os.File, s *seg) error {
	defer func() { d.mu.Lock(); s.active = false; d.mu.Unlock() }()
	fails := 0
	for {
		d.mu.Lock()
		pos, end := s.Start+s.Done, s.End
		d.mu.Unlock()
		if end >= 0 && pos >= end {
			return nil
		}
		progressed, err := d.stream(ctx, f, s, pos, end)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var he HTTPError
		if errors.As(err, &he) || errors.Is(err, ErrChanged) || errors.Is(err, ErrNoRange) {
			return err
		}
		if progressed {
			fails = 0
		}
		if fails++; fails > d.o.Retries {
			return fmt.Errorf("giving up after %d retries: %w", d.o.Retries, err)
		}
		// Exponential backoff with jitter, capped at 30 s.
		wait := min(time.Second<<min(fails-1, 5), 30*time.Second)
		wait += time.Duration(rand.Int64N(int64(wait / 4)))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func retryable(code int) bool {
	return code == http.StatusTooManyRequests || code == http.StatusRequestTimeout || code >= 500
}

// stream copies one HTTP response into the segment until it ends or the segment was shortened by a split.
func (d *Download) stream(ctx context.Context, f *os.File, s *seg, pos, end int64) (progressed bool, err error) {
	req, err := newReq(ctx, d.st.URL, d.o.Header)
	if err != nil {
		return false, err
	}
	if d.st.Ranges {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", pos, end-1))
		if v := d.st.ETag; v != "" && !strings.HasPrefix(v, "W/") {
			req.Header.Set("If-Range", v)
		} else if d.st.LastMod != "" {
			req.Header.Set("If-Range", d.st.LastMod)
		}
	}
	resp, err := d.o.Client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch {
	case d.st.Ranges && resp.StatusCode == http.StatusOK:
		if req.Header.Get("If-Range") != "" {
			return false, ErrChanged
		}
		return false, ErrNoRange
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent:
	case retryable(resp.StatusCode):
		return false, fmt.Errorf("server answered %d", resp.StatusCode)
	default:
		return false, HTTPError{resp.StatusCode}
	}

	buf := make([]byte, 64<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			d.mu.Lock()
			if s.End >= 0 && int64(n) > s.End-pos {
				n = int(s.End - pos) // the tail was handed to another connection
			}
			d.mu.Unlock()
			if n > 0 {
				if _, err := f.WriteAt(buf[:n], pos); err != nil {
					return progressed, err
				}
				pos += int64(n)
				progressed = true
				d.mu.Lock()
				// Clamp: a split may have moved End below what this write covered (same bytes, harmless).
				s.Done = pos - s.Start
				if s.End >= 0 && s.Done > s.End-s.Start {
					s.Done = s.End - s.Start
				}
				done := s.End >= 0 && s.Done >= s.End-s.Start
				d.mu.Unlock()
				if done {
					return progressed, nil
				}
			}
		}
		if rerr == io.EOF {
			d.mu.Lock()
			if s.End < 0 { // unknown size: EOF is the end
				s.End = pos
				d.st.Size = pos
			}
			short := s.Done < s.End-s.Start
			d.mu.Unlock()
			if short {
				return progressed, io.ErrUnexpectedEOF
			}
			return progressed, nil
		}
		if rerr != nil {
			return progressed, rerr
		}
	}
}
