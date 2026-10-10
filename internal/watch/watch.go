// Package watch keeps what each user watched, as Foyer's own: Foyer is the
// household's watch status, the way a media server keeps its own. Loge reports
// progress to it as it does to Jellyfin — started, progress, stopped, played —
// and reads it back.
package watch

import (
	"errors"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// Collection holds one record per user and thing watched.
const Collection = "watch_states"

// Report is what a player says as it plays.
type Report struct {
	// Kind is started, progress, stopped, or end — the player reached the
	// end itself.
	Kind       string `json:"kind"`
	PositionMs int64  `json:"positionMs"`
	DurationMs int64  `json:"durationMs"`
	Paused     bool   `json:"paused"`
	// Session names one playing, so a report sent twice counts once.
	Session string `json:"session"`
}

// State is a user's watch status for one thing.
type State struct {
	Watched          bool
	PositionMs       int64
	DurationMs       int64
	PlayCount        int
	LastPlayedAt     time.Time
	CompletedSession string
}

// Apply is a report's effect, by the rules Loge keeps: a stop at 90 % of the
// length or more, or the end, is watched — and watched holds; a report at 0
// never lowers where it got to, since an engine reports 0 as it closes; a
// finished playing counts once, whatever is sent again.
func Apply(s State, r Report, now time.Time) State {
	s.LastPlayedAt = now
	if r.DurationMs > 0 {
		s.DurationMs = r.DurationMs
	}
	if r.PositionMs > 0 {
		s.PositionMs = r.PositionMs
	}
	done := r.Kind == "end" || r.Kind == "stopped" && s.DurationMs > 0 && r.PositionMs*10 >= s.DurationMs*9
	if done && (r.Session == "" || r.Session != s.CompletedSession) {
		s.Watched = true
		s.PlayCount++
		s.PositionMs = 0
		s.CompletedSession = r.Session
	}
	return s
}

// Mark is a user's word: watched, or not. Unwatched starts again from the
// beginning; how often it was played stays.
func Mark(s State, watched bool, now time.Time) State {
	if watched {
		if !s.Watched {
			s.PlayCount++
		}
		s.Watched = true
	} else {
		s.Watched = false
	}
	s.PositionMs = 0
	s.LastPlayedAt = now
	return s
}

func read(r *core.Record) State {
	return State{
		Watched:          r.GetBool("watched"),
		PositionMs:       r.GetInt64("position_ms"),
		DurationMs:       r.GetInt64("duration_ms"),
		PlayCount:        r.GetInt("play_count"),
		LastPlayedAt:     r.GetDateTime("last_played_at").Time(),
		CompletedSession: r.GetString("completed_session"),
	}
}

func write(r *core.Record, s State) {
	r.Set("watched", s.Watched)
	r.Set("position_ms", s.PositionMs)
	r.Set("duration_ms", s.DurationMs)
	r.Set("play_count", s.PlayCount)
	if !s.LastPlayedAt.IsZero() {
		r.Set("last_played_at", s.LastPlayedAt)
	}
	r.Set("completed_session", s.CompletedSession)
}

// Item is the title a state is about, and how it is known.
type Item struct {
	Collection  string
	ID          string
	Identity    string
	ExternalIDs map[string]string
}

// update changes a user's state for an item, in one transaction, made if it
// was never kept.
func update(app core.App, user string, item Item, change func(State) State) error {
	if item.Identity == "" {
		return errors.New("this title is not known well enough to keep its watch status")
	}
	return app.RunInTransaction(func(tx core.App) error {
		r, err := tx.FindFirstRecordByFilter(Collection, "user = {:user} && identity = {:identity}",
			dbx.Params{"user": user, "identity": item.Identity})
		if err != nil {
			c, err := tx.FindCollectionByNameOrId(Collection)
			if err != nil {
				return err
			}
			r = core.NewRecord(c)
			r.Set("user", user)
			r.Set("identity", item.Identity)
		}
		write(r, change(read(r)))
		r.Set("item_collection", item.Collection)
		r.Set("item_id", item.ID)
		if item.ExternalIDs != nil {
			r.Set("external_ids", item.ExternalIDs)
		}
		return tx.Save(r)
	})
}

// Move gives what was watched under one identity to another — a title a
// superuser named, or TMDB at last — for every user, unless another title
// still goes by the old one. Where a user has both, they are one: watched if
// either was, played as often as both, and where the later playing left off.
func Move(app core.App, from, to string) error {
	if from == "" || to == "" || from == to {
		return nil
	}
	return app.RunInTransaction(func(tx core.App) error {
		old, err := tx.FindAllRecords(Collection, dbx.HashExp{"identity": from})
		if err != nil {
			return err
		}
		for _, r := range old {
			existing, err := tx.FindFirstRecordByFilter(Collection, "user = {:user} && identity = {:identity}",
				dbx.Params{"user": r.GetString("user"), "identity": to})
			if err != nil {
				r.Set("identity", to)
				if err := tx.Save(r); err != nil {
					return err
				}
				continue
			}
			a, b := read(r), read(existing)
			merged := b
			if a.LastPlayedAt.After(b.LastPlayedAt) {
				merged = a
			}
			merged.Watched = a.Watched || b.Watched
			merged.PlayCount = a.PlayCount + b.PlayCount
			write(existing, merged)
			if err := tx.Save(existing); err != nil {
				return err
			}
			if err := tx.Delete(r); err != nil {
				return err
			}
		}
		return nil
	})
}
