package library

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/titles"
)

// sortTitleKey names the field that marks a collection as a library's titles
// at template 2 or later: what the keys are kept on.
const sortTitleKey = "sort_title_key"

// bindKeys keeps a title's keys with it on every save, whoever saves it — a
// scan, its artwork, a probe, a correction: the catalogue sorts and searches
// by them, and a title saved without them would sort as nothing.
func (e *Engine) bindKeys() {
	keep := func(ev *core.RecordEvent) error {
		applyKeys(ev.Record)
		return ev.Next()
	}
	e.app.OnRecordCreate().BindFunc(keep)
	e.app.OnRecordUpdate().BindFunc(keep)
}

// applyKeys sets a film's or a series' keys from what it holds. Anything
// else is left alone.
func applyKeys(r *core.Record) {
	fields := r.Collection().Fields
	if fields.GetByName(sortTitleKey) == nil {
		return
	}
	title, sortTitle := r.GetString("title"), r.GetString("sort_title")
	text := sortTitle
	if text == "" {
		// The catalogue never sends an empty sort title, so the app falls back to the title too.
		text = title
	}
	r.Set(sortTitleKey, titles.SortKey(text))
	date := r.GetString("release_date")
	if fields.GetByName("first_air_date") != nil {
		date = r.GetString("first_air_date")
	}
	r.Set("sort_release", titles.ReleaseKey(date, r.GetInt("year")))
	// TMDB says 0 of what nobody rated: none, as the catalogue answers it.
	var rating *float64
	if v := r.GetFloat("rating"); v > 0 {
		rating = &v
	}
	r.Set("sort_rating", titles.RatingKey(rating))
	var search []string
	for _, s := range []string{title, r.GetString("original_title"), sortTitle} {
		if s != "" {
			search = append(search, s)
		}
	}
	if year := r.GetInt("year"); year > 0 {
		search = append(search, strconv.Itoa(year))
	}
	r.Set("search_text", titles.Fold(strings.Join(search, " ")))
	var genres []string
	_ = r.UnmarshalJSONField("genres", &genres)
	keys := make([]string, 0, len(genres))
	for _, g := range genres {
		if k := titles.GenreKey(g); k != "" {
			keys = append(keys, k)
		}
	}
	r.Set("genre_keys", keys)
}

// backfillChunk is how many titles one write holds: SQLite has one writer,
// and the scans and the probes wait for it.
const backfillChunk = 500

// Backfill gives every title saved before its keys existed its keys, a chunk
// at a time under its library's lock — adding a field fills nothing. A
// title's keys are never empty once kept, so it is run at every start, and
// does nothing once done.
func (e *Engine) Backfill(ctx context.Context) error {
	libs, err := e.app.FindAllRecords(LibrariesCollection)
	if err != nil {
		return err
	}
	for _, lib := range libs {
		name := lib.GetString("name")
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			done, err := e.backfillChunk(lib.Id, name)
			if err != nil {
				return fmt.Errorf("the library %s: %w", name, err)
			}
			if done {
				break
			}
		}
	}
	return nil
}

func (e *Engine) backfillChunk(libID, collection string) (bool, error) {
	lock := e.writes(libID)
	lock.Lock()
	defer lock.Unlock()
	c, err := e.app.FindCollectionByNameOrId(collection)
	if err != nil || c.Fields.GetByName(sortTitleKey) == nil {
		return true, nil
	}
	records, err := e.app.FindRecordsByFilter(collection, sortTitleKey+" = ''", "", backfillChunk, 0)
	if err != nil {
		return false, err
	}
	if len(records) == 0 {
		return true, nil
	}
	err = e.app.RunInTransaction(func(tx core.App) error {
		for _, r := range records {
			// The hook keeps the keys; a title with an empty title keys to nothing and would be asked for again.
			if r.GetString("title") == "" {
				continue
			}
			if err := tx.Save(r); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return len(records) < backfillChunk, nil
}
