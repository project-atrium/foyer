package hls

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"foyer/internal/ffmpeg"
	"foyer/internal/media"
)

const (
	// aheadMax is how far a run may get ahead of what was last asked for
	// before it waits: a 4K film is about 60 MB a segment.
	aheadMax = 8
	// aheadResume is how near what is asked for comes before it goes on.
	aheadResume = 4
	// keepBehind is how many segments before what was asked for are kept,
	// for a step back.
	keepBehind = 3
	// restartAhead is how far past what the run has made a request may land
	// and still be waited for; further, the run starts again there.
	restartAhead = 3
	// waitFor is how long a request waits for its segment.
	waitFor = 60 * time.Second
	// pollEvery is how often a run's lists of finished segments are read.
	pollEvery = 150 * time.Millisecond
)

var (
	// ErrFailed is a remux ffmpeg could not make.
	ErrFailed = errors.New("the remux failed")
	// ErrNotYet is a segment that did not come in time.
	ErrNotYet = errors.New("the segment did not come in time")
)

// Grants open a file to ffmpeg on the loopback.
type Grants interface {
	Grant(file string, ttl time.Duration) (string, func(), error)
	Loopback() string
}

// Playings are what each remux belongs to, as stream addresses do.
type Playings interface {
	Open(r *http.Request, session, user, file, mode string, expires time.Time) (context.Context, func(), bool)
	Sent(session string, n int)
	// Speed is how fast the remux of a playing runs, as ffmpeg says.
	Speed(session string, speed float64)
}

// Manager runs the remuxes: a job per playing, a run of ffmpeg in it.
type Manager struct {
	log  func(msg string, args ...any)
	root string
	// sign signs a ticket, with a key drawn from the users' file-token secret.
	sign func(payload []byte) ([]byte, error)
	// tool is ffmpeg's path, while the FFmpeg adapter is switched on.
	tool func() (string, bool)
	// converting says whether the household lets Foyer convert for a player.
	converting func() bool
	grants     Grants
	index      func(ctx context.Context, file string) (Index, error)
	version    func(file string) (media.Version, error)
	playings   Playings

	mu   sync.Mutex
	jobs map[string]*job
}

// Options are what a Manager is made with.
type Options struct {
	// Log says what went wrong, where nobody asked.
	Log func(msg string, args ...any)
	// Root is where the segments are written; anything there is gone at start.
	Root string
	Sign func([]byte) ([]byte, error)
	Tool func() (string, bool)
	// Converting is the household's word: converting for players is plan z,
	// and off until it says otherwise.
	Converting func() bool
	// Grants open a file to ffmpeg, by its id.
	Grants Grants
	// Index is where a file's keyframes lie, Version what it is.
	Index    func(ctx context.Context, file string) (Index, error)
	Version  func(file string) (media.Version, error)
	Playings Playings
}

// NewManager makes a Manager, clearing what an earlier one left.
func NewManager(o Options) *Manager {
	_ = os.RemoveAll(o.Root)
	log := o.Log
	if log == nil {
		log = func(string, ...any) {}
	}
	converting := o.Converting
	if converting == nil {
		converting = func() bool { return true }
	}
	return &Manager{log: log, root: o.Root, sign: o.Sign, tool: o.Tool, converting: converting, grants: o.Grants, index: o.Index,
		version: o.Version, playings: o.Playings, jobs: map[string]*job{}}
}

// Available says whether a remux can run now: ffmpeg is set up, and the
// household lets Foyer convert for players.
func (m *Manager) Available() bool {
	_, ok := m.tool()
	return ok && m.converting()
}

// End ends a playing's remux: its run stopped, its segments gone.
func (m *Manager) End(session string) {
	m.mu.Lock()
	j := m.jobs[session]
	delete(m.jobs, session)
	m.mu.Unlock()
	if j != nil {
		j.close()
	}
}

// Close ends every remux.
func (m *Manager) Close() {
	m.mu.Lock()
	jobs := m.jobs
	m.jobs = map[string]*job{}
	m.mu.Unlock()
	for _, j := range jobs {
		j.close()
	}
}

// Active is how many playings have a remux, for the tests.
func (m *Manager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.jobs)
}

