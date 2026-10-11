package copies

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"foyer/internal/ffmpeg"
	"foyer/internal/media"
)

// DirName is where copies are kept, in pb_data: never in a backup, which
// would carry gigabytes anyone can make again.
const DirName = "foyer_copies"

// RecipeVersion names how copies are made: raise it when what ffmpeg is
// told changes what comes out, and copies made before are made again.
const RecipeVersion = 1

// retryAfter is how long a copy that could not be made stays so: asked for
// again after it, by someone who pressed Try again, it is tried once more.
const retryAfter = time.Minute

// keepFree is what the disk keeps free whatever else: copies are a cache.
const keepFree = 10 << 30

// Grants open a file to ffmpeg on the loopback.
type Grants interface {
	Grant(file string, ttl time.Duration) (string, func(), error)
	Loopback() string
}

// Settings are the household's for copies, as the FFmpeg adapter says them.
type Settings struct {
	// Tool is ffmpeg; empty, copies are not made — those made are still
	// handed out.
	Tool      string
	Effort    string
	AtOnce    int
	KeepDays  int
	RoomBytes int64
}

// Options are what a Manager needs of the server.
type Options struct {
	// Root is the folder copies are kept in.
	Root     string
	Settings func() Settings
	Grants   Grants
	// Sign signs a made copy's addresses.
	Sign Signer
	Log  func(msg string, args ...any)
}

// Request is a copy asked for: which file it is made from, how, and who
// asked — what Live Activity shows.
type Request struct {
	Key    string
	File   string
	Source media.Version
	Recipe Recipe
	Title  string
	User   string
	// Make, where set, makes the copy instead of ffmpeg's recipe on a file
	// of the share's — a site's own renditions fetched and put together.
	// Recipe then only says what it comes to: its Encode, its Bytes.
	Make Maker
}

// Maker makes a copy in dir, which is its own and emptied after, with the
// ffmpeg the household set to put it together, saying how far it has got
// and how fast — bytes a second — and answers the file it made and what it
// is.
type Maker func(ctx context.Context, dir, ffmpeg string, progress func(done, speed float64)) (file string, made media.Version, err error)

// fetchesAtOnce is how many copies a Maker makes at once: they wait on the
// network, not the processor, so never behind a film being made anew.
const fetchesAtOnce = 2

// Key is a copy's name: the file it is made from — as it is now — and what
// it is made to.
func Key(file string, size int64, modified string, e Encode) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "copy v%d|%s|%d|%s|%s", RecipeVersion, file, size, modified, e.ID()))
	return hex.EncodeToString(sum[:16])
}

// States a copy is in.
const (
	Queued  = "queued"
	Running = "running"
	Done    = "done"
	Failed  = "failed"
)

// Status is where a copy has got to.
type Status struct {
	State    string
	Progress float64
	EtaMs    int64
	Err      string
	// Made is the copy's record, once it is made.
	Made *Made
}

// Made is a copy kept: its record, beside it on disk.
type Made struct {
	Key     string        `json:"key"`
	File    string        `json:"file"`
	Title   string        `json:"title"`
	Encode  Encode        `json:"encode"`
	Version media.Version `json:"version"`
	Name    string        `json:"name"`
	Size    int64         `json:"size"`
	Made    time.Time     `json:"made"`
	Asked   time.Time     `json:"asked"`
	// By is who asked for it first.
	By string `json:"by,omitempty"`
}

// Making is a copy being made, as Live Activity shows it.
type Making struct {
	Key      string    `json:"key"`
	File     string    `json:"file"`
	Title    string    `json:"title"`
	By       string    `json:"by,omitempty"`
	Encode   Encode    `json:"encode"`
	State    string    `json:"state"`
	Progress float64   `json:"progress"`
	Speed    float64   `json:"speed,omitzero"`
	EtaMs    int64     `json:"etaMs,omitzero"`
	Bytes    int64     `json:"estimatedBytes,omitzero"`
	Asked    time.Time `json:"asked"`
	Started  time.Time `json:"started,omitzero"`
	Error    string    `json:"error,omitempty"`
}

