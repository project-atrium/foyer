package trakt

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"foyer/internal/watch"
)

// Modes a user's connection runs in.
const (
	// TwoWay is both ways: a play, a watch and an unwatch on either side
	// reach the other.
	TwoWay = "two-way"
	// Send is Foyer's way alone: what is watched here reaches Trakt, and
	// nothing of Trakt's comes back, nor is anything taken out of it.
	Send = "send"
	// Off keeps the connection and syncs nothing.
	Off = "off"
)

// thing is one film or one episode, as Trakt and Foyer both know it: by
// TMDB's ids. A title known only by its name, an extra or a video of
// YouTube's has none, and is never sent.
type thing struct {
	movie   int64
	episode Episode
}

func (t thing) identity() string {
	if t.movie > 0 {
		return "tmdb:movie:" + strconv.FormatInt(t.movie, 10)
	}
	return fmt.Sprintf("tmdb:tv:%d/s%de%d", t.episode.Show, t.episode.Season, t.episode.Number)
}

// thingOf reads an identity Trakt can be told of.
func thingOf(identity string) (thing, bool) {
	if rest, ok := strings.CutPrefix(identity, "tmdb:movie:"); ok {
		id, err := strconv.ParseInt(rest, 10, 64)
		return thing{movie: id}, err == nil && id > 0
	}
	rest, ok := strings.CutPrefix(identity, "tmdb:tv:")
	if !ok {
		return thing{}, false
	}
	show, episode, ok := strings.Cut(rest, "/s")
	if !ok {
		return thing{}, false
	}
	season, number, ok := strings.Cut(episode, "e")
	if !ok {
		return thing{}, false
	}
	id, err1 := strconv.ParseInt(show, 10, 64)
	s, err2 := strconv.Atoi(season)
	n, err3 := strconv.Atoi(number)
	if err1 != nil || err2 != nil || err3 != nil || id <= 0 || s < 0 || n <= 0 {
		return thing{}, false
	}
	return thing{episode: Episode{Show: id, Season: s, Number: n}}, true
}

// outcome is what a sync settles of one thing: Foyer's state as it is to
// be — nil where it stays — plays to add on Trakt, or the thing taken out
// of Trakt's history, and what both sides agree once done.
type outcome struct {
	state  *watch.State
	add    int
	remove bool
	agreed watch.Agreed
}

// reconcile settles one thing between Trakt — played plays times, last at
// last — and Foyer — its state, kept or not — from what they last agreed:
// nil, the first time. Each side's change since is told from that, and goes
// to the other as the mode allows; where both changed, watched wins. Plays
// are counted: a play on either side is one on the other. Nothing is done
// where nothing changed: false.
func reconcile(played int, last time.Time, f watch.State, kept bool, agreed *watch.Agreed, mode string) (outcome, bool) {
	if mode == Off || (!kept && played == 0) {
		return outcome{}, false
	}
	takes := mode == TwoWay
	if agreed == nil {
		// The first time: Trakt's history comes in, and what Trakt lacks goes.
		switch {
		case played > 0 && !f.Watched && takes:
			s := f
			s.Watched, s.PlayCount, s.PositionMs = true, max(f.PlayCount, played), 0
			s.LastPlayedAt = later(f.LastPlayedAt, last)
			return outcome{state: &s, agreed: watch.Agreed{Plays: played, Foyer: s.PlayCount, Watched: true}}, true
		case played == 0 && f.Watched:
			// One play, at when Foyer heard of it: how often before is Foyer's own count.
			return outcome{add: 1, agreed: watch.Agreed{Plays: 1, Foyer: f.PlayCount, Watched: true}}, true
		case played == 0:
			return outcome{}, false
		}
		// Both have it, or Trakt alone where Foyer only sends: as they are.
		return outcome{agreed: watch.Agreed{Plays: played, Foyer: f.PlayCount, Watched: f.Watched}}, true
	}

	gained := max(f.PlayCount-agreed.Foyer, 0) // plays Foyer counted since
	moved := played - agreed.Plays             // Trakt's, since: more, or fewer
	unwatched := agreed.Watched && !f.Watched && gained == 0
	if gained == 0 && moved == 0 && agreed.Watched == f.Watched {
		return outcome{}, false
	}
	s := f
	changed := false
	plays := played
	o := outcome{}
	switch {
	case moved > 0 && takes:
		// Played on Trakt: watched here, as often more.
		s.Watched, s.PlayCount, s.PositionMs = true, f.PlayCount+moved, 0
		s.LastPlayedAt = later(f.LastPlayedAt, last)
		changed = true
	case moved < 0 && played == 0 && gained == 0 && f.Watched && takes:
		// Taken out of Trakt's history, and not played here since: unwatched here too.
		s.Watched, s.PositionMs = false, 0
		changed = true
	}
	switch {
	case gained > 0:
		o.add = gained
		plays += gained
	case unwatched && mode == TwoWay && moved <= 0 && played > 0:
		// Unwatched here, and not played there since: taken out of its history.
		o.remove = true
		plays = 0
	}
	if changed {
		o.state = &s
	}
	o.agreed = watch.Agreed{Plays: plays, Foyer: s.PlayCount, Watched: s.Watched}
	return o, true
}

// later is the later of two times, a zero one never.
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
