package migrations

import (
	"os"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"

	"foyer/internal/config"
)

// The dashboard's name, the rate limiter, and the first superuser.
//
// The limiter counts per address, never per user, so nobody who knows a
// username can keep its owner out.
//
// The superuser comes from FOYER_ADMIN_EMAIL and FOYER_ADMIN_PASSWORD, when
// the server starts for the first time with them set. Without them,
// `superuser upsert` makes one. Either way the password should not stay in
// `.env`.
func init() {
	m.Register(func(app core.App) error {
		settings := app.Settings()
		settings.Meta.AppName = "Foyer"
		settings.RateLimits.Enabled = true
		settings.RateLimits.Rules = []core.RateLimitRule{
			// Guessing a password gets nowhere.
			{Label: "*:authWithPassword", MaxRequests: 5, Duration: 60},
			{Label: "*:authRefresh", MaxRequests: 60, Duration: 60},
			{Label: "/api/", MaxRequests: 300, Duration: 10},
		}
		if err := app.Save(settings); err != nil {
			return err
		}

		cfg, err := config.FromEnv(os.Getenv)
		if err != nil || cfg.AdminEmail == "" {
			return err
		}
		superusers, err := app.FindCollectionByNameOrId(core.CollectionNameSuperusers)
		if err != nil {
			return err
		}
		if total, err := app.CountRecords(superusers); err != nil || total > 0 {
			return err
		}
		record := core.NewRecord(superusers)
		record.SetEmail(cfg.AdminEmail)
		record.SetPassword(cfg.AdminPassword)
		return app.Save(record)
	}, nil)
}
