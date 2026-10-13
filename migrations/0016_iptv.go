package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// A portal's lineup, one record per IPTV connection: its groups and every
// channel, as the last read of the whole list found them, each channel's
// link command among them — so it is the superusers' alone, and users read
// it through Foyer's routes, which leave the commands out. Kept whole in one
// record: a read replaces it in one write, and a portal of thousands of
// channels is a megabyte or two.
func init() {
	m.Register(func(app core.App) error {
		if err := namesFree(app, "iptv_lineups"); err != nil {
			return err
		}
		connections, err := app.FindCollectionByNameOrId("connections")
		if err != nil {
			return err
		}
		lineups := core.NewBaseCollection("iptv_lineups")
		lineups.Fields.Add(
			&core.RelationField{Name: "connection", CollectionId: connections.Id, MaxSelect: 1, Required: true, CascadeDelete: true},
			// When the last read of the whole list was, and the last try.
			&core.DateField{Name: "read_at"},
			&core.DateField{Name: "tried_at"},
			// Why the last try failed, where it did.
			&core.TextField{Name: "error", Max: 1000},
			&core.JSONField{Name: "groups", MaxSize: 4 << 20},
			&core.JSONField{Name: "channels", MaxSize: 64 << 20},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		lineups.AddIndex("idx_iptv_lineups_connection", true, "`connection`", "")
		return app.Save(lineups)
	}, nil)
}