// Running is how many remuxes have a run of ffmpeg going, for the tests.
func (m *Manager) Running() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, j := range m.jobs {
		j.mu.Lock()
		if j.run != nil && !j.run.over() {
			n++
		}
		j.mu.Unlock()
	}
	return n
}

// jobOf is a ticket's job: the one running, or a new one, its file read
// again, its index and its version as the file keeps them.
func (m *Manager) jobOf(ctx context.Context, t Ticket) (*job, error) {
	m.mu.Lock()
	if j := m.jobs[t.Session]; j != nil {
		m.mu.Unlock()
		return j, nil
	}
	m.mu.Unlock()
	idx, err := m.index(ctx, t.File)
	if err != nil {
		return nil, err
	}
	version, err := m.version(t.File)
	if err != nil {
		return nil, err
	}
	expires := time.Until(time.Unix(t.Expires, 0))
	input, revoke, err := m.grants.Grant(t.File, expires+time.Minute)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(m.root, t.Session)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		revoke()
		return nil, err
	}
	segments := Segments(idx)
	j := &job{m: m, ticket: t, version: version, index: idx, segments: segments, dir: dir, input: input, revoke: revoke,
		changed: make(chan struct{}), done: map[string]map[int]string{}, inits: map[string][]byte{},
		cues: make([][]cue, len(t.Plan.Subtitles)), asked: SegmentAt(segments, t.Plan.StartMs)}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.jobs[t.Session]; existing != nil {
		revoke()
		return existing, nil
	}
	m.jobs[t.Session] = j
	return j, nil
}

// job is one playing's remux.
type job struct {
	m        *Manager
	ticket   Ticket
	version  media.Version
	index    Index
	segments []Segment
	dir      string
	input    string
	revoke   func()

	mu      sync.Mutex
	changed chan struct{}
	run     *run
	runs    int
	// done are the segments finished and on disk, by kind — v, a0, a1… —
	// each the file that holds it.
	done  map[string]map[int]string
	inits map[string][]byte
	// cues are each subtitle rendition's, from every run so far.
	cues [][]cue
	// covered are the spans of the timeline runs got through.
	covered []span
	// asked is the segment last asked for, picture or sound: where the
	// player is.
	asked  int
	err    error
	closed bool
}

type span struct{ from, to int64 }

// run is one ffmpeg, from one segment on.
type run struct {
	id     int
	start  int
	files  runFiles
	cancel context.CancelFunc
	cmd    *exec.Cmd
	exited chan struct{}
	// produced is the last segment of the picture this run finished; made,
	// each rendition's — a sound made anew falls behind a picture copied.
	produced int
	made     map[string]int
	// span is the run's entry in covered.
	span   int
	paused bool
	err    error
	listAt map[string]int64
	vttAt  []int64
	errOut *bytes.Buffer
}

// madeOf is the last segment of a rendition this run finished.
func (r *run) madeOf(kind string) int {
	if n, ok := r.made[kind]; ok {
		return n
	}
	return r.start - 1
}

// slowest is the last segment every one of the renditions named has.
func (r *run) slowest(kinds ...string) int {
	slowest := r.produced
	for _, kind := range kinds {
		slowest = min(slowest, r.madeOf(kind))
	}
	return slowest
}

func (r *run) over() bool {
	select {
	case <-r.exited:
		return true
	default:
		return false
	}
}

// kinds are the job's renditions made of segments: the picture, and each
// sound.
func (j *job) kinds() []string {
	kinds := []string{"v"}
	for k := range j.ticket.Plan.Audio {
		kinds = append(kinds, "a"+strconv.Itoa(k))
	}
	return kinds
}

// broadcast tells every request waiting that something changed. Its caller
// holds the lock.
func (j *job) broadcast() {
	close(j.changed)
	j.changed = make(chan struct{})
}

// await waits until ready holds — it is asked under the lock — or the wait
// or the request ends.
func (j *job) await(ctx context.Context, ready func() (bool, error)) error {
	deadline := time.NewTimer(waitFor)
	defer deadline.Stop()
	for {
		j.mu.Lock()
		if j.closed {
			j.mu.Unlock()
			return context.Canceled
		}
		ok, err := ready()
		changed := j.changed
		j.mu.Unlock()
		if ok || err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return ErrNotYet
		case <-changed:
		}
	}
}

