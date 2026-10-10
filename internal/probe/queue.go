package probe

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/adapters"
	"foyer/internal/ffmpeg"
	"foyer/internal/media"
)

const (
	probeTimeout = 90 * time.Second
	grantFor     = 10 * time.Minute
	// perMinute bounds what the probes ask of the share — and of Real-Debrid,
	// behind Zurg.
	perMinute = 30
	// pause is how long the queue rests when the share keeps failing: its own
	// trouble, which no file is to blame for.
	pause = 5 * time.Minute
)

// backoff is the wait before a file is tried again after its first, second
// and third failure; a fourth fails it for good, until its size changes.
var backoff = []time.Duration{10 * time.Minute, time.Hour, 6 * time.Hour}

// Grants opens files to a tool, on the loopback.
type Grants interface {
	Grant(file string, ttl time.Duration) (string, func(), error)
	Loopback() string
}

// Library keeps what a probe found, and draws its titles' lists anew.
type Library interface {
	SaveProbe(file *core.Record, apply func(*core.Record)) error
	Flush()
}

// Queue is the files waiting for ffprobe.
type Queue struct {
	app        core.App
	adapters   *adapters.Registry
	grants     Grants
	library    Library
	collection string
	poke       chan struct{}

	mu          sync.Mutex
	now         func() time.Time
	pausedUntil time.Time
	failing     int
	recent      []time.Time
}

func New(app core.App, registry *adapters.Registry, grants Grants, library Library, collection string) *Queue {
	return &Queue{app: app, adapters: registry, grants: grants, library: library, collection: collection,
		poke: make(chan struct{}, 1), now: time.Now}
}

// Poke says there may be files to probe.
func (q *Queue) Poke() {
	select {
	case q.poke <- struct{}{}:
	default:
	}
}

// Run probes as files come, until ctx ends.
func (q *Queue) Run(ctx context.Context) {
	for {
		probed, _ := q.Once(ctx)
		wait := 30 * time.Second
		if probed > 0 {
			wait = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-q.poke:
		case <-time.After(wait):
		}
	}
}

