package iptv

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/tools/security"
)

const (
	// firstOpen is how long a new viewer has to open its stream before it
	// is taken to have gone.
	firstOpen = 30 * time.Second
	// stayAfter is how long a viewer whose stream closed stays on its line:
	// a player that reconnects keeps its place.
	stayAfter = 15 * time.Second
	// tookOverFor is how long a device whose line was taken over is told so,
	// once, when it asks for the same channel again.
	tookOverFor = 2 * time.Minute
)

// Viewer is a playing that watches a channel: whose, and on which device.
type Viewer struct {
	Session  string
	User     string
	UserName string
	// Installation is the device's own id: a device watches one channel at
	// a time, so its next channel lets go of its last.
	Installation string
	DeviceName   string
}

// Watching is someone on a line, as a device that finds it full is shown.
type Watching struct {
	User   string `json:"user"`
	Device string `json:"device,omitempty"`
}

// Stream is one line in use, as a device that finds them full is shown.
type Stream struct {
	ID      string     `json:"id"`
	Channel string     `json:"channel"`
	Name    string     `json:"name"`
	Forced  bool       `json:"forced,omitzero"`
	Viewers []Watching `json:"viewers"`
}

// Full is every line of a connection in use: who watches what where, and
// whether one more may be forced.
type Full struct {
	Streams  []Stream
	CanForce bool
}

func (f *Full) Error() string { return "every line of this subscription is in use" }

// TakenOver is a device whose line someone else took.
type TakenOver struct {
	By Watching
}

func (t *TakenOver) Error() string { return "someone else took this line" }

type watcher struct {
	Viewer
	open  int
	timer *time.Timer
}

// line is one stream of a subscription: one channel, opened once for every
// device that watches it.
type line struct {
	id, conn, channel, name, cmd string
	forced                       bool
	watchers                     map[string]*watcher
	// The upstream's bytes while they come, and how to stop them.
	cast    *broadcast
	stop    context.CancelFunc
	opening chan struct{}
	err     error
	closed  bool
}

type eviction struct {
	conn, channel string
	by            Watching
	at            time.Time
}

// Lines are every subscription's lines in use: a channel watched on several
// devices is one stream, a line full is said with who holds it, and a
// device may take one over, or force one more where the connection allows.
type Lines struct {
	// Open starts a line's upstream: the portal's link made and its stream
	// asked for, on no request's context.
	Open func(ctx context.Context, conn, cmd string) (*broadcast, error)
	// Left tells of each viewer that went: its playing ends.
	Left func(session string)
	// FirstOpen and StayAfter are how long a new viewer has to open its
	// stream, and how long one whose stream closed keeps its place.
	FirstOpen, StayAfter time.Duration
	now                  func() time.Time

	mu        sync.Mutex
	lines     map[string]*line
	bySession map[string]*line
	evicted   map[string]eviction
	// shut is Lines after Shutdown: no viewer is told of as gone any more,
	// and leaving counts those being told of, outside the lock.
	shut    bool
	leaving sync.WaitGroup
}

// NewLines keeps lines, starting each with open.
func NewLines(open func(ctx context.Context, conn, cmd string) (*broadcast, error)) *Lines {
	return &Lines{Open: open, Left: func(string) {}, FirstOpen: firstOpen, StayAfter: stayAfter, now: time.Now,
		lines: map[string]*line{}, bySession: map[string]*line{}, evicted: map[string]eviction{}}
}