// need makes sure segment n of the picture or a sound is being made: by the
// run going, once it is near enough and goes on, or by a new run from n.
// Only the picture moves a run that is going: a player asks for a sound
// it is not playing too, ahead or behind, and a run started there would
// leave the picture. Its caller holds the lock.
func (j *job) need(kind string, n int) error {
	if j.done[kind][n] != "" {
		return nil
	}
	r := j.run
	going := r != nil && !r.over()
	if kind == "v" || !going {
		j.asked = n
	}
	if going && n >= r.start && n <= r.madeOf(kind)+restartAhead {
		// A run waiting goes on once the rendition furthest behind is near.
		if r.paused && r.slowest(j.kinds()...)-n < aheadResume {
			r.resume()
		}
		return nil
	}
	if going && kind != "v" {
		return nil
	}
	if r != nil && r.over() && r.err != nil && r.produced < r.start && n == r.start {
		// The run from here failed before it made anything: do not loop on it.
		return fmt.Errorf("%w: %s", ErrFailed, j.err)
	}
	return j.start(n)
}

// start begins a run of ffmpeg from segment n, ending the one before. Its
// caller holds the lock.
func (j *job) start(n int) error {
	if j.run != nil {
		j.run.stop()
	}
	tool, ok := j.m.tool()
	if !ok {
		return fmt.Errorf("%w: ffmpeg is switched off", ErrFailed)
	}
	j.runs++
	files := runFiles{dir: j.dir, run: j.runs}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := ffmpeg.Command(ctx, tool, commandFor(j.input, j.index, j.segments, n, j.ticket.Plan, files)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	r := &run{id: j.runs, start: n, files: files, cancel: cancel, cmd: cmd, exited: make(chan struct{}), produced: n - 1, made: map[string]int{},
		listAt: map[string]int64{}, vttAt: make([]int64, len(j.ticket.Plan.Subtitles)), errOut: &bytes.Buffer{}}
	cmd.Stderr = &limited{w: r.errOut, left: 8 << 10}
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("%w: %w", ErrFailed, err)
	}
	j.run = r
	r.span = len(j.covered)
	j.covered = append(j.covered, span{from: j.segments[n].StartMs, to: j.segments[n].StartMs})
	go j.progress(r, stdout)
	go func() {
		err := cmd.Wait()
		j.mu.Lock()
		j.poll(r)
		if err != nil && ctx.Err() == nil {
			r.err = err
			if r.produced < r.start {
				j.err = errors.New(redact(strings.TrimSpace(r.errOut.String()), j.input, j.m.grants.Loopback()))
			}
		}
		close(r.exited)
		j.broadcast()
		j.mu.Unlock()
	}()
	go func() {
		tick := time.NewTicker(pollEvery)
		defer tick.Stop()
		for {
			select {
			case <-r.exited:
				return
			case <-tick.C:
				j.mu.Lock()
				if j.poll(r) {
					j.broadcast()
				}
				j.mu.Unlock()
			}
		}
	}()
	return nil
}

// progress reads ffmpeg's progress for its speed, which Live Activity shows.
func (j *job) progress(r *run, out io.Reader) {
	lines := bufio.NewScanner(out)
	for lines.Scan() {
		value, ok := strings.CutPrefix(lines.Text(), "speed=")
		if !ok {
			continue
		}
		if speed, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(value), "x"), 64); err == nil {
			j.m.playings.Speed(j.ticket.Session, speed)
		}
	}
}

