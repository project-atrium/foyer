package migrations

import (
	"fmt"
	"slices"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// The household, and the devices its users signed in on.
//
//   - household: one record — its id is the household's, which a device keys
//     what Foyer serves by, and its name is what Loge calls it.
//   - devices: each device that paired or signed in, with the users on it
//     and when it was last seen. Superusers only: it is the dashboard's.
//
// Pairing codes are kept in memory, never here: a code lasts minutes.
func init() {
	m.Register(func(app core.App) error {
		if err := namesFree(app, "household", "devices"); err != nil {
			return err
		}
		household := core.NewBaseCollection("household")
		household.Fields.Add(
			&core.TextField{Name: "name", Required: true, Max: 50},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		if err := app.Save(household); err != nil {
			return err
		}
		record := core.NewRecord(household)
		record.Set("name", "Foyer")
		if err := app.Save(record); err != nil {
			return err
		}

		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		devices := core.NewBaseCollection("devices")
		devices.Fields.Add(
			// What the device calls itself to Foyer: a hash of its own key, the
			// same for every user and adapter on it.
			&core.TextField{Name: "installation", Required: true, Max: 100},
			&core.TextField{Name: "name", Max: 100},
			&core.TextField{Name: "platform", Max: 20},
			&core.TextField{Name: "app_version", Max: 40},
			&core.RelationField{Name: "users", CollectionId: users.Id, MaxSelect: 50},
			&core.BoolField{Name: "paired"},
			&core.DateField{Name: "last_seen"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		devices.AddIndex("idx_devices_installation", true, "`installation`", "")
		if err := app.Save(devices); err != nil {
			return err
		}

		// A pairing code is short: guessing one gets five tries a minute from
		// an address. A narrower prefix goes before /api/, which PocketBase
		// would take first otherwise.
		settings := app.Settings()
		rules := slices.DeleteFunc(settings.RateLimits.Rules, func(r core.RateLimitRule) bool { return r.Label == "/api/foyer/pair" })
		at := slices.IndexFunc(rules, func(r core.RateLimitRule) bool { return r.Label == "/api/" })
		if at < 0 {
			at = len(rules)
		}
		settings.RateLimits.Rules = slices.Insert(rules, at, core.RateLimitRule{Label: "/api/foyer/pair", MaxRequests: 5, Duration: 60})
		return app.Save(settings)
	}, nil)
}

// namesFree refuses to make a collection whose name a library took first —
// with that library's name, so the household knows what to rename — before
// PocketBase refuses it less helpfully, and the server does not start.
func namesFree(app core.App, names ...string) error {
	for _, name := range names {
		if _, err := app.FindCollectionByNameOrId(name); err == nil {
			return fmt.Errorf("this version of Foyer makes a collection called %q, and one of that name exists already — a library's? "+
				"Remove or rename it with the version before, then start this one again", name)
		}
	}
	return nil
}
