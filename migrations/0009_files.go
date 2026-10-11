package migrations

import (
	"slices"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Posters, backdrops and stills come from PocketBase's file route, a request
// each: a TV scrolling its rows asks for dozens a second. They get a limit of
// their own, before /api/ — which PocketBase would take first otherwise — so
// pictures never starve a device's own reports.
func init() {
	m.Register(func(app core.App) error {
		settings := app.Settings()
		rules := slices.DeleteFunc(settings.RateLimits.Rules, func(r core.RateLimitRule) bool { return r.Label == "/api/files/" })
		at := slices.IndexFunc(rules, func(r core.RateLimitRule) bool { return r.Label == "/api/" })
		if at < 0 {
			at = len(rules)
		}
		settings.RateLimits.Rules = slices.Insert(rules, at, core.RateLimitRule{Label: "/api/files/", MaxRequests: 600, Duration: 10})
		return app.Save(settings)
	}, nil)
}
