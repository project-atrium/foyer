package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// What was played, by whom and where, once each playing ended — and which a
// superuser stopped: a stopped playing's addresses stay refused for as long
// as they could have lasted, across a restart, so the history is the
// revocation list too. Superusers only: it is the dashboard's.
func init() {
	m.Register(func(app core.App) error {
		if err := namesFree(app, "play_history"); err != nil {
			return err
		}
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		history := core.NewBaseCollection("play_history")
		history.Fields.Add(
			&core.TextField{Name: "session", Required: true, Max: 32},
			&core.RelationField{Name: "user", CollectionId: users.Id, MaxSelect: 1, CascadeDelete: true},
			&core.TextField{Name: "device_name", Max: 100},
			&core.TextField{Name: "device_platform", Max: 20},
			&core.TextField{Name: "app_version", Max: 40},
			&core.TextField{Name: "kind", Max: 20},
			&core.TextField{Name: "item_collection", Max: 64},
			&core.TextField{Name: "item_id", Max: 32},
			&core.TextField{Name: "title", Max: 500},
			&core.TextField{Name: "subtitle", Max: 500},
			&core.TextField{Name: "file", Max: 32},
			&core.TextField{Name: "mode", Max: 20},
			// The player's engine, by its User-Agent.
			&core.TextField{Name: "client", Max: 200},
			&core.DateField{Name: "started"},
			&core.DateField{Name: "ended"},
			// The last address's: a stopped playing is refused until then.
			&core.DateField{Name: "expires"},
			&core.NumberField{Name: "position_ms", OnlyInt: true},
			&core.NumberField{Name: "duration_ms", OnlyInt: true},
			&core.NumberField{Name: "bytes", OnlyInt: true},
			&core.BoolField{Name: "revoked"},
			&core.AutodateField{Name: "created", OnCreate: true},
		)
		history.AddIndex("idx_play_history_ended", false, "`ended`", "")
		history.AddIndex("idx_play_history_revoked", false, "`revoked`, `expires`", "")
		return app.Save(history)
	}, nil)
}