type job struct {
	Request
	state    string
	progress float64
	speed    float64
	asked    time.Time
	started  time.Time
	failedAt time.Time
	err      string
	cancel   context.CancelFunc
}

// Manager makes copies, a few at a time, in the order they were asked for,
// and keeps them a while after they were last asked for.
type Manager struct {
	o         Options
	mu        sync.Mutex
	jobs      map[string]*job
	running   int
	fetching  int
	wake      chan struct{}
	abilities map[string]Abilities
	closed    bool
}

// New is a manager keeping its copies under o.Root.
func New(o Options) *Manager {
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	return &Manager{o: o, jobs: map[string]*job{}, wake: make(chan struct{}, 1), abilities: map[string]Abilities{}}
}

// Available says whether copies are made at all.
func (m *Manager) Available() bool {
	return m.o.Settings().Tool != ""
}

// Abilities are what this server's ffmpeg makes copies with, asked of it
// once.
func (m *Manager) Abilities(ctx context.Context) (Abilities, bool) {
	tool := m.o.Settings().Tool
	if tool == "" {
		return Abilities{}, false
	}
	m.mu.Lock()
	can, ok := m.abilities[tool]
	m.mu.Unlock()
	if ok {
		return can, len(can.Encoders) > 0
	}
	can, err := Detect(ctx, tool)
	if err != nil {
		m.o.Log("What ffmpeg can make copies with could not be read", "error", err.Error())
		return Abilities{}, false
	}
	m.mu.Lock()
	m.abilities[tool] = can
	m.mu.Unlock()
	return can, len(can.Encoders) > 0
}

// Ask is a copy asked for: made already, being made, or queued now.
func (m *Manager) Ask(r Request) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if made, ok := m.made(r.Key); ok {
		made.Asked = now
		if err := m.keep(made); err != nil {
			m.o.Log("A copy's record could not be kept", "error", err.Error())
		}
		return Status{State: Done, Progress: 1, Made: made}
	}
	if j := m.jobs[r.Key]; j != nil {
		j.asked = now
		if j.state != Failed || now.Sub(j.failedAt) < retryAfter {
			return j.status()
		}
		delete(m.jobs, r.Key)
	}
	if m.closed || !m.Available() {
		return Status{State: Failed, Err: "Making copies is switched off on this Foyer."}
	}
	m.jobs[r.Key] = &job{Request: r, state: Queued, asked: now}
	m.poke()
	return Status{State: Queued}
}

func (j *job) status() Status {
	s := Status{State: j.state, Progress: j.progress, Err: j.err}
	switch {
	case j.state != Running || j.speed <= 0:
	case j.Make != nil && j.Recipe.Bytes > 0:
		// A fetch's speed is bytes a second.
		s.EtaMs = int64(float64(j.Recipe.Bytes) * (1 - j.progress) / j.speed * 1000)
	case j.Make == nil && j.Source.DurationMs > 0:
		// A recipe's is how many times faster than the film plays.
		s.EtaMs = int64(float64(j.Source.DurationMs) * (1 - j.progress) / j.speed)
	}
	return s
}

func (m *Manager) poke() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Run makes what is asked for until ctx ends: what an earlier run left half
// made is let go first, and what was kept too long once an hour.
func (m *Manager) Run(ctx context.Context) {
	if err := os.MkdirAll(m.o.Root, 0o700); err != nil {
		m.o.Log("The folder for copies could not be made", "error", err.Error())
		return
	}
	m.clearParts()
	m.prune(0)
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		m.startDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		case <-tick.C:
			m.prune(0)
		}
	}
}