// Claim is a viewer asking for a channel of a connection: the line already
// open for it, a new one while the subscription has one free, the one it
// takes over, or one more forced — else Full, with who holds each; or
// TakenOver, once, for a device whose line was just taken. The device's
// other channel is let go of first. Answers the line, and the playings
// stopped by a take-over.
func (ls *Lines) Claim(conn, channel, name, cmd string, v Viewer, limit int, canForce bool, takeOver string, force bool) (*line, []string, error) {
	ls.mu.Lock()
	now := ls.now()
	device := deviceKey(v)
	if ev, ok := ls.evicted[device]; ok && device != "" {
		if now.Sub(ev.at) < tookOverFor && ev.conn == conn && ev.channel == channel && takeOver == "" {
			delete(ls.evicted, device)
			ls.mu.Unlock()
			return nil, nil, &TakenOver{By: ev.by}
		}
		if now.Sub(ev.at) >= tookOverFor || ev.conn == conn {
			delete(ls.evicted, device)
		}
	}
	target := ls.find(conn, channel)
	w := &watcher{Viewer: v}
	if target != nil {
		ls.add(target, w)
	}
	// One channel at a time on a device: what it watched before goes.
	var left []string
	if device != "" {
		for _, l := range ls.lines {
			for session, other := range l.watchers {
				if session != v.Session && deviceKey(other.Viewer) == device {
					left = append(left, session)
					ls.removeLocked(l, other)
				}
			}
		}
	}
	var stopped []string
	if target == nil {
		forced := false
		switch {
		case ls.held(conn) < limit:
		case takeOver != "" && ls.lines[takeOver] != nil && ls.lines[takeOver].conn == conn:
			taken := ls.lines[takeOver]
			for session, other := range taken.watchers {
				stopped = append(stopped, session)
				if key := deviceKey(other.Viewer); key != "" {
					ls.evicted[key] = eviction{conn: conn, channel: taken.channel, by: Watching{User: v.UserName, Device: v.DeviceName}, at: now}
				}
			}
			ls.closeLocked(taken, errTaken)
		case force && canForce:
			forced = true
		default:
			full := &Full{Streams: ls.streams(conn), CanForce: canForce}
			left = ls.leavingLocked(left)
			ls.mu.Unlock()
			ls.left(left)
			return nil, nil, full
		}
		target = &line{id: security.RandomStringWithAlphabet(12, "abcdefghijklmnopqrstuvwxyz0123456789"), conn: conn, channel: channel, name: name, cmd: cmd,
			forced: forced, watchers: map[string]*watcher{}}
		ls.lines[target.id] = target
		ls.add(target, w)
	}
	left = ls.leavingLocked(left)
	ls.mu.Unlock()
	ls.left(left)
	return target, stopped, nil
}

func deviceKey(v Viewer) string {
	if v.Installation == "" {
		return ""
	}
	return v.User + "|" + v.Installation
}

func (ls *Lines) find(conn, channel string) *line {
	for _, l := range ls.lines {
		if l.conn == conn && l.channel == channel && !l.closed {
			return l
		}
	}
	return nil
}

// held is how many lines of a connection are in use, those forced beyond
// its limit apart.
func (ls *Lines) held(conn string) int {
	n := 0
	for _, l := range ls.lines {
		if l.conn == conn && !l.forced && len(l.watchers) > 0 {
			n++
		}
	}
	return n
}

// streams are a connection's lines as a device that finds them full is
// shown: the oldest first.
func (ls *Lines) streams(conn string) []Stream {
	out := []Stream{}
	for _, l := range ls.lines {
		if l.conn != conn || len(l.watchers) == 0 {
			continue
		}
		s := Stream{ID: l.id, Channel: l.channel, Name: l.name, Forced: l.forced, Viewers: []Watching{}}
		for _, w := range l.watchers {
			s.Viewers = append(s.Viewers, Watching{User: w.UserName, Device: w.DeviceName})
		}
		slices.SortFunc(s.Viewers, func(a, b Watching) int { return strings.Compare(a.User+a.Device, b.User+b.Device) })
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Stream) int { return strings.Compare(a.Name+a.ID, b.Name+b.ID) })
	return out
}

// add puts a viewer on a line, given a while to open its stream.
func (ls *Lines) add(l *line, w *watcher) {
	l.watchers[w.Session] = w
	ls.bySession[w.Session] = l
	ls.wait(l, w, ls.FirstOpen)
}

func (ls *Lines) wait(l *line, w *watcher, d time.Duration) {
	if w.timer != nil {
		w.timer.Stop()
	}
	if ls.shut {
		return
	}
	w.timer = time.AfterFunc(d, func() {
		ls.mu.Lock()
		// A timer that fired as Lines shut down does nothing: the database
		// its viewer's playing would be written to is closing.
		var left []string
		if !ls.shut && l.watchers[w.Session] == w && w.open == 0 {
			ls.removeLocked(l, w)
			left = ls.leavingLocked([]string{w.Session})
		}
		ls.mu.Unlock()
		ls.left(left)
	})
}

// removeLocked takes a viewer off its line, closing the line once nobody
// watches it.
func (ls *Lines) removeLocked(l *line, w *watcher) {
	if w.timer != nil {
		w.timer.Stop()
	}
	delete(l.watchers, w.Session)
	if ls.bySession[w.Session] == l {
		delete(ls.bySession, w.Session)
	}
	if len(l.watchers) == 0 {
		ls.closeLocked(l, errClosed)
	}
}

func (ls *Lines) closeLocked(l *line, why error) {
	l.closed = true
	for session, w := range l.watchers {
		if w.timer != nil {
			w.timer.Stop()
		}
		delete(ls.bySession, session)
	}
	if l.stop != nil {
		l.stop()
	}
	if l.cast != nil {
		l.cast.end(why)
	}
	delete(ls.lines, l.id)
}

