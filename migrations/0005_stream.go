package migrations

import (
	"slices"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// A player asks for a range at every seek, and a household's players share
// one address: streams get a limit of their own. PocketBase takes the first
// rule whose prefix matches, in the list's order, so it goes before "/api/".
// The signature keeps strangers out; the limit only keeps a runaway player
// from starving the rest.
func init() {
	m.Register(func(app core.App) error {
		settings := app.Settings()
		rules := slices.DeleteFunc(settings.RateLimits.Rules, func(r core.RateLimitRule) bool {
			return r.Label == "/api/foyer/stream/" || r.Label == "/api/foyer/webdav/"
		})
		at := slices.IndexFunc(rules, func(r core.RateLimitRule) bool { return r.Label == "/api/" })
		if at < 0 {
			at = len(rules)
		}
		rules = slices.Insert(rules, at,
			core.RateLimitRule{Label: "/api/foyer/stream/", MaxRequests: 600, Duration: 10},
			core.RateLimitRule{Label: "/api/foyer/webdav/", MaxRequests: 30, Duration: 60},
		)
		settings.RateLimits.Rules = rules
		return app.Save(settings)
	}, nil)
}