// Once probes the next few files: the newest first, as many at once as the
// FFmpeg adapter says.
func (q *Queue) Once(ctx context.Context) (int, error) {
	s, err := q.adapters.Settings(ffmpeg.Key)
	if err != nil || !s.Enabled {
		return 0, err
	}
	tools := ffmpeg.FromSettings(s)
	at := max(1, min(s.Int("concurrency"), 8))
	now := q.now()
	q.mu.Lock()
	paused := now.Before(q.pausedUntil)
	q.mu.Unlock()
	if paused {
		return 0, nil
	}
	files, err := q.app.FindRecordsByFilter(q.collection,
		"probe_state = 'pending' && missing_since = '' && (probe_next_at = '' || probe_next_at <= {:now})",
		"-added", at*4, 0, dbx.Params{"now": now.UTC()})
	if err != nil || len(files) == 0 {
		return 0, err
	}
	gate := make(chan struct{}, at)
	var wg sync.WaitGroup
	probed := 0
	var mu sync.Mutex
	for _, f := range files {
		if !q.allowed() {
			break
		}
		select {
		case gate <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return probed, ctx.Err()
		}
		wg.Go(func() {
			defer func() { <-gate }()
			if q.probe(ctx, tools, f) {
				mu.Lock()
				probed++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	q.library.Flush()
	return probed, nil
}

// allowed counts a probe against the minute's share, and says whether it may
// go.
func (q *Queue) allowed() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	recent := q.recent[:0]
	for _, t := range q.recent {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	q.recent = recent
	if len(q.recent) >= perMinute {
		return false
	}
	q.recent = append(q.recent, now)
	return true
}

// probe reads one file, and keeps what it found — or why it could not.
func (q *Queue) probe(ctx context.Context, tools ffmpeg.Tools, file *core.Record) bool {
	address, revoke, err := q.grants.Grant(file.Id, grantFor)
	if err != nil {
		return false
	}
	defer revoke()
	raw, err := Run(ctx, tools.FFprobe, address, q.grants.Loopback())
	var v media.Version
	if err == nil {
		if v, err = Map(raw, file.GetString("name")); err == nil {
			if v.Video != nil && v.Video.HDR == "hdr10" && (v.Video.Codec == "hevc" || v.Video.Codec == "av1") && dynamic(ctx, tools.FFprobe, address) {
				v.Video.HDR = "hdr10+"
			}
		}
	}
	if ctx.Err() != nil {
		return false
	}
	now := q.now().UTC()
	if err == nil {
		q.mu.Lock()
		q.failing = 0
		q.mu.Unlock()
		v.Size = file.GetInt64("size")
		return q.library.SaveProbe(file, func(r *core.Record) {
			r.Set("probe", v)
			r.Set("probe_state", "done")
			r.Set("probed_at", now)
			r.Set("probe_error", "")
			r.Set("probe_attempts", 0)
			r.Set("probe_next_at", "")
		}) == nil
	}
	said := err.Error()
	switch kind(said) {
	case "gone":
		_ = q.library.SaveProbe(file, func(r *core.Record) {
			r.Set("probe_error", said)
			r.Set("probe_next_at", now.Add(time.Hour))
		})
	case "source":
		q.mu.Lock()
		q.failing++
		if q.failing >= 5 {
			q.pausedUntil, q.failing = now.Add(pause), 0
		}
		q.mu.Unlock()
		_ = q.library.SaveProbe(file, func(r *core.Record) {
			r.Set("probe_error", said)
			r.Set("probe_next_at", now.Add(backoff[0]))
		})
	default:
		_ = q.library.SaveProbe(file, func(r *core.Record) {
			attempts := r.GetInt("probe_attempts") + 1
			r.Set("probe_attempts", attempts)
			r.Set("probe_error", said)
			if attempts > len(backoff) {
				r.Set("probe_state", "failed")
				r.Set("probe_next_at", "")
			} else {
				r.Set("probe_next_at", now.Add(backoff[attempts-1]))
			}
		})
	}
	return false
}

// kind says whose a failure is: the file's being gone, its source failing —
// no attempt of the file's — or the file's own.
func kind(said string) string {
	lower := strings.ToLower(said)
	switch {
	case strings.Contains(lower, "404"), strings.Contains(lower, "not found"):
		return "gone"
	case strings.Contains(lower, "server returned 5"), strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "timed out"), strings.Contains(lower, "connection reset"), strings.Contains(lower, "502"),
		strings.Contains(lower, "503"), strings.Contains(lower, "504"):
		return "source"
	}
	return "file"
}

var anyURL = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://\S+`)

// Run asks ffprobe what is at address — a loopback grant — and answers its
// JSON. What it says on failure is cleared of addresses before anyone sees
// it, and kept short.
func Run(ctx context.Context, ffprobe, address, loopback string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var out, errOut bytes.Buffer
	cmd := ffmpeg.Command(ctx, ffprobe,
		"-hide_banner", "-v", "error",
		"-print_format", "json", "-show_format", "-show_streams", "-show_chapters",
		"-probesize", "5000000", "-analyzeduration", "5000000",
		"-rw_timeout", "20000000",
		// A file that is really a playlist must not send ffprobe elsewhere.
		"-protocol_whitelist", "http,tcp",
		address)
	cmd.Stdout, cmd.Stderr = &capped{&out, 4 << 20}, &capped{&errOut, 16 << 10}
	if err := cmd.Run(); err != nil {
		said := strings.TrimSpace(errOut.String())
		if said == "" {
			said = err.Error()
		}
		return nil, errors.New(redact(said, address, loopback))
	}
	return out.Bytes(), nil
}

// dynamic says whether the first frame carries HDR10+'s metadata, which only
// a frame does.
func dynamic(ctx context.Context, ffprobe, address string) bool {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := ffmpeg.Command(ctx, ffprobe, "-hide_banner", "-v", "error", "-select_streams", "v:0",
		"-read_intervals", "%+#1", "-show_frames", "-show_entries", "frame=side_data_list",
		"-print_format", "json", "-protocol_whitelist", "http,tcp", address)
	cmd.Stdout = &capped{&out, 1 << 20}
	if cmd.Run() != nil {
		return false
	}
	return bytes.Contains(out.Bytes(), []byte("SMPTE2094-40"))
}

func redact(said, address, loopback string) string {
	said = strings.ReplaceAll(said, address, "<input>")
	if loopback != "" {
		said = strings.ReplaceAll(said, loopback, "<loopback>")
	}
	said = anyURL.ReplaceAllString(said, "<url>")
	if len(said) > 500 {
		said = said[:500]
	}
	return said
}

// capped keeps the first max bytes written, and drops the rest.
type capped struct {
	buf *bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
