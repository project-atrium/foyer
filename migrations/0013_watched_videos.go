package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// What a video of YouTube's is, kept on the watch state of whoever began it:
// a video is no record of Foyer's, so Continue watching draws it from here —
// its title, its channel, its picture — without asking the site.
func init() {
	m.Register(func(app core.App) error {
		states, err := app.FindCollectionByNameOrId("watch_states")
		if err != nil {
			return err
		}
		states.Fields.Add(
			&core.TextField{Name: "title", Max: 300},
			&core.TextField{Name: "subtitle", Max: 200},
			&core.TextField{Name: "subtitle_id", Max: 64},
			&core.TextField{Name: "picture", Max: 1000},
		)
		return app.Save(states)
	}, nil)
}
