package iptv

import (
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// gone counts the viewers Lines told of as gone.
type gone struct{ n atomic.Int32 }

func (g *gone) lines(firstOpen time.Duration) *Lines {
	ls := NewLines(nil)
	ls.FirstOpen = firstOpen
	ls.Left = func(string) { g.n.Add(1) }
	return ls
}

func viewer(n int) Viewer {
	id := strconv.Itoa(n)
	return Viewer{Session: "s" + id, User: "u" + id, Installation: "d" + id}
}

func TestAViewerThatNeverOpensItsStreamIsToldOfAsGone(t *testing.T) {
	var g gone
	ls := g.lines(10 * time.Millisecond)
	if _, _, err := ls.Claim("c1", "ch1", "One", "cmd", viewer(1), 1, false, "", false); err != nil {
		t.Fatal(err)
	}
	for wait := time.Now().Add(5 * time.Second); g.n.Load() == 0 && time.Now().Before(wait); time.Sleep(5 * time.Millisecond) {
	}
	if got := g.n.Load(); got != 1 {
		t.Fatalf("told of %d viewers as gone", got)
	}
}

func TestAViewerWaitingAtShutdownIsNeverToldOfAsGone(t *testing.T) {
	var g gone
	ls := g.lines(20 * time.Millisecond)
	if _, _, err := ls.Claim("c1", "ch1", "One", "cmd", viewer(1), 2, false, "", false); err != nil {
		t.Fatal(err)
	}
	// One whose stream opened and closed waits StayAfter instead.
	ls.StayAfter = 20 * time.Millisecond
	if _, _, err := ls.Claim("c1", "ch2", "Two", "cmd", viewer(2), 2, false, "", false); err != nil {
		t.Fatal(err)
	}
	_, done, err := ls.Attach("s2")
	if err != nil {
		t.Fatal(err)
	}
	done()
	ls.Shutdown()
	if len(ls.InUse("c1")) != 0 {
		t.Error("a line stayed open")
	}
	if _, _, err := ls.Attach("s1"); !errors.Is(err, ErrNotWatching) {
		t.Errorf("a stream opened on a line shut: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := g.n.Load(); got != 0 {
		t.Fatalf("told of %d viewers as gone after Shutdown", got)
	}
}

func TestATimerThatFiredAsLinesShutDoesNothing(t *testing.T) {
	var g gone
	ls := g.lines(time.Millisecond)
	if _, _, err := ls.Claim("c1", "ch1", "One", "cmd", viewer(1), 1, false, "", false); err != nil {
		t.Fatal(err)
	}
	// The timer fires while the lock is held, and waits for it; Lines shut
	// before it gets it.
	ls.mu.Lock()
	time.Sleep(50 * time.Millisecond)
	ls.shutLocked()
	ls.mu.Unlock()
	ls.leaving.Wait()
	time.Sleep(50 * time.Millisecond)
	if got := g.n.Load(); got != 0 {
		t.Fatalf("told of %d viewers as gone after Shutdown", got)
	}
}

func TestNoViewerIsToldOfAsGoneOnceShutdownReturns(t *testing.T) {
	for round := range 200 {
		var after, running atomic.Int32
		var shut atomic.Bool
		ls := NewLines(nil)
		ls.FirstOpen = time.Duration(round%3) * time.Millisecond
		ls.Left = func(string) {
			if shut.Load() {
				after.Add(1)
			}
			running.Add(1)
			// Telling of one takes a while: Shutdown waits for it.
			time.Sleep(time.Millisecond)
			if shut.Load() {
				after.Add(1)
			}
			running.Add(-1)
		}
		for n := range 5 {
			if _, _, err := ls.Claim("c1", "ch"+strconv.Itoa(n), "Channel", "cmd", viewer(n), 5, false, "", false); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(time.Duration(round%4) * 500 * time.Microsecond)
		ls.Shutdown()
		if running.Load() != 0 {
			t.Fatalf("round %d: Shutdown returned while a viewer was being told of as gone", round)
		}
		shut.Store(true)
		time.Sleep(5 * time.Millisecond)
		if after.Load() != 0 || running.Load() != 0 {
			t.Fatalf("round %d: a viewer was told of as gone after Shutdown returned", round)
		}
	}
}