// leavingLocked is the viewers about to be told of as gone, counted until
// left has told of them — none once Lines shut down. Its caller holds the
// lock, and passes what it answers to left once it lets go of it.
func (ls *Lines) leavingLocked(sessions []string) []string {
	if ls.shut || len(sessions) == 0 {
		return nil
	}
	ls.leaving.Add(1)
	return sessions
}

// left tells of viewers that went, as leavingLocked answered them; never
// under the lock, since Left ends their playings.
func (ls *Lines) left(sessions []string) {
	if len(sessions) == 0 {
		return
	}
	defer ls.leaving.Done()
	for _, s := range sessions {
		ls.Left(s)
	}
}

var (
	errClosed = errors.New("nobody watches this line any more")
	errTaken  = errors.New("someone else took this line")
	// ErrNotWatching is a stream asked for by a playing on no line: its
	// line was taken, or it let go of it.
	ErrNotWatching = errors.New("this playing watches no channel now")
)

// Start opens a line's upstream where it is not running — once, however
// many of its viewers ask together — and answers its bytes.
func (ls *Lines) Start(ctx context.Context, l *line) (*broadcast, error) {
	for {
		ls.mu.Lock()
		switch {
		case l.closed:
			ls.mu.Unlock()
			return nil, ErrNotWatching
		case l.cast != nil && !l.cast.ended():
			cast := l.cast
			ls.mu.Unlock()
			return cast, nil
		case l.opening != nil:
			opening := l.opening
			ls.mu.Unlock()
			select {
			case <-opening:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			ls.mu.Lock()
			err := l.err
			ls.mu.Unlock()
			if err != nil {
				return nil, err
			}
			continue
		}
		opening := make(chan struct{})
		l.opening, l.err = opening, nil
		upstream, stop := context.WithCancel(context.Background())
		if l.stop != nil {
			l.stop()
		}
		l.stop = stop
		conn, cmd := l.conn, l.cmd
		ls.mu.Unlock()
		cast, err := ls.Open(upstream, conn, cmd)
		ls.mu.Lock()
		l.opening, l.err = nil, err
		if err == nil {
			if l.closed {
				// Closed while it opened: nobody wants it now.
				stop()
				cast.end(errClosed)
			} else {
				l.cast = cast
			}
		} else {
			stop()
		}
		close(opening)
		ls.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
}

// Attach is a viewer's stream opening: its line, and done for when it
// closes — a viewer with no stream left stays a while, then goes.
func (ls *Lines) Attach(session string) (*line, func(), error) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	l := ls.bySession[session]
	if l == nil || l.closed {
		return nil, nil, ErrNotWatching
	}
	w := l.watchers[session]
	w.open++
	if w.timer != nil {
		w.timer.Stop()
	}
	var once sync.Once
	return l, func() {
		once.Do(func() {
			ls.mu.Lock()
			defer ls.mu.Unlock()
			if l.watchers[session] != w {
				return
			}
			if w.open--; w.open == 0 {
				ls.wait(l, w, ls.StayAfter)
			}
		})
	}, nil
}

// Leave lets a playing go of its line, as its player stopping would.
func (ls *Lines) Leave(session string) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if l := ls.bySession[session]; l != nil {
		if w := l.watchers[session]; w != nil {
			ls.removeLocked(l, w)
		}
	}
}

// Drop takes a viewer off its line at once: its line could not be started.
func (ls *Lines) Drop(session string) { ls.Leave(session) }

// Close closes every line of a connection: its settings changed, or it went.
func (ls *Lines) Close(conn string) {
	ls.mu.Lock()
	var left []string
	for _, l := range ls.lines {
		if l.conn == conn {
			for session := range l.watchers {
				left = append(left, session)
			}
			ls.closeLocked(l, errClosed)
		}
	}
	left = ls.leavingLocked(left)
	ls.mu.Unlock()
	ls.left(left)
}

// Shutdown ends every line as the server stops: every viewer's timer
// stopped, one that fired already made to do nothing, and no viewer told of
// as gone — the database closes next. It returns once any viewer already
// being told of as gone has been. Lines stay shut.
func (ls *Lines) Shutdown() {
	ls.mu.Lock()
	ls.shutLocked()
	ls.mu.Unlock()
	ls.leaving.Wait()
}

func (ls *Lines) shutLocked() {
	ls.shut = true
	for _, l := range ls.lines {
		ls.closeLocked(l, errClosed)
	}
}

// InUse are every connection's lines, for Live Activity.
func (ls *Lines) InUse(conn string) []Stream {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.streams(conn)
}
