package stalker

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeClock is a clock whose timers fire only as the test moves it on.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !t.stopped
		t.stopped = true
		return was
	}
}

// advance moves the clock on, firing each timer due on the way, in order.
func (c *fakeClock) advance(d time.Duration) {
	settle()
	c.mu.Lock()
	until := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var due *fakeTimer
		for _, t := range c.timers {
			if !t.stopped && !t.at.After(until) && (due == nil || t.at.Before(due.at)) {
				due = t
			}
		}
		if due == nil {
			c.now = until
			c.mu.Unlock()
			settle()
			return
		}
		due.stopped = true
		c.now = due.at
		c.mu.Unlock()
		due.f()
		settle()
	}
}

// settle lets what was just started get going.
func settle() { time.Sleep(15 * time.Millisecond) }

// requests answer only when the test says, and stop when their context ends.
type requests struct {
	mu      sync.Mutex
	started []*request
}

type request struct {
	name   string
	ctx    context.Context
	answer chan string
}

func (r *requests) task(name string) func(context.Context) (any, error) {
	return func(ctx context.Context) (any, error) {
		req := &request{name: name, ctx: ctx, answer: make(chan string, 1)}
		r.mu.Lock()
		r.started = append(r.started, req)
		r.mu.Unlock()
		select {
		case v := <-req.answer:
			return v, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (r *requests) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, s := range r.started {
		out = append(out, s.name)
	}
	return out
}

func (r *requests) answer(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.started) - 1; i >= 0; i-- {
		if r.started[i].name == name {
			r.started[i].answer <- name + " answered"
			return
		}
	}
	panic(name + " never started")
}

func (r *requests) at(i int) *request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started[i]
}

type running struct {
	value any
	err   error
	done  chan struct{}
}

func (g *Gate) start(ctx context.Context, lane Lane, task func(context.Context) (any, error)) *running {
	r := &running{done: make(chan struct{})}
	go func() {
		r.value, r.err = g.Run(ctx, lane, task)
		close(r.done)
	}()
	settle()
	return r
}

func (r *running) wait(t *testing.T) (any, error) {
	t.Helper()
	select {
	case <-r.done:
		return r.value, r.err
	case <-time.After(2 * time.Second):
		t.Fatal("never settled")
		return nil, nil
	}
}

func (r *running) settled() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

func setUp(atOnce int) (*fakeClock, *Gate, *requests) {
	c := newFakeClock()
	return c, NewGate(atOnce, c), &requests{}
}

func want(t *testing.T, r *requests, names ...string) {
	t.Helper()
	if got := r.names(); !slices.Equal(got, names) {
		t.Fatalf("sent %q, want %q", got, names)
	}
}

func TestOneRequestAtATimeAQuarterSecondApart(t *testing.T) {
	c, g, r := setUp(1)
	ctx := context.Background()
	first := g.start(ctx, LaneList, r.task("a"))
	g.start(ctx, LaneList, r.task("b"))
	want(t, r, "a")
	r.answer("a")
	if v, err := first.wait(t); v != "a answered" || err != nil {
		t.Fatalf("%v %v", v, err)
	}
	c.advance(Gap - time.Millisecond)
	want(t, r, "a")
	c.advance(time.Millisecond)
	want(t, r, "a", "b")
}

func TestAsManyAtOnceAsToldStillAQuarterSecondApart(t *testing.T) {
	c, g, r := setUp(2)
	for _, name := range []string{"a", "b", "c"} {
		g.start(context.Background(), LaneList, r.task(name))
	}
	want(t, r, "a")
	c.advance(Gap)
	want(t, r, "a", "b")
	c.advance(Gap)
	// Two are out: the third waits for one of them.
	want(t, r, "a", "b")
	r.answer("a")
	c.advance(0)
	want(t, r, "a", "b", "c")
}

func TestAChannelFirstThenAPageAndTheWholeGuideLast(t *testing.T) {
	c, g, r := setUp(1)
	ctx := context.Background()
	g.start(ctx, LaneList, r.task("page 1"))
	g.start(ctx, LaneGuide, r.task("guide"))
	g.start(ctx, LaneList, r.task("page 2"))
	g.start(ctx, LanePlay, r.task("channel"))
	r.answer("page 1")
	c.advance(Gap)
	want(t, r, "page 1", "channel")
	r.answer("channel")
	c.advance(Gap)
	want(t, r, "page 1", "channel", "page 2")
	r.answer("page 2")
	c.advance(Quiet - 2*Gap - time.Millisecond)
	want(t, r, "page 1", "channel", "page 2")
	c.advance(Gap + time.Millisecond)
	want(t, r, "page 1", "channel", "page 2", "guide")
}

func TestAGuideStopsForAChannelAndStartsAgainItsWaiterNoneTheWiser(t *testing.T) {
	c, g, r := setUp(1)
	ctx := context.Background()
	guide := g.start(ctx, LaneGuide, r.task("guide"))
	channel := g.start(ctx, LanePlay, r.task("channel"))
	if r.at(0).ctx.Err() == nil {
		t.Fatal("the guide download went on")
	}
	c.advance(Gap)
	want(t, r, "guide", "channel")
	r.answer("channel")
	if v, _ := channel.wait(t); v != "channel answered" {
		t.Fatalf("channel: %v", v)
	}
	if guide.settled() {
		t.Fatal("the guide's waiter heard of it")
	}
	c.advance(Quiet)
	want(t, r, "guide", "channel", "guide")
	r.answer("guide")
	if v, err := guide.wait(t); v != "guide answered" || err != nil {
		t.Fatalf("guide: %v %v", v, err)
	}
}

func TestACallerGivingUpWhileWaitingTakesNoSlot(t *testing.T) {
	c, g, r := setUp(1)
	g.start(context.Background(), LaneList, r.task("a"))
	ctx, leave := context.WithCancel(context.Background())
	left := g.start(ctx, LaneList, r.task("b"))
	g.start(context.Background(), LaneList, r.task("c"))
	leave()
	if _, err := left.wait(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("left with %v", err)
	}
	r.answer("a")
	c.advance(Gap)
	want(t, r, "a", "c")
}

func TestACallerGivingUpWhileRunningStopsItsRequestAndFreesItsSlot(t *testing.T) {
	c, g, r := setUp(1)
	ctx, leave := context.WithCancel(context.Background())
	left := g.start(ctx, LaneList, r.task("a"))
	g.start(context.Background(), LaneList, r.task("b"))
	leave()
	if _, err := left.wait(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("left with %v", err)
	}
	if r.at(0).ctx.Err() == nil {
		t.Fatal("its request went on")
	}
	c.advance(Gap)
	want(t, r, "a", "b")
}

func TestAFailureIsPassedOnAsItIs(t *testing.T) {
	_, g, _ := setUp(1)
	broke := errors.New("portal broke")
	if _, err := g.Run(context.Background(), LaneList, func(context.Context) (any, error) { return nil, broke }); !errors.Is(err, broke) {
		t.Fatalf("%v", err)
	}
}
