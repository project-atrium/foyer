package library

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"foyer/internal/adapters/tmdb"
)

const (
	sure      = "sure"
	uncertain = "uncertain"
	none      = "none"
	manual    = "manual"
	// retryNone is how long TMDB having nothing for a title is believed.
	retryNone = 7 * 24 * time.Hour
	// askAbout is how many of several titles of one name are looked at.
	askAbout = 5
)

// decision is what a title is: a TMDB id, and how sure.
type decision struct {
	id     int
	state  string
	reason string
}

// matchState is a title's match_state for a decision.
func (d decision) matchState() string {
	switch d.state {
	case sure:
		return "matched"
	case uncertain:
		return "uncertain"
	case manual:
		if d.id == 0 {
			return "unmatched"
		}
		return "manual"
	}
	return "unmatched"
}

// errTMDBDown is TMDB failing for now: the title waits and is asked again.
var errTMDBDown = errors.New("TMDB did not answer")

// identify is what a title is: what was decided before — a superuser's word
// above all — else TMDB's answer, which is remembered. With TMDB off or its
// key refused, a title is its name's until TMDB can be asked.
func (e *Engine) identify(ctx context.Context, lib *core.Record, c *candidate, client *tmdb.Client) (decision, error) {
	if record, err := e.app.FindFirstRecordByFilter(IdentitiesCollection, "library = {:library} && key = {:key}",
		dbx.Params{"library": lib.Id, "key": c.key}); err == nil {
		state := record.GetString("state")
		checked := record.GetDateTime("checked_at").Time()
		if state != none || time.Since(checked) < retryNone || client == nil {
			return decision{id: record.GetInt("tmdb_id"), state: state, reason: record.GetString("reason")}, nil
		}
	}
	if client == nil {
		return decision{state: none, reason: "TMDB is off"}, nil
	}
	d, candidates, err := e.ask(ctx, c, client)
	if errors.Is(err, tmdb.ErrRefused) || errors.Is(err, tmdb.ErrNoKey) {
		return decision{state: none, reason: "TMDB refused the key"}, nil
	}
	if err != nil {
		return decision{}, errTMDBDown
	}
	return d, e.remember(lib, c, d, candidates)
}

// remember keeps a decision for a title's key, so it outlives the records it
// made and is never asked again for nothing.
func (e *Engine) remember(lib *core.Record, c *candidate, d decision, candidates []candidateInfo) error {
	record, err := e.app.FindFirstRecordByFilter(IdentitiesCollection, "library = {:library} && key = {:key}",
		dbx.Params{"library": lib.Id, "key": c.key})
	if err != nil {
		collection, err := e.app.FindCollectionByNameOrId(IdentitiesCollection)
		if err != nil {
			return err
		}
		record = core.NewRecord(collection)
		record.Set("library", lib.Id)
		record.Set("key", c.key)
	}
	record.Set("kind", string(c.kind))
	record.Set("tmdb_id", d.id)
	record.Set("state", d.state)
	record.Set("reason", d.reason)
	if candidates == nil {
		candidates = []candidateInfo{}
	}
	record.Set("candidates", candidates)
	record.Set("checked_at", types.NowDateTime())
	return e.app.Save(record)
}

// candidateInfo is what one of several titles of one name is, as far as
// telling them apart goes.
type candidateInfo struct {
	ID       int         `json:"id"`
	Name     string      `json:"name"`
	Year     int         `json:"year,omitempty"`
	Episodes int         `json:"episodes,omitempty"`
	Seasons  map[int]int `json:"seasons,omitempty"`
	Anime    bool        `json:"anime"`
	// Continuing is a series still airing: a season beyond TMDB's may be
	// coming.
	Continuing bool `json:"continuing,omitempty"`
}

