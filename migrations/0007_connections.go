package migrations

import (
	"database/sql"
	"errors"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// An adapter is set up once or more: two WebDAV shares, two portals. Each is
// a connection, so `adapters` becomes `connections` — renamed in place, which
// keeps every record's id, its secrets and the webhook's token, so the line in
// Zurg's config keeps working — and a library says which share it is a folder
// of. A folder name may now come back in another share.
func init() {
	m.Register(func(app core.App) error {
		connections, err := app.FindCollectionByNameOrId("adapters")
		if err != nil {
			return err
		}
		connections.Name = "connections"
		if key, ok := connections.Fields.GetByName("key").(*core.TextField); ok {
			key.Name = "adapter"
		}
		connections.Fields.Add(
			&core.TextField{Name: "name", Max: 100},
			&core.NumberField{Name: "position", OnlyInt: true},
		)
		connections.RemoveIndex("idx_adapters_key")
		connections.AddIndex("idx_connections_adapter", false, "`adapter`", "")
		if err := app.Save(connections); err != nil {
			return err
		}

		// Each is named after its adapter, as the page showed it.
		names := map[string]string{"webdav": "WebDAV", "tmdb": "TMDB", "ffmpeg": "FFmpeg"}
		records, err := app.FindAllRecords(connections)
		if err != nil {
			return err
		}
		for _, record := range records {
			name := names[record.GetString("adapter")]
			if name == "" {
				name = record.GetString("adapter")
			}
			record.Set("name", name)
			if err := app.Save(record); err != nil {
				return err
			}
		}

		libraries, err := app.FindCollectionByNameOrId("libraries")
		if err != nil {
			return err
		}
		// No cascade: a library's collections are Foyer's to remove, and a
		// share in use is never deleted from under them.
		libraries.Fields.Add(&core.RelationField{Name: "connection", CollectionId: connections.Id, MaxSelect: 1})
		libraries.RemoveIndex("idx_libraries_folder")
		libraries.AddIndex("idx_libraries_connection_folder", true, "`connection`, `folder`", "")
		if err := app.Save(libraries); err != nil {
			return err
		}

		// Until now there was one share, so every library is a folder of it.
		share, err := app.FindFirstRecordByData(connections, "adapter", "webdav")
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		libs, err := app.FindAllRecords(libraries)
		if err != nil {
			return err
		}
		for _, lib := range libs {
			lib.Set("connection", share.Id)
			if err := app.Save(lib); err != nil {
				return err
			}
		}
		return nil
	}, nil)
}