// startDue starts what is queued, the longest waiting first, as many at
// once as the household allows — and fetches beside them, a few at once.
func (m *Manager) startDue(ctx context.Context) {
	settings := m.o.Settings()
	m.mu.Lock()
	defer m.mu.Unlock()
	queued := []*job{}
	for _, j := range m.jobs {
		if j.state == Queued {
			queued = append(queued, j)
		}
	}
	slices.SortFunc(queued, func(a, b *job) int { return a.asked.Compare(b.asked) })
	for _, next := range queued {
		lane := &m.running
		limit := max(settings.AtOnce, 1)
		if next.Make != nil {
			lane, limit = &m.fetching, fetchesAtOnce
		}
		if m.closed || *lane >= limit {
			continue
		}
		jobCtx, cancel := context.WithCancel(ctx)
		next.state, next.started, next.cancel = Running, time.Now(), cancel
		*lane++
		go func() {
			err := m.make(jobCtx, next, settings)
			stopped := jobCtx.Err() != nil
			cancel()
			m.mu.Lock()
			*lane--
			// A copy stopped and asked for again is another job by now.
			if m.jobs[next.Key] == next {
				switch {
				case err == nil, stopped:
					// Made — on disk now — or stopped: nothing more is kept of it here.
					delete(m.jobs, next.Key)
				default:
					next.state, next.err, next.failedAt = Failed, err.Error(), time.Now()
					m.o.Log("A copy could not be made", "file", next.File, "error", next.err)
				}
			}
			m.mu.Unlock()
			m.poke()
		}()
	}
}

// make runs ffmpeg for one copy, or its Maker, and keeps what it made.
func (m *Manager) make(ctx context.Context, j *job, settings Settings) error {
	if err := m.room(j.Recipe.Bytes, settings); err != nil {
		return err
	}
	if j.Make != nil {
		return m.fetch(ctx, j, settings)
	}
	input, revoke, err := m.o.Grants.Grant(j.File, 24*time.Hour)
	if err != nil {
		return err
	}
	defer revoke()
	// A part of its own: a copy stopped and asked for again writes beside it.
	part := filepath.Join(m.o.Root, fmt.Sprintf("%s-%d.part", j.Key, time.Now().UnixNano()))
	defer os.Remove(part)
	cmd := ffmpeg.Command(ctx, settings.Tool, j.Recipe.Args(input, part, settings.Effort)...)
	// SVT-AV1 prints its whole configuration otherwise, and what went wrong
	// would be lost behind it.
	cmd.Env = append(cmd.Env, "SVT_LOG=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	said := &tail{max: 4 << 10}
	cmd.Stderr = said
	if err := cmd.Start(); err != nil {
		return err
	}
	// A copy waits on whatever a player needs: it runs at a lower priority.
	lower(cmd)
	m.follow(j, stdout)
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New(redact(strings.TrimSpace(said.String()), input, m.o.Grants.Loopback()))
	}
	info, err := os.Stat(part)
	if err != nil {
		return err
	}
	name := j.Key + "." + j.Recipe.Encode.Container
	if err := os.Rename(part, filepath.Join(m.o.Root, name)); err != nil {
		return err
	}
	now := time.Now()
	made := &Made{Key: j.Key, File: j.File, Title: j.Title, Encode: j.Recipe.Encode, Version: j.Recipe.Made(j.Source, info.Size()),
		Name: name, Size: info.Size(), Made: now, Asked: now, By: j.User}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.keep(made)
}

