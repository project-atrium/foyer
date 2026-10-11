package migrations_test

import (
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"

	_ "foyer/migrations"
)

// upTo is a server whose migrations ran up to and including the file named:
// the database a real server of that time holds.
func upTo(t *testing.T, last string) core.App {
	t.Helper()
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir(), EncryptionEnv: "pb_test_env"})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	var list core.MigrationsList
	for _, m := range core.AppMigrations.Items() {
		if m.File <= last {
			list.Add(m)
		}
	}
	if _, err := core.NewMigrationsRunner(app, list).Up(); err != nil {
		t.Fatal(err)
	}
	return app
}

func save(t *testing.T, app core.App, collection string, fields map[string]any) *core.Record {
	t.Helper()
	c, err := app.FindCollectionByNameOrId(collection)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(c)
	for k, v := range fields {
		record.Set(k, v)
	}
	if err := app.Save(record); err != nil {
		t.Fatalf("saving into %s: %v", collection, err)
	}
	return record
}

// Step 1's server upgrades in place: its share and TMDB become connections
// that keep their ids, settings and the webhook's token — so Zurg's config
// keeps working — and its libraries become folders of that share.
func TestAdaptersBecomeConnectionsInPlace(t *testing.T) {
	app := upTo(t, "0006_watch.go")
	share := save(t, app, "adapters", map[string]any{
		"key": "webdav", "enabled": true,
		"config":  map[string]any{"url": "http://zurg:9999/dav", "interval": 15},
		"secrets": map[string]any{"password": "", "webhook": "the-token"},
	})
	tmdb := save(t, app, "adapters", map[string]any{"key": "tmdb", "enabled": true, "secrets": map[string]any{"key": "a key"}})
	lib := save(t, app, "libraries", map[string]any{"name": "movies", "kind": "movies", "folder": "movies", "enabled": true})

	if err := app.RunAppMigrations(); err != nil {
		t.Fatal(err)
	}

	if _, err := app.FindCollectionByNameOrId("adapters"); err == nil {
		t.Error("adapters is still there")
	}
	got, err := app.FindRecordById("connections", share.Id)
	if err != nil {
		t.Fatalf("the share is gone: %v", err)
	}
	secrets := map[string]any{}
	_ = got.UnmarshalJSONField("secrets", &secrets)
	config := map[string]any{}
	_ = got.UnmarshalJSONField("config", &config)
	if got.GetString("adapter") != "webdav" || got.GetString("name") != "WebDAV" || !got.GetBool("enabled") ||
		secrets["webhook"] != "the-token" || config["url"] != "http://zurg:9999/dav" {
		t.Fatalf("the share: %v, secrets %v, config %v", got.PublicExport(), secrets, config)
	}
	if again, err := app.FindRecordById("connections", tmdb.Id); err != nil || again.GetString("name") != "TMDB" {
		t.Fatalf("tmdb: %v %v", again, err)
	}
	library, err := app.FindRecordById("libraries", lib.Id)
	if err != nil || library.GetString("connection") != share.Id {
		t.Fatalf("the library: %v %v", library, err)
	}
	// A folder name may now come back in another share — never twice in one.
	other := save(t, app, "connections", map[string]any{"adapter": "webdav", "name": "NAS"})
	save(t, app, "libraries", map[string]any{"name": "nas_movies", "kind": "movies", "folder": "movies", "connection": other.Id})
	c, _ := app.FindCollectionByNameOrId("libraries")
	twice := core.NewRecord(c)
	twice.Set("name", "again")
	twice.Set("kind", "movies")
	twice.Set("folder", "movies")
	twice.Set("connection", share.Id)
	if err := app.Save(twice); err == nil || !strings.Contains(err.Error(), "unique") && !strings.Contains(strings.ToLower(err.Error()), "constraint") {
		t.Fatalf("a folder twice in one share: %v", err)
	}
}

// A server that never set up a share upgrades too.
func TestAServerWithNoShareUpgrades(t *testing.T) {
	app := upTo(t, "0006_watch.go")
	if err := app.RunAppMigrations(); err != nil {
		t.Fatal(err)
	}
	if _, err := app.FindCollectionByNameOrId("connections"); err != nil {
		t.Fatal(err)
	}
}
