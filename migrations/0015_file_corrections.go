package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// A superuser's word on one file of a series: the episode it is, or that
// it is an extra. Kept by the file's library and path, apart from the
// file's own record — which goes three days after the share stops listing
// it — so it holds through every rescan, and comes back with the file.
// Superusers only.
func init() {
	m.Register(func(app core.App) error {
		if err := namesFree(app, "file_corrections"); err != nil {
			return err
		}
		libraries, err := app.FindCollectionByNameOrId("libraries")
		if err != nil {
			return err
		}
		corrections := core.NewBaseCollection("file_corrections")
		corrections.Fields.Add(
			&core.RelationField{Name: "library", CollectionId: libraries.Id, MaxSelect: 1, Required: true, CascadeDelete: true},
			&core.TextField{Name: "path", Required: true, Max: 4096},
			// episode, or extra.
			&core.TextField{Name: "kind", Required: true, Max: 20},
			&core.NumberField{Name: "season", OnlyInt: true},
			&core.NumberField{Name: "episode", OnlyInt: true},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		corrections.AddIndex("idx_file_corrections_library_path", true, "`library`, `path`", "")
		return app.Save(corrections)
	}, nil)
}
