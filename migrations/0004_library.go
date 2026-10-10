package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// The library's bookkeeping. A library's own collections — `movies`, or
// `shows` with `shows_episodes` — are not here: Foyer makes them from a
// template, one set per Zurg folder chosen as a library (internal/library).
//
//   - libraries: which Zurg folder is which library, and how its scans went.
//     Users may read them, to know what there is.
//   - media_torrents: each folder Zurg lists under a library's folder — a
//     torrent — and when Foyer first saw it, or saw it last.
//   - media_files: each file of a torrent, what its name says, which title
//     took it, and what ffprobe found. Superusers only: users read a title's
//     versions on the title itself, and a title not yet confirmed must not
//     show through its files.
//   - identities: what TMDB was asked and answered for each title a library
//     read, so a decision outlives the records it made, and a superuser's
//     correction stays corrected.
//
// Written by Foyer's code alone: the guard refuses even a superuser's writes
// from the API.
func init() {
	m.Register(func(app core.App) error {
		users := types.Pointer("@request.auth.collectionName = 'users'")

		libraries := core.NewBaseCollection("libraries")
		libraries.ListRule, libraries.ViewRule = users, users
		libraries.Fields.Add(
			&core.TextField{Name: "name", Required: true, Max: 40, Pattern: `^[a-z][a-z0-9_]*$`},
			&core.SelectField{Name: "kind", Required: true, MaxSelect: 1, Values: []string{"movies", "shows"}},
			&core.TextField{Name: "folder", Required: true, Max: 255},
			&core.BoolField{Name: "enabled"},
			&core.NumberField{Name: "template", OnlyInt: true},
			&core.DateField{Name: "last_scan_at"},
			&core.DateField{Name: "last_full_scan_at"},
			&core.TextField{Name: "scan_error", Max: 2000},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		libraries.AddIndex("idx_libraries_name", true, "`name` COLLATE NOCASE", "")
		libraries.AddIndex("idx_libraries_folder", true, "`folder`", "")
		if err := app.Save(libraries); err != nil {
			return err
		}

		torrents := core.NewBaseCollection("media_torrents")
		torrents.Fields.Add(
			&core.RelationField{Name: "library", CollectionId: libraries.Id, MaxSelect: 1, Required: true, CascadeDelete: true},
			&core.TextField{Name: "name", Required: true, Max: 1024},
			// A file lying in the folder itself, with no torrent folder round it.
			&core.BoolField{Name: "loose"},
			&core.DateField{Name: "modified"},
			&core.DateField{Name: "first_seen"},
			&core.DateField{Name: "missing_since"},
			&core.TextField{Name: "error", Max: 2000},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		torrents.AddIndex("idx_media_torrents_library_name", true, "`library`, `name`", "")
		if err := app.Save(torrents); err != nil {
			return err
		}

		files := core.NewBaseCollection("media_files")
		files.Fields.Add(
			&core.RelationField{Name: "library", CollectionId: libraries.Id, MaxSelect: 1, Required: true, CascadeDelete: true},
			&core.RelationField{Name: "torrent", CollectionId: torrents.Id, MaxSelect: 1, Required: true, CascadeDelete: true},
			// Under the library's folder: "<torrent>/<folders…>/<file>", as Zurg lists it.
			&core.TextField{Name: "path", Required: true, Max: 4096},
			&core.TextField{Name: "name", Max: 1024},
			&core.NumberField{Name: "size", OnlyInt: true},
			&core.DateField{Name: "modified"},
			&core.DateField{Name: "added"},
			&core.SelectField{Name: "added_source", MaxSelect: 1, Values: []string{"folder", "file", "seen"}},
			&core.JSONField{Name: "parsed", MaxSize: 16 << 10},
			&core.TextField{Name: "match_key", Max: 600},
			// version: one of a title's files; extra: a film, an OVA or a
			// featurette that came with it; unmapped: a show's file no episode
			// took yet; pending: waiting for TMDB to answer.
			&core.SelectField{Name: "role", MaxSelect: 1, Values: []string{"version", "extra", "unmapped", "pending"}},
			&core.TextField{Name: "label", Max: 200},
			// Which title took it: library collections come and go, so no
			// relation can point at them.
			&core.TextField{Name: "item_collection", Max: 64},
			&core.TextField{Name: "item_id", Max: 32},
			&core.SelectField{Name: "probe_state", MaxSelect: 1, Values: []string{"pending", "done", "failed"}},
			&core.JSONField{Name: "probe", MaxSize: 64 << 10},
			&core.TextField{Name: "probe_error", Max: 2000},
			&core.NumberField{Name: "probe_attempts", OnlyInt: true},
			&core.DateField{Name: "probe_next_at"},
			&core.DateField{Name: "probed_at"},
			&core.DateField{Name: "missing_since"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		files.AddIndex("idx_media_files_library_path", true, "`library`, `path`", "")
		files.AddIndex("idx_media_files_torrent", false, "`torrent`", "")
		files.AddIndex("idx_media_files_item", false, "`item_collection`, `item_id`", "")
		files.AddIndex("idx_media_files_match_key", false, "`library`, `match_key`", "")
		files.AddIndex("idx_media_files_probe", false, "`probe_state`, `probe_next_at`", "")
		if err := app.Save(files); err != nil {
			return err
		}

		identities := core.NewBaseCollection("identities")
		identities.Fields.Add(
			&core.RelationField{Name: "library", CollectionId: libraries.Id, MaxSelect: 1, Required: true, CascadeDelete: true},
			&core.TextField{Name: "key", Required: true, Max: 600},
			&core.SelectField{Name: "kind", Required: true, MaxSelect: 1, Values: []string{"movie", "show"}},
			&core.NumberField{Name: "tmdb_id", OnlyInt: true},
			&core.SelectField{Name: "state", Required: true, MaxSelect: 1, Values: []string{"sure", "uncertain", "none", "manual"}},
			&core.TextField{Name: "reason", Max: 200},
			&core.JSONField{Name: "candidates", MaxSize: 16 << 10},
			&core.DateField{Name: "checked_at"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		identities.AddIndex("idx_identities_library_key", true, "`library`, `key`", "")
		return app.Save(identities)
	}, nil)
}