// fetch makes a copy by its Maker, in a folder of its own that goes with
// whatever is left in it.
func (m *Manager) fetch(ctx context.Context, j *job, settings Settings) error {
	dir := filepath.Join(m.o.Root, fmt.Sprintf("%s-%d.part", j.Key, time.Now().UnixNano()))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file, version, err := j.Make(ctx, dir, settings.Tool, func(done, speed float64) {
		m.mu.Lock()
		j.progress, j.speed = min(max(done, 0), 0.999), speed
		m.mu.Unlock()
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	info, err := os.Stat(file)
	if err != nil {
		return err
	}
	name := j.Key + filepath.Ext(file)
	if err := os.Rename(file, filepath.Join(m.o.Root, name)); err != nil {
		return err
	}
	version.Size = info.Size()
	now := time.Now()
	made := &Made{Key: j.Key, File: j.File, Title: j.Title, Encode: j.Recipe.Encode, Version: version,
		Name: name, Size: info.Size(), Made: now, Asked: now, By: j.User}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.keep(made)
}

// follow reads ffmpeg's progress: how far into the film it is, and how fast.
func (m *Manager) follow(j *job, out io.Reader) {
	lines := bufio.NewScanner(out)
	for lines.Scan() {
		key, value, ok := strings.Cut(lines.Text(), "=")
		if !ok {
			continue
		}
		switch key {
		case "out_time_us":
			if us, err := strconv.ParseInt(value, 10, 64); err == nil && j.Source.DurationMs > 0 {
				m.mu.Lock()
				j.progress = min(max(float64(us/1000)/float64(j.Source.DurationMs), 0), 0.999)
				m.mu.Unlock()
			}
		case "speed":
			if speed, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(value), "x"), 64); err == nil {
				m.mu.Lock()
				j.speed = speed
				m.mu.Unlock()
			}
		}
	}
}

// Path is a made copy's file and its record, to hand to a device.
func (m *Manager) Path(key string) (string, *Made, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	made, ok := m.made(key)
	if !ok {
		return "", nil, false
	}
	return filepath.Join(m.o.Root, made.Name), made, true
}

// made reads a copy's record, where its file is there too. Its caller holds
// the lock.
func (m *Manager) made(key string) (*Made, bool) {
	if !validKey(key) {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(m.o.Root, key+".json"))
	if err != nil {
		return nil, false
	}
	var made Made
	if json.Unmarshal(b, &made) != nil || made.Key != key || made.Name != filepath.Base(made.Name) {
		return nil, false
	}
	if _, err := os.Stat(filepath.Join(m.o.Root, made.Name)); err != nil {
		return nil, false
	}
	return &made, true
}

// keep writes a copy's record. Its caller holds the lock.
func (m *Manager) keep(made *Made) error {
	b, err := json.Marshal(made)
	if err != nil {
		return err
	}
	tmp := filepath.Join(m.o.Root, made.Key+".json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(m.o.Root, made.Key+".json"))
}

// Making are the copies being made and waiting.
func (m *Manager) Making() []Making {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Making{}
	for _, j := range m.jobs {
		s := j.status()
		// A fetch's speed is in bytes, not against the film: shown by its time left alone.
		speed := j.speed
		if j.Make != nil {
			speed = 0
		}
		out = append(out, Making{Key: j.Key, File: j.File, Title: j.Title, By: j.User, Encode: j.Recipe.Encode, State: j.state,
			Progress: j.progress, Speed: speed, EtaMs: s.EtaMs, Bytes: j.Recipe.Bytes, Asked: j.asked, Started: j.started, Error: j.err})
	}
	slices.SortFunc(out, func(a, b Making) int { return a.Asked.Compare(b.Asked) })
	return out
}

// Kept are the copies made, the last asked for first.
func (m *Manager) Kept() []Made {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.all()
	slices.SortFunc(kept, func(a, b Made) int { return b.Asked.Compare(a.Asked) })
	return kept
}

// all are every copy's record. Its caller holds the lock.
func (m *Manager) all() []Made {
	entries, err := os.ReadDir(m.o.Root)
	if err != nil {
		return []Made{}
	}
	out := []Made{}
	for _, e := range entries {
		if key, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			if made, ok := m.made(key); ok {
				out = append(out, *made)
			}
		}
	}
	return out
}

// Stop ends a copy being made, or one waiting.
func (m *Manager) Stop(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[key]
	if j == nil {
		return false
	}
	if j.cancel != nil {
		j.cancel()
	}
	delete(m.jobs, key)
	return true
}

// Remove lets a made copy go.
func (m *Manager) Remove(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	made, ok := m.made(key)
	if !ok {
		return false
	}
	m.drop(*made)
	return true
}

// drop removes a copy and its record. Its caller holds the lock.
func (m *Manager) drop(made Made) {
	_ = os.Remove(filepath.Join(m.o.Root, made.Key+".json"))
	_ = os.Remove(filepath.Join(m.o.Root, made.Name))
}