// ask asks TMDB: Loge's matcher first; where several titles share the name
// and no year tells them apart, what the files say — how far the episodes
// run, whether it is anime — and else TMDB's first, flagged uncertain.
func (e *Engine) ask(ctx context.Context, c *candidate, client *tmdb.Client) (decision, []candidateInfo, error) {
	queries := []tmdb.Query{{Kind: c.kind, Title: c.title, Year: c.year}}
	if c.alternate != "" {
		queries = append(queries, tmdb.Query{Kind: c.kind, Title: c.alternate, Year: c.year})
	}
	for _, q := range queries {
		found, err := client.Find(ctx, q)
		if err != nil {
			return decision{}, nil, err
		}
		if found.ID != 0 {
			return decision{id: found.ID, state: sure, reason: "TMDB"}, nil, nil
		}
		if len(found.Named) == 0 {
			continue
		}
		infos, err := e.describe(ctx, c.kind, found.Named, client)
		if err != nil {
			return decision{}, nil, err
		}
		return decide(c.kind, infos, c.evidence), infos, nil
	}
	return decision{state: none, reason: "TMDB has nothing by this name"}, nil, nil
}

func (e *Engine) describe(ctx context.Context, kind tmdb.Kind, named []tmdb.Found, client *tmdb.Client) ([]candidateInfo, error) {
	var infos []candidateInfo
	for _, f := range named[:min(len(named), askAbout)] {
		info := candidateInfo{ID: f.ID, Year: f.Year}
		if len(f.Names) > 0 {
			info.Name = f.Names[0]
		}
		if kind == tmdb.Show {
			show, err := client.Show(ctx, f.ID, "")
			if errors.Is(err, tmdb.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			info.Episodes, info.Anime = show.EpisodeCount, show.IsAnime()
			info.Seasons = map[int]int{}
			for _, s := range show.Seasons {
				info.Seasons[s.Number] = s.EpisodeCount
			}
			info.Continuing = show.Status == "Returning Series" || show.Status == "In Production" || show.Status == "Planned"
		} else {
			movie, err := client.Movie(ctx, f.ID, "")
			if errors.Is(err, tmdb.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			animated := false
			for _, g := range movie.Genres {
				animated = animated || g.ID == 16
			}
			info.Anime = animated && movie.OriginalLanguage == "ja"
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// decide picks among several titles of one name by what the files say. Sure
// where the episodes leave one series able to hold them, or a strong anime
// mark leaves one anime; else TMDB's first, uncertain — hidden from users
// until a superuser says.
func decide(kind tmdb.Kind, infos []candidateInfo, ev evidence) decision {
	if len(infos) == 0 {
		return decision{state: none, reason: "TMDB has nothing by this name"}
	}
	left := infos
	if kind == tmdb.Show && (ev.maxAbsolute > 0 || len(ev.perSeason) > 0) {
		var fit []candidateInfo
		for _, info := range infos {
			if holds(info, ev) {
				fit = append(fit, info)
			}
		}
		if len(fit) == 1 {
			return decision{id: fit[0].ID, state: sure, reason: "episodes"}
		}
		if len(fit) > 1 {
			left = fit
		}
	}
	if ev.anime > 0 {
		var anime []candidateInfo
		for _, info := range left {
			if info.Anime {
				anime = append(anime, info)
			}
		}
		if len(anime) == 1 {
			if ev.anime >= 2 {
				return decision{id: anime[0].ID, state: sure, reason: "anime"}
			}
			return decision{id: anime[0].ID, state: uncertain, reason: "anime"}
		}
	}
	return decision{id: left[0].ID, state: uncertain, reason: "best guess: " + strings.ToLower(string(kind)) + "s of one name"}
}

// holds says whether a series has room for the episodes a library holds of
// it: its count, give or take what TMDB has yet to add.
func holds(info candidateInfo, ev evidence) bool {
	slack := max(2, info.Episodes/50)
	if ev.maxAbsolute > 0 && ev.maxAbsolute > info.Episodes+slack {
		return false
	}
	for season, episode := range ev.perSeason {
		count, ok := info.Seasons[season]
		if !ok {
			// The season after TMDB's last, of a series still airing.
			last := 0
			for s := range info.Seasons {
				last = max(last, s)
			}
			if !(info.Continuing && season == last+1) {
				return false
			}
			continue
		}
		if episode > count+slack && episode > info.Episodes+slack {
			return false
		}
	}
	return true
}
