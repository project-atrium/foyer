package library

import (
	"context"
	"errors"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// CorrectionsCollection holds a superuser's word on single files of a
// series, by library and path: kept apart from the files' own records, so
// it holds through every rescan and comes back with a file that vanished.
const CorrectionsCollection = "file_corrections"

// What a file of a series is said to be.
const (
	CorrectEpisode = "episode"
	CorrectExtra   = "extra"
	// CorrectAuto lets go of a correction: the file is read by its name again.
	CorrectAuto = "auto"
)

// FileCorrection is a superuser's word on one file of a series: the episode
// it is — season 0 for a special — or that it is an extra.
type FileCorrection struct {
	Kind    string `json:"kind"`
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
}

// corrections are a library's, by the files' paths.
func corrections(app core.App, library string) map[string]FileCorrection {
	records, err := app.FindRecordsByFilter(CorrectionsCollection, "library = {:library}", "", 0, 0, dbx.Params{"library": library})
	if err != nil {
		return nil
	}
	out := make(map[string]FileCorrection, len(records))
	for _, r := range records {
		out[r.GetString("path")] = FileCorrection{Kind: r.GetString("kind"), Season: r.GetInt("season"), Episode: r.GetInt("episode")}
	}
	return out
}

// corrected is where a correction places a file, or that it is an extra:
// what the scan would read of its name is not asked.
func corrected(fix FileCorrection) (role string, p placement) {
	if fix.Kind == CorrectExtra {
		return "extra", placement{}
	}
	return "version", placement{season: fix.Season, episode: fix.Episode, by: "manual"}
}

var errNoFile = errors.New("no such file")

// CorrectFile is a superuser's word on one file of a series — the episode
// it is, an extra, or as its name reads — kept by its library and path, and
// the series' files placed again at once, watch status following them.
func (e *Engine) CorrectFile(ctx context.Context, fileID string, c FileCorrection) error {
	f, err := e.app.FindRecordById(FilesCollection, fileID)
	if err != nil {
		return errNoFile
	}
	lib, err := e.app.FindRecordById(LibrariesCollection, f.GetString("library"))
	if err != nil {
		return errNoFile
	}
	if lib.GetString("kind") != Shows {
		return errors.New("only a series' files are corrected one by one: a film is named as a whole")
	}
	switch c.Kind {
	case CorrectEpisode:
		if c.Season < 0 || c.Episode < 1 {
			return errors.New("an episode has a season — 0 for a special — and a number from 1")
		}
	case CorrectExtra, CorrectAuto:
		c.Season, c.Episode = 0, 0
	default:
		return errors.New("a file is an episode, an extra, or as its name reads")
	}
	name := lib.GetString("name")
	show := ""
	switch f.GetString("item_collection") {
	case name:
		show = f.GetString("item_id")
	case Episodes(name):
		if ep, err := e.app.FindRecordById(Episodes(name), f.GetString("item_id")); err == nil {
			show = ep.GetString("show")
		}
	}
	if show == "" {
		return errors.New("this file is in no series yet: the next scan places it")
	}
	lock := e.writes(lib.Id)
	lock.Lock()
	err = e.app.RunInTransaction(func(tx core.App) error {
		r, err := tx.FindFirstRecordByFilter(CorrectionsCollection, "library = {:library} && path = {:path}",
			dbx.Params{"library": lib.Id, "path": f.GetString("path")})
		if c.Kind == CorrectAuto {
			if err == nil {
				return tx.Delete(r)
			}
			return nil
		}
		if err != nil {
			col, err := tx.FindCollectionByNameOrId(CorrectionsCollection)
			if err != nil {
				return err
			}
			r = core.NewRecord(col)
			r.Set("library", lib.Id)
			r.Set("path", f.GetString("path"))
		}
		r.Set("kind", c.Kind)
		r.Set("season", c.Season)
		r.Set("episode", c.Episode)
		return tx.Save(r)
	})
	lock.Unlock()
	if err != nil {
		return err
	}
	return e.replace(ctx, name, show, lib, nil)
}
