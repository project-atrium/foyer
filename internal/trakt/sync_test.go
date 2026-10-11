package trakt

import (
	"testing"
	"time"

	"foyer/internal/watch"
)

func TestAThingIsKnownByTMDBsIdsAlone(t *testing.T) {
	for _, identity := range []string{"tmdb:movie:603", "tmdb:tv:1396/s1e3", "tmdb:tv:37854/s0e12"} {
		th, ok := thingOf(identity)
		if !ok || th.identity() != identity {
			t.Errorf("%s read as %+v", identity, th)
		}
	}
	for _, identity := range []string{"title:movie:the matrix:1999", "tmdb:tv:37854/extra:4f0e9c1d2b3a5867", "youtube:x3TJFoQWCHA", "tmdb:tv:1396", "tmdb:movie:x", "tmdb:tv:1396/s1e0"} {
		if th, ok := thingOf(identity); ok {
			t.Errorf("%s read as %+v", identity, th)
		}
	}
}

// Each side's change since they last agreed goes to the other, as the mode
// allows; where both changed, watched wins; plays are counted.
func TestTraktAndFoyerSettleWhatChanged(t *testing.T) {
	then := time.Date(2026, 9, 1, 20, 0, 0, 0, time.UTC)
	trakt := time.Date(2026, 9, 3, 21, 0, 0, 0, time.UTC)
	watched := func(plays int) watch.State {
		return watch.State{Watched: true, PlayCount: plays, LastPlayedAt: then}
	}
	agreed := func(plays, foyer int, w bool) *watch.Agreed {
		return &watch.Agreed{Plays: plays, Foyer: foyer, Watched: w}
	}
	type want struct {
		ok     bool
		add    int
		remove bool
		state  *watch.State
		agreed watch.Agreed
	}
	stateOf := func(w bool, plays int, at time.Time) *watch.State {
		return &watch.State{Watched: w, PlayCount: plays, LastPlayedAt: at}
	}
	cases := []struct {
		name   string
		played int
		f      watch.State
		kept   bool
		agreed *watch.Agreed
		mode   string
		want   want
	}{
		{"first: Trakt's history comes in", 2, watch.State{}, false, nil, TwoWay, want{ok: true, state: stateOf(true, 2, trakt), agreed: watch.Agreed{Plays: 2, Foyer: 2, Watched: true}}},
		{"first: what Trakt lacks goes, once", 0, watched(3), true, nil, TwoWay, want{ok: true, add: 1, agreed: watch.Agreed{Plays: 1, Foyer: 3, Watched: true}}},
		{"first: both have it, as they are", 4, watched(1), true, nil, TwoWay, want{ok: true, agreed: watch.Agreed{Plays: 4, Foyer: 1, Watched: true}}},
		{"first: sent only, Trakt's stays there", 2, watch.State{}, true, nil, Send, want{ok: true, agreed: watch.Agreed{Plays: 2, Foyer: 0}}},
		{"first: neither", 0, watch.State{PositionMs: 60_000}, true, nil, TwoWay, want{}},
		{"nothing changed", 2, watched(2), true, agreed(2, 2, true), TwoWay, want{}},
		{"played here", 2, watched(3), true, agreed(2, 2, true), TwoWay, want{ok: true, add: 1, agreed: watch.Agreed{Plays: 3, Foyer: 3, Watched: true}}},
		{"played there", 3, watched(2), true, agreed(2, 2, true), TwoWay, want{ok: true, state: stateOf(true, 3, trakt), agreed: watch.Agreed{Plays: 3, Foyer: 3, Watched: true}}},
		{"played on both", 3, watched(3), true, agreed(2, 2, true), TwoWay, want{ok: true, add: 1, state: stateOf(true, 4, trakt), agreed: watch.Agreed{Plays: 4, Foyer: 4, Watched: true}}},
		{"unwatched here", 2, watch.State{PlayCount: 2, LastPlayedAt: then}, true, agreed(2, 2, true), TwoWay, want{ok: true, remove: true, agreed: watch.Agreed{Plays: 0, Foyer: 2}}},
		{"unwatched here, sent only: Trakt keeps it", 2, watch.State{PlayCount: 2, LastPlayedAt: then}, true, agreed(2, 2, true), Send, want{ok: true, agreed: watch.Agreed{Plays: 2, Foyer: 2}}},
		{"taken out there", 0, watched(2), true, agreed(2, 2, true), TwoWay, want{ok: true, state: stateOf(false, 2, then), agreed: watch.Agreed{Plays: 0, Foyer: 2}}},
		{"taken out there, sent only: kept here", 0, watched(2), true, agreed(2, 2, true), Send, want{ok: true, agreed: watch.Agreed{Plays: 0, Foyer: 2, Watched: true}}},
		{"taken out there, played here since: watched wins", 0, watched(3), true, agreed(2, 2, true), TwoWay, want{ok: true, add: 1, agreed: watch.Agreed{Plays: 1, Foyer: 3, Watched: true}}},
		{"unwatched here, played there since: watched wins", 3, watch.State{PlayCount: 2, LastPlayedAt: then}, true, agreed(2, 2, true), TwoWay, want{ok: true, state: stateOf(true, 3, trakt), agreed: watch.Agreed{Plays: 3, Foyer: 3, Watched: true}}},
		{"watched again here after an unwatch", 0, watched(3), true, agreed(0, 2, false), TwoWay, want{ok: true, add: 1, agreed: watch.Agreed{Plays: 1, Foyer: 3, Watched: true}}},
		{"played there, sent only: not taken", 3, watched(2), true, agreed(2, 2, true), Send, want{ok: true, agreed: watch.Agreed{Plays: 3, Foyer: 2, Watched: true}}},
		{"off", 3, watched(5), true, agreed(2, 2, true), Off, want{}},
	}
	for _, c := range cases {
		o, ok := reconcile(c.played, trakt, c.f, c.kept, c.agreed, c.mode)
		if ok != c.want.ok {
			t.Errorf("%s: settled %v", c.name, ok)
			continue
		}
		if !ok {
			continue
		}
		if o.add != c.want.add || o.remove != c.want.remove || o.agreed != c.want.agreed {
			t.Errorf("%s: add %d remove %v agreed %+v", c.name, o.add, o.remove, o.agreed)
		}
		switch {
		case (o.state == nil) != (c.want.state == nil):
			t.Errorf("%s: state %+v", c.name, o.state)
		case o.state != nil && (o.state.Watched != c.want.state.Watched || o.state.PlayCount != c.want.state.PlayCount || !o.state.LastPlayedAt.Equal(c.want.state.LastPlayedAt) || o.state.PositionMs != 0):
			t.Errorf("%s: state %+v, want %+v", c.name, *o.state, *c.want.state)
		}
	}
}
