package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// What Foyer connects to, and how the household set each up: one record per
// adapter, written only through the Adapters page (internal/adapters). Every
// rule is nil — superusers only — and the guard refuses even their record
// writes from the API. A password is kept as typed, in `secrets`: the Adapters
// page only ever sees it masked, but a superuser reading the records sees it,
// as a superuser sees everything on this server.
func init() {
	m.Register(func(app core.App) error {
		adapters := core.NewBaseCollection("adapters")
		adapters.Fields.Add(
			&core.TextField{Name: "key", Required: true, Max: 40, Pattern: `^[a-z][a-z0-9_]*$`},
			&core.BoolField{Name: "enabled"},
			&core.JSONField{Name: "config", MaxSize: 64 << 10},
			&core.JSONField{Name: "secrets", MaxSize: 16 << 10},
			&core.JSONField{Name: "status", MaxSize: 64 << 10},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		adapters.AddIndex("idx_adapters_key", true, "`key`", "")
		return app.Save(adapters)
	}, nil)
}
