package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// What each user keeps for themselves, which every device they sign in on
// reads back: their preferences, the channels they follow, their lists and
// their ★. One row per user and record key — a tombstone where it was
// deleted, so every device learns of it — and a `seq` each write takes in
// the order it was stored. A user reads their own; only Foyer's routes write.
//
// The seq lives in a table of its own, `_foyer_seq`, never on a record: a
// record anything else saves — the household's, renamed on the dashboard —
// would write back the value it read, and a seq that ever went back would
// make a device miss a write.
func init() {
	m.Register(func(app core.App) error {
		if err := namesFree(app, "preferences", "subscriptions", "playlists", "favorite_channels"); err != nil {
			return err
		}
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		own := types.Pointer("user = @request.auth.id")
		state := func(name string, fields ...core.Field) *core.Collection {
			c := core.NewBaseCollection(name)
			c.ListRule, c.ViewRule = own, own
			c.Fields.Add(&core.RelationField{Name: "user", CollectionId: users.Id, MaxSelect: 1, Required: true, CascadeDelete: true})
			// The record's key, as the app keys it: `<user>/<name>` for a preference, a generated id for the rest.
			c.Fields.Add(&core.TextField{Name: "key", Required: true, Max: 300})
			c.Fields.Add(fields...)
			c.Fields.Add(
				&core.BoolField{Name: "deleted"},
				&core.NumberField{Name: "seq", OnlyInt: true},
				&core.AutodateField{Name: "created", OnCreate: true},
				&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
			)
			c.AddIndex("idx_"+name+"_user_key", true, "`user`, `key`", "")
			c.AddIndex("idx_"+name+"_user_seq", false, "`user`, `seq`", "")
			return c
		}
		for _, c := range []*core.Collection{
			state("preferences",
				&core.TextField{Name: "name", Max: 128},
				&core.JSONField{Name: "value", MaxSize: 256 << 10},
			),
			state("subscriptions",
				&core.TextField{Name: "connection", Max: 128},
				&core.TextField{Name: "external_id", Max: 128},
				&core.TextField{Name: "title", Max: 200},
				&core.TextField{Name: "added_at", Max: 200},
			),
			state("playlists",
				&core.TextField{Name: "title", Max: 200},
				&core.TextField{Name: "description", Max: 200},
				&core.JSONField{Name: "items", MaxSize: 512 << 10},
				&core.JSONField{Name: "source", MaxSize: 4 << 10},
				&core.TextField{Name: "created_at", Max: 200},
				&core.TextField{Name: "updated_at", Max: 200},
			),
			state("favorite_channels",
				&core.TextField{Name: "connection", Max: 128},
				&core.TextField{Name: "external_id", Max: 128},
				&core.TextField{Name: "name", Max: 200},
				&core.NumberField{Name: "number", OnlyInt: true},
				&core.TextField{Name: "logo", Max: 2048},
				&core.TextField{Name: "added_at", Max: 200},
			),
		} {
			if err := app.Save(c); err != nil {
				return err
			}
		}
		// `state`: the last seq a write took. `pruned`: the last a tombstone let go of had.
		if _, err := app.DB().NewQuery("CREATE TABLE IF NOT EXISTS `_foyer_seq` (`name` TEXT PRIMARY KEY NOT NULL, `value` INTEGER NOT NULL DEFAULT 0)").Execute(); err != nil {
			return err
		}
		_, err = app.DB().NewQuery("INSERT OR IGNORE INTO `_foyer_seq` (`name`, `value`) VALUES ('state', 0), ('pruned', 0)").Execute()
		return err
	}, nil)
}
