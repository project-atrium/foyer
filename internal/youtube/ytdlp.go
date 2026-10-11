package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"foyer/internal/ffmpeg"
)

// Why the site will not play a video, as the contract names them.
const (
	Unavailable = "unavailable"
	Age         = "age"
	Members     = "members"
	Upcoming    = "upcoming"
	Blocked     = "blocked"
	Failed      = "failed"
)

// Refusal is the site declining a video, and why.
type Refusal struct {
	Reason  string
	Message string
}

func (r *Refusal) Error() string { return r.Message }

// ErrOff is YouTube switched off on this Foyer.
var ErrOff = errors.New("YouTube is switched off on this Foyer")

// kept is how long what yt-dlp said of a video is used again: its links last
// hours, and a run costs seconds.
const kept = 20 * time.Minute

// Runner runs yt-dlp: one run for a video at a time, its answer kept a while.
type Runner struct {
	tool  func() (string, bool)
	dir   string
	now   func() time.Time
	mu    sync.Mutex
	cache map[string]cached
	group singleflight.Group
}

type cached struct {
	info  *Info
	until time.Time
}

// NewRunner runs the yt-dlp tool names — false while YouTube is off — and
// keeps its own cache, and Deno's, under dir.
func NewRunner(tool func() (string, bool), dir string) *Runner {
	return &Runner{tool: tool, dir: dir, now: time.Now, cache: map[string]cached{}}
}

// Video is what yt-dlp says of a video, asked now or a little while ago.
func (r *Runner) Video(ctx context.Context, id string) (*Info, error) {
	if !ValidID.MatchString(id) {
		return nil, &Refusal{Reason: Unavailable, Message: "That is no video of the site's."}
	}
	now := r.now()
	r.mu.Lock()
	if c, ok := r.cache[id]; ok && now.Before(c.until) {
		r.mu.Unlock()
		return c.info, nil
	}
	r.mu.Unlock()
	v, err, _ := r.group.Do(id, func() (any, error) {
		info, err := r.run(ctx, id)
		if err != nil {
			return nil, err
		}
		// Kept a while, never past an hour before its links stop working.
		until := r.now().Add(kept)
		if last := info.Expires(r.now()).Add(-time.Hour); last.Before(until) {
			until = last
		}
		r.mu.Lock()
		for key, c := range r.cache {
			if !r.now().Before(c.until) {
				delete(r.cache, key)
			}
		}
		r.cache[id] = cached{info: info, until: until}
		r.mu.Unlock()
		return info, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*Info), nil
}

// peek is what yt-dlp said of a video a little while ago, where it is at
// hand: nothing is run for it.
func (r *Runner) peek(id string) (*Info, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cache[id]
	if !ok || !r.now().Before(c.until) {
		return nil, false
	}
	return c.info, true
}

func (r *Runner) run(ctx context.Context, id string) (*Info, error) {
	tool, ok := r.tool()
	if !ok {
		return nil, ErrOff
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return nil, err
	}
	// IPv4 alone, as the relay fetches: the site binds its links to the address that asked.
	cmd := ffmpeg.Command(ctx, tool, "--ignore-config", "--no-warnings", "--no-progress", "--skip-download", "--no-playlist", "--force-ipv4",
		"--dump-single-json", "--cache-dir", filepath.Join(r.dir, "yt-dlp"), "--", "https://www.youtube.com/watch?v="+id)
	// Deno, which solves the site's challenges, keeps what it fetched beside it.
	cmd.Env = append(cmd.Env, "HOME="+r.dir, "DENO_DIR="+filepath.Join(r.dir, "deno"), "XDG_CACHE_HOME="+r.dir)
	var out bytes.Buffer
	said := &tail{max: 8 << 10}
	cmd.Stdout, cmd.Stderr = &out, said
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("yt-dlp took too long: %w", ctx.Err())
		}
		return nil, refusalOf(said.String())
	}
	var info Info
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		return nil, fmt.Errorf("yt-dlp's answer could not be read: %w", err)
	}
	if info.ID != id {
		return nil, errors.New("yt-dlp answered for another video")
	}
	return &info, nil
}

var links = regexp.MustCompile(`https?://\S+`)

// refusalOf reads why yt-dlp could not say what a video is: its last error,
// every address in it cleared.
func refusalOf(said string) *Refusal {
	line := ""
	for _, l := range strings.Split(said, "\n") {
		if strings.HasPrefix(l, "ERROR:") {
			line = l
		}
	}
	if line == "" {
		line = strings.TrimSpace(said)
	}
	message := strings.TrimSpace(links.ReplaceAllString(strings.TrimPrefix(line, "ERROR:"), "<address>"))
	if i := strings.Index(message, ": "); strings.HasPrefix(message, "[youtube]") && i >= 0 {
		// "[youtube] <id>: Video unavailable" reads as what follows the id.
		message = strings.TrimSpace(message[i+2:])
	}
	if len(message) > 300 {
		message = message[:300]
	}
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "not a bot"), strings.Contains(lower, "429"), strings.Contains(lower, "too many requests"):
		return &Refusal{Reason: Blocked, Message: "YouTube refused this server for now. Try again later."}
	case strings.Contains(lower, "confirm your age"), strings.Contains(lower, "age-restricted"), strings.Contains(lower, "inappropriate for some users"):
		return &Refusal{Reason: Age, Message: "This video is for adults alone, and needs a signed-in account."}
	case strings.Contains(lower, "members-only"), strings.Contains(lower, "join this channel"), strings.Contains(lower, "members only"):
		return &Refusal{Reason: Members, Message: "This video is for the channel's members alone."}
	case strings.Contains(lower, "premieres in"), strings.Contains(lower, "will begin"), strings.Contains(lower, "is upcoming"), strings.Contains(lower, "scheduled"):
		return &Refusal{Reason: Upcoming, Message: "This video has not begun yet."}
	case strings.Contains(lower, "unavailable"), strings.Contains(lower, "private video"), strings.Contains(lower, "has been removed"),
		strings.Contains(lower, "does not exist"), strings.Contains(lower, "terminated"), strings.Contains(lower, "not available"):
		return &Refusal{Reason: Unavailable, Message: "This video is no longer on the site, or is private."}
	}
	if message == "" {
		message = "yt-dlp stopped"
	}
	return &Refusal{Reason: Failed, Message: "yt-dlp could not read this video: " + message}
}

// tail keeps the last max bytes written to it: what went wrong comes last.
type tail struct {
	mu  sync.Mutex
	b   []byte
	max int
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - t.max; over > 0 {
		t.b = t.b[over:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
