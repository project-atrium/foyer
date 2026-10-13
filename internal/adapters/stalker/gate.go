package stalker

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Lane is which requests go first: a channel starting and a sign-in, then
// lists, then a download of the whole portal's guide — heavy, and never what
// someone is waiting to see. None given is a list's.
type Lane int

const (
	LanePlay Lane = iota + 1
	LaneList
	LaneGuide
)

const (
	// Gap is the least time between one request setting out and the next: a
	// portal that counts them sees one box, not a burst.
	Gap = 250 * time.Millisecond
	// Quiet is how long after a channel started a guide download waits to
	// begin, or to begin again where a channel stopped it.
	Quiet = 8 * time.Second
)

// Clock is the gate's time: the wall clock, or a test's.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f after d, and answers how to stop it.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }
func (wallClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

type outcome struct {
	value any
	err   error
}

type turn struct {
	lane  Lane
	task  func(context.Context) (any, error)
	ctx   context.Context
	order int
	done  chan outcome
	// While it runs: how to stop it, when it set out, and whether a channel
	// stopped it — then it waits its turn again rather than fail.
	cancel    context.CancelFunc
	startedAt time.Time
	preempted bool
}

// Gate is the one way out to a portal. A portal that counts requests limits
// the address that asks too fast, so requests go out one or a few at a time,
// a quarter of a second apart, in the order a viewer would want: a channel
// starting before a page of films, a page before the whole guide. A channel
// waiting while only guide downloads hold the way stops the youngest; it
// starts again, its waiter none the wiser, once no channel has started for a
// while.
type Gate struct {
	atOnce int
	clock  Clock

	mu        sync.Mutex
	waiting   []*turn
	running   map[*turn]bool
	lastStart time.Time
	lastPlay  time.Time
	placed    int
	stopWake  func() bool
}

// NewGate lets atOnce requests out at a time, on clock — the wall clock
// where nil.
func NewGate(atOnce int, clock Clock) *Gate {
	if clock == nil {
		clock = wallClock{}
	}
	return &Gate{atOnce: max(1, atOnce), clock: clock, running: map[*turn]bool{}}
}

// Run runs one request when its turn comes. Giving up while it waits leaves
// the queue and takes no slot; giving up while it runs stops it.
func (g *Gate) Run(ctx context.Context, lane Lane, task func(context.Context) (any, error)) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	t := &turn{lane: lane, task: task, ctx: ctx, order: g.placed, done: make(chan outcome, 1)}
	g.placed++
	g.waiting = append(g.waiting, t)
	if lane == LanePlay {
		g.preemptLocked()
	}
	g.pumpLocked()
	g.mu.Unlock()
	stop := context.AfterFunc(ctx, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if i := slices.Index(g.waiting, t); i >= 0 {
			g.waiting = slices.Delete(g.waiting, i, i+1)
			t.done <- outcome{err: ctx.Err()}
		}
	})
	defer stop()
	o := <-t.done
	return o.value, o.err
}

// due says whether a turn may go now: a guide waits for the quiet after a
// channel started.
func (g *Gate) due(t *turn, now time.Time) bool {
	return t.lane != LaneGuide || g.lastPlay.IsZero() || !now.Before(g.lastPlay.Add(Quiet))
}

// nextLocked is the next to go: the first of the highest lane that may go now.
func (g *Gate) nextLocked(now time.Time) *turn {
	var best *turn
	for _, t := range g.waiting {
		if !g.due(t, now) {
			continue
		}
		if best == nil || t.lane < best.lane || t.lane == best.lane && t.order < best.order {
			best = t
		}
	}
	return best
}

func (g *Gate) laterLocked(d time.Duration) {
	if g.stopWake != nil {
		g.stopWake()
	}
	g.stopWake = g.clock.AfterFunc(max(0, d), func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.stopWake = nil
		g.pumpLocked()
	})
}

func (g *Gate) pumpLocked() {
	for len(g.running) < g.atOnce && len(g.waiting) > 0 {
		now := g.clock.Now()
		t := g.nextLocked(now)
		if t == nil {
			// Only guides wait, for the quiet after a channel started.
			g.laterLocked(g.lastPlay.Add(Quiet).Sub(now))
			return
		}
		// After the gap the queue is looked at afresh: a channel asked for
		// meanwhile goes first.
		if !g.lastStart.IsZero() {
			if gap := g.lastStart.Add(Gap).Sub(now); gap > 0 {
				g.laterLocked(gap)
				return
			}
		}
		g.startLocked(t, now)
	}
}

func (g *Gate) startLocked(t *turn, now time.Time) {
	g.waiting = slices.Delete(g.waiting, slices.Index(g.waiting, t), slices.Index(g.waiting, t)+1)
	g.lastStart = now
	if t.lane == LanePlay {
		g.lastPlay = now
	}
	ctx, cancel := context.WithCancel(t.ctx)
	t.cancel, t.startedAt, t.preempted = cancel, now, false
	g.running[t] = true
	go func() {
		value, err := t.task(ctx)
		cancel()
		g.mu.Lock()
		defer g.mu.Unlock()
		delete(g.running, t)
		switch {
		case err != nil && t.preempted && t.ctx.Err() == nil:
			// Stopped for a channel: its turn again, its place kept.
			g.waiting = append(g.waiting, t)
		case err != nil && t.ctx.Err() != nil:
			t.done <- outcome{err: t.ctx.Err()}
		default:
			t.done <- outcome{value: value, err: err}
		}
		g.pumpLocked()
	}()
}

// preemptLocked stops the youngest guide download where only guides hold
// every slot: a channel waits for nothing heavier.
func (g *Gate) preemptLocked() {
	if len(g.running) < g.atOnce {
		return
	}
	var youngest *turn
	for t := range g.running {
		if t.lane != LaneGuide || t.preempted {
			continue
		}
		if youngest == nil || t.startedAt.After(youngest.startedAt) {
			youngest = t
		}
	}
	if youngest == nil {
		return
	}
	youngest.preempted = true
	youngest.cancel()
}
