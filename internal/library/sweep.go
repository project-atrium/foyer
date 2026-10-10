package library

import (
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// sweep lets go of files Zurg has not listed for the grace period, and of
// titles left with nothing: a film with no file, an episode with none, a
// series with no episode and nothing that came with it. Watch status stays,
// keyed by what was watched, for when the title comes back.
func (e *Engine) sweep(lib *core.Record, now time.Time, run *run) error {
	cutoff := now.Add(-grace)
	params := dbx.Params{"library": lib.Id, "cutoff": cutoff}
	gone, err := e.app.FindRecordsByFilter(FilesCollection, "library = {:library} && missing_since != '' && missing_since < {:cutoff}", "", 0, 0, params)
	if err != nil {
		return err
	}
	torrents, err := e.app.FindRecordsByFilter(TorrentsCollection, "library = {:library} && missing_since != '' && missing_since < {:cutoff}", "", 0, 0, params)
	if err != nil {
		return err
	}
	if len(gone) == 0 && len(torrents) == 0 {
		return nil
	}
	lock := e.writes(lib.Id)
	lock.Lock()
	defer lock.Unlock()
	err = e.app.RunInTransaction(func(tx core.App) error {
		affected := map[string]map[string]bool{}
		for _, f := range gone {
			if c, id := f.GetString("item_collection"), f.GetString("item_id"); id != "" {
				if affected[c] == nil {
					affected[c] = map[string]bool{}
				}
				affected[c][id] = true
			}
			if err := tx.Delete(f); err != nil {
				return err
			}
		}
		for _, t := range torrents {
			if err := tx.Delete(t); err != nil {
				return err
			}
		}
		return tidy(tx, lib, affected)
	})
	if err == nil {
		run.update(func(p *Progress) { p.Removed += len(gone) })
	}
	return err
}

// tidy refreshes titles whose files changed, and removes those left with
// nothing.
func tidy(tx core.App, lib *core.Record, affected map[string]map[string]bool) error {
	name := lib.GetString("name")
	shows := map[string]bool{}
	for id := range affected[Episodes(name)] {
		ep, err := tx.FindRecordById(Episodes(name), id)
		if err != nil {
			continue
		}
		shows[ep.GetString("show")] = true
		if err := refresh(tx, ep); err != nil {
			return err
		}
		if ep.GetInt("version_count") == 0 {
			if err := tx.Delete(ep); err != nil {
				return err
			}
			continue
		}
		if err := tx.Save(ep); err != nil {
			return err
		}
	}
	for id := range affected[name] {
		if lib.GetString("kind") == Shows {
			shows[id] = true
			continue
		}
		item, err := tx.FindRecordById(name, id)
		if err != nil {
			continue
		}
		if err := refresh(tx, item); err != nil {
			return err
		}
		if empty, err := hasNothing(tx, item); err != nil {
			return err
		} else if empty {
			if err := tx.Delete(item); err != nil {
				return err
			}
			continue
		}
		if err := tx.Save(item); err != nil {
			return err
		}
	}
	for id := range shows {
		show, err := tx.FindRecordById(name, id)
		if err != nil {
			continue
		}
		if err := refreshShow(tx, show); err != nil {
			return err
		}
		if empty, err := hasNothing(tx, show); err != nil {
			return err
		} else if empty && show.GetInt("episodes_owned") == 0 {
			if err := tx.Delete(show); err != nil {
				return err
			}
			continue
		}
		if err := tx.Save(show); err != nil {
			return err
		}
	}
	return nil
}

// hasNothing says a title has no file of its own left.
func hasNothing(tx core.App, item *core.Record) (bool, error) {
	files, err := filesOf(tx, item.Collection().Name, item.Id)
	return len(files) == 0, err
}
