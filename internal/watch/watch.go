// Package watch keeps what each user watched, as Foyer's own: Foyer is the
// household's watch status, the way a media server keeps its own. Loge reports
// progress to it as it does to Jellyfin — started, progress, stopped, played —
// and reads it back.
package watch

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

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
	// What a video of the site's is, where it is said: no record of Foyer's
	// holds it, so its state keeps it for Continue watching.
	Title, Subtitle, SubtitleID, Picture string
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
		for field, value := range map[string]string{"title": item.Title, "subtitle": item.Subtitle, "subtitle_id": item.SubtitleID, "picture": item.Picture} {
			if value != "" {
				r.Set(field, value)
			}
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

// MoveExtras gives what was watched of a title's extras to its new identity
// with it: an extra is known by its title's identity (identity.Extra).
func MoveExtras(app core.App, from, to string) error {
	if from == "" || to == "" || from == to {
		return nil
	}
	prefix := from + "/extra:"
	var identities []string
	// Not LIKE: SQLite's takes no escape unless told, and an identity may hold an underscore.
	err := app.DB().Select("identity").Distinct(true).From(Collection).
		Where(dbx.NewExp("substr([[identity]], 1, {:n}) = {:prefix}", dbx.Params{"n": utf8.RuneCountInString(prefix), "prefix": prefix})).
		Column(&identities)
	if err != nil {
		return err
	}
	for _, id := range identities {
		if err := Move(app, id, to+"/extra:"+strings.TrimPrefix(id, prefix)); err != nil {
			return err
		}
	}
	return nil
}

// Agreed is what Foyer and a tracker last agreed of one thing: how often the
// tracker had it played, how often Foyer had counted, and whether Foyer had
// it watched. A change on either side is told from it, so neither side's is
// ever sent back.
type Agreed struct {
	Plays   int  `json:"plays"`
	Foyer   int  `json:"foyer"`
	Watched bool `json:"watched"`
}

// Tracked is a user's state for one thing, as a tracker's sync reads it.
type Tracked struct {
	Identity string
	State    State
	// Agreed is nil where Foyer and the tracker never agreed of it.
	Agreed  *Agreed
	Updated time.Time
}

// TrackedBy is every state a user keeps, each with what it last agreed with
// provider, by identity.
func TrackedBy(app core.App, user, provider string) (map[string]Tracked, error) {
	records, err := app.FindRecordsByFilter(Collection, "user = {:user}", "", 0, 0, dbx.Params{"user": user})
	if err != nil {
		return nil, err
	}
	out := make(map[string]Tracked, len(records))
	for _, r := range records {
		t := Tracked{Identity: r.GetString("identity"), State: read(r), Updated: r.GetDateTime("updated").Time()}
		var remote map[string]Agreed
		if err := r.UnmarshalJSONField("remote", &remote); err == nil {
			if agreed, ok := remote[provider]; ok {
				t.Agreed = &agreed
			}
		}
		out[t.Identity] = t
	}
	return out, nil
}

// Settled is what a tracker's sync settles of one thing: Foyer's state as
// it is to be — nil where it stays as it is — and what both sides agree now.
type Settled struct {
	Identity string
	State    *State
	Agreed   Agreed
}

// settleChunk is how many states one transaction writes: SQLite has one
// writer, and a player's report waits behind it.
const settleChunk = 500

// Settle writes what a tracker's sync settled for a user, a chunk to a
// transaction: each state as it is to be, made where none was kept — a
// title watched elsewhere, before its files are here — and what it agreed
// with provider, beside what it agreed with any other.
func Settle(app core.App, user, provider string, settled []Settled) error {
	for start := 0; start < len(settled); start += settleChunk {
		chunk := settled[start:min(start+settleChunk, len(settled))]
		err := app.RunInTransaction(func(tx core.App) error {
			c, err := tx.FindCollectionByNameOrId(Collection)
			if err != nil {
				return err
			}
			for _, s := range chunk {
				r, err := tx.FindFirstRecordByFilter(Collection, "user = {:user} && identity = {:identity}", dbx.Params{"user": user, "identity": s.Identity})
				if err != nil {
					r = core.NewRecord(c)
					r.Set("user", user)
					r.Set("identity", s.Identity)
				}
				if s.State != nil {
					write(r, *s.State)
				}
				remote := map[string]Agreed{}
				_ = r.UnmarshalJSONField("remote", &remote)
				remote[provider] = s.Agreed
				r.Set("remote", remote)
				if err := tx.Save(r); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Forget lets go of what a user's states agreed with provider: connected
// again, the tracker's history is read as if for the first time.
func Forget(app core.App, user, provider string) error {
	records, err := app.FindRecordsByFilter(Collection, "user = {:user}", "", 0, 0, dbx.Params{"user": user})
	if err != nil {
		return err
	}
	for start := 0; start < len(records); start += settleChunk {
		chunk := records[start:min(start+settleChunk, len(records))]
		err := app.RunInTransaction(func(tx core.App) error {
			for _, r := range chunk {
				remote := map[string]Agreed{}
				if err := r.UnmarshalJSONField("remote", &remote); err != nil {
					continue
				}
				if _, ok := remote[provider]; !ok {
					continue
				}
				delete(remote, provider)
				r.Set("remote", remote)
				if err := tx.Save(r); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
