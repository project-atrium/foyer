package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Each user's connection to a tracker of their own — Trakt — and, on each
// watch state, what Foyer and each tracker last agreed of it: a change on
// either side is told from that, so neither side's is ever sent back.
// Superusers only, its tokens hidden even from them: users see theirs
// through Foyer's own routes, never a token.
func init() {
	m.Register(func(app core.App) error {
		if err := namesFree(app, "integrations"); err != nil {
			return err
		}
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		integrations := core.NewBaseCollection("integrations")
		integrations.Fields.Add(
			&core.RelationField{Name: "user", CollectionId: users.Id, MaxSelect: 1, Required: true, CascadeDelete: true},
			&core.TextField{Name: "provider", Required: true, Max: 32},
			// connecting, connected, refused — a token the tracker no longer
			// takes — or failed: a code denied or never entered.
			&core.TextField{Name: "state", Required: true, Max: 20},
			// two-way, send, or off.
			&core.TextField{Name: "mode", Max: 20},
			// Whose account it is on the tracker's side, to show.
			&core.TextField{Name: "account", Max: 200},
			&core.TextField{Name: "access_token", Max: 2000, Hidden: true},
			&core.TextField{Name: "refresh_token", Max: 2000, Hidden: true},
			&core.DateField{Name: "expires_at"},
			// While connecting: what the user types elsewhere, and how often to ask.
			&core.TextField{Name: "device_code", Max: 200, Hidden: true},
			&core.TextField{Name: "user_code", Max: 32},
			&core.TextField{Name: "verification_url", Max: 300},
			&core.DateField{Name: "code_expires_at"},
			&core.NumberField{Name: "interval", OnlyInt: true},
			&core.DateField{Name: "next_poll"},
			// What the tracker last said it had changed, to read it again only then.
			&core.JSONField{Name: "cursors", MaxSize: 4 << 10},
			&core.DateField{Name: "last_sync"},
			&core.TextField{Name: "error", Max: 500},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		integrations.AddIndex("idx_integrations_user_provider", true, "`user`, `provider`", "")
		if err := app.Save(integrations); err != nil {
			return err
		}
		states, err := app.FindCollectionByNameOrId("watch_states")
		if err != nil {
			return err
		}
		states.Fields.Add(&core.JSONField{Name: "remote", MaxSize: 4 << 10})
		return app.Save(states)
	}, nil)
}