// Close stops every copy being made: the server is going.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for _, j := range m.jobs {
		if j.cancel != nil {
			j.cancel()
		}
	}
}

// clearParts lets go of what an earlier run left half made.
func (m *Manager) clearParts() {
	entries, err := os.ReadDir(m.o.Root)
	if err != nil {
		return
	}
	for _, e := range entries {
		// A fetch's part is a folder.
		if strings.HasSuffix(e.Name(), ".part") || strings.HasSuffix(e.Name(), ".json.tmp") {
			_ = os.RemoveAll(filepath.Join(m.o.Root, e.Name()))
		}
	}
}

// prune lets go of copies nobody asked for in the household's days, then
// of the least lately asked for until what is kept, and need more, fit the
// room it allows.
func (m *Manager) prune(need int64) {
	settings := m.o.Settings()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(need, settings)
}

func (m *Manager) pruneLocked(need int64, settings Settings) {
	kept := m.all()
	slices.SortFunc(kept, func(a, b Made) int { return a.Asked.Compare(b.Asked) })
	var total int64
	stay := kept[:0]
	for _, made := range kept {
		if settings.KeepDays > 0 && time.Since(made.Asked) > time.Duration(settings.KeepDays)*24*time.Hour {
			m.drop(made)
			continue
		}
		stay = append(stay, made)
		total += made.Size
	}
	for len(stay) > 0 && (settings.RoomBytes > 0 && total+need > settings.RoomBytes || free(m.o.Root) >= 0 && free(m.o.Root)-need < keepFree) {
		// What was asked for within the hour may be coming down now.
		if time.Since(stay[0].Asked) < time.Hour {
			break
		}
		m.drop(stay[0])
		total -= stay[0].Size
		stay = stay[1:]
	}
}

// room makes room for a copy of about need bytes, or says there is none.
func (m *Manager) room(need int64, settings Settings) error {
	// The estimate is an estimate: room for a fifth more.
	need += need / 5
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(need, settings)
	if f := free(m.o.Root); f >= 0 && f-need < keepFree {
		return fmt.Errorf("this server has no room for the copy: %d GB free", f>>30)
	}
	return nil
}

func validKey(key string) bool {
	if len(key) != 32 {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil
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

// redact clears what ffmpeg said of every address in it: the loopback's
// grants open a file to whoever holds them.
func redact(said, input, loopback string) string {
	said = strings.ReplaceAll(said, input, "<input>")
	if loopback != "" {
		said = strings.ReplaceAll(said, loopback, "<loopback>")
	}
	if len(said) > 500 {
		said = said[len(said)-500:]
	}
	if said == "" {
		said = "ffmpeg stopped"
	}
	return said
}

// Detect asks ffmpeg which encoders and filters it has.
func Detect(ctx context.Context, tool string) (Abilities, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	list := func(what string) (string, error) {
		out, err := ffmpeg.Command(ctx, tool, "-hide_banner", "-"+what).Output()
		var exit *exec.ExitError
		if errors.As(err, &exit) || err == nil {
			return string(out), nil
		}
		return "", err
	}
	encodersSaid, err := list("encoders")
	if err != nil {
		return Abilities{}, err
	}
	filtersSaid, err := list("filters")
	if err != nil {
		return Abilities{}, err
	}
	can := Abilities{Encoders: map[string]bool{}}
	for _, line := range strings.Split(encodersSaid, "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 {
			for _, want := range []string{"libsvtav1", "libx265", "libx264", "libopus", "aac"} {
				if fields[1] == want {
					can.Encoders[want] = true
				}
			}
		}
	}
	filters := map[string]bool{}
	for _, line := range strings.Split(filtersSaid, "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 {
			filters[fields[1]] = true
		}
	}
	switch {
	case filters["tonemapx"]:
		can.ToneMap = ToneMapX
	case filters["zscale"] && filters["tonemap"]:
		can.ToneMap = ToneMapZ
	}
	return can, nil
}
