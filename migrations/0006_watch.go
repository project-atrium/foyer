package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// What each user watched: one record per user and thing watched, keyed by
// its identity — `tmdb:movie:603`, `tmdb:tv:1396/s1e3` — so a film's every
// file, in any library, is one, and a title that leaves and comes back finds
// its history. A user reads its own; Foyer's routes write them, by the rules
// the routes keep.
func init() {
	m.Register(func(app core.App) error {
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		own := types.Pointer("user = @request.auth.id")
		states := core.NewBaseCollection("watch_states")
		states.ListRule, states.ViewRule = own, own
		states.Fields.Add(
			&core.RelationField{Name: "user", CollectionId: users.Id, MaxSelect: 1, Required: true, CascadeDelete: true},
			&core.TextField{Name: "identity", Required: true, Max: 300},
			// The title it was last played as, to show it by.
			&core.TextField{Name: "item_collection", Max: 64},
			&core.TextField{Name: "item_id", Max: 32},
			&core.JSONField{Name: "external_ids", MaxSize: 4 << 10},
			&core.BoolField{Name: "watched"},
			&core.NumberField{Name: "position_ms", OnlyInt: true},
			&core.NumberField{Name: "duration_ms", OnlyInt: true},
			&core.NumberField{Name: "play_count", OnlyInt: true},
			&core.DateField{Name: "last_played_at"},
			// The playing that finished it last, so a report sent twice counts once.
			&core.TextField{Name: "completed_session", Max: 64},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		states.AddIndex("idx_watch_states_user_identity", true, "`user`, `identity`", "")
		states.AddIndex("idx_watch_states_user_played", false, "`user`, `last_played_at`", "")
		states.AddIndex("idx_watch_states_identity", false, "`identity`", "")
		return app.Save(states)
	}, nil)
}