// poll reads what a run finished since it last looked: its lists' new
// lines, its subtitles' new cues. It answers whether anything changed, and
// keeps the run within reach of the player. Its caller holds the lock.
func (j *job) poll(r *run) bool {
	changed := false
	kinds := j.kinds()
	for _, kind := range kinds {
		for _, n := range readList(r.files.list(kind), r.listAt, kind, r.id) {
			if j.done[kind] == nil {
				j.done[kind] = map[int]string{}
			}
			if j.done[kind][n] == "" {
				j.done[kind][n] = r.files.segment(kind, n)
			} else {
				// An earlier run's is kept, and may be being read.
				_ = os.Remove(r.files.segment(kind, n))
			}
			changed = true
			if n > r.madeOf(kind) {
				r.made[kind] = n
			}
			if kind == "v" && n > r.produced {
				r.produced = n
				j.covered[r.span].to = j.segments[n].EndMs
			}
		}
	}
	for k := range j.ticket.Plan.Subtitles {
		b, err := readFrom(r.files.subtitles(k), r.vttAt[k])
		if err != nil || len(b) == 0 {
			continue
		}
		cues, read := readCues(b)
		r.vttAt[k] += int64(read)
		for _, c := range cues {
			if !containsCue(j.cues[k], c) {
				j.cues[k] = append(j.cues[k], c)
				changed = true
			}
		}
	}
	if changed {
		j.prune()
		// Waits only once every rendition is far enough ahead: paused, the
		// one behind would never catch up.
		if r == j.run && !r.paused && r.slowest(kinds...)-j.asked >= aheadMax {
			r.pause()
		}
	}
	return changed
}

// prune lets go of segments the player has gone past — a few kept for a
// step back — and of an earlier run's ahead of it, which a seek left. The
// run going keeps what it made ahead: its renditions do not keep pace, and
// it waits once the slowest is far enough ahead. Its caller holds the lock.
func (j *job) prune() {
	current := ""
	if j.run != nil {
		current = fmt.Sprintf("-r%d_", j.run.id)
	}
	for _, done := range j.done {
		for n, path := range done {
			behind := n < j.asked-keepBehind
			left := n > j.asked+aheadMax+restartAhead+keepBehind && !strings.Contains(filepath.Base(path), current)
			if behind || left {
				// One being read is read to its end: the file goes once it is closed.
				_ = os.Remove(path)
				delete(done, n)
			}
		}
	}
}

// close stops the run and lets go of everything the job wrote.
func (j *job) close() {
	j.mu.Lock()
	if j.closed {
		j.mu.Unlock()
		return
	}
	j.closed = true
	r := j.run
	j.broadcast()
	j.mu.Unlock()
	if r != nil {
		r.stop()
		<-r.exited
	}
	j.revoke()
	_ = os.RemoveAll(j.dir)
}

func (r *run) stop() {
	r.resume()
	r.cancel()
}

// readList reads the lines a segment list gained since at: each one a
// segment ffmpeg finished, `v-r2_12.mp4,start,end`.
func readList(path string, at map[string]int64, kind string, run int) []int {
	b, err := readFrom(path, at[kind])
	if err != nil || len(b) == 0 {
		return nil
	}
	end := bytes.LastIndexByte(b, '\n')
	if end < 0 {
		return nil
	}
	at[kind] += int64(end + 1)
	var finished []int
	for _, line := range strings.Split(string(b[:end]), "\n") {
		name, _, _ := strings.Cut(line, ",")
		number, ok := strings.CutPrefix(strings.TrimSuffix(filepath.Base(name), ".mp4"), fmt.Sprintf("%s-r%d_", kind, run))
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(number); err == nil {
			finished = append(finished, n)
		}
	}
	return finished
}

// readFrom reads a file from an offset on: what was written since.
func readFrom(path string, at int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(at, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, 16<<20))
}

func containsCue(cues []cue, c cue) bool {
	for _, have := range cues {
		if have == c {
			return true
		}
	}
	return false
}

// limited keeps the first bytes written to it, and drops the rest.
type limited struct {
	w    io.Writer
	left int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.left > 0 {
		n := min(len(p), l.left)
		_, _ = l.w.Write(p[:n])
		l.left -= n
	}
	return len(p), nil
}

// redact clears what ffmpeg said of every address in it: the loopback's
// grants open a file to whoever holds them.
func redact(said, input, loopback string) string {
	said = strings.ReplaceAll(said, input, "<input>")
	if loopback != "" {
		said = strings.ReplaceAll(said, loopback, "<loopback>")
	}
	if len(said) > 500 {
		said = said[:500]
	}
	if said == "" {
		said = "ffmpeg stopped"
	}
	return said
}
