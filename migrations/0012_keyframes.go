package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Where each file's keyframes lie, for a remux: read from the file's own
// index — Matroska's Cues, an MP4's sync samples — the first time one needs
// it, and kept on the file, so a playlist is never more than a lookup away.
// `none` is a file that keeps no index: it plays as it is.
func init() {
	m.Register(func(app core.App) error {
		files, err := app.FindCollectionByNameOrId("media_files")
		if err != nil {
			return err
		}
		files.Fields.Add(
			&core.JSONField{Name: "keyframes", MaxSize: 4 << 20},
			&core.SelectField{Name: "keyframes_state", MaxSelect: 1, Values: []string{"done", "none"}},
		)
		return app.Save(files)
	}, nil)
}
