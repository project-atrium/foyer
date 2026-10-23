package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
)

func typeView(t *testing.T, list []any, key string) map[string]any {
	t.Helper()
	for _, raw := range list {
		if view, _ := raw.(map[string]any); view["key"] == key {
			return view
		}
	}
	t.Fatalf("no %s adapter in %v", key, list)
	return nil
}

func TestTheAdaptersPageIsTheSuperusersOnly(t *testing.T) {
	h := start(t, options{})
	alex := h.makeUser("alex", nil)
	for _, token := range []string{"", alex.token} {
		if got := h.call(http.MethodGet, "/api/foyer/admin/adapters", token, nil); got.status != http.StatusUnauthorized && got.status != http.StatusForbidden {
			t.Errorf("token %q: %d", token, got.status)
		}
		if got := h.call(http.MethodPut, "/api/foyer/admin/adapters/tmdb", token, map[string]any{"enabled": true}); got.status != http.StatusUnauthorized && got.status != http.StatusForbidden {
			t.Errorf("token %q saving: %d", token, got.status)
		}
	}
}

func TestAnAdapterStartsWithNoConnectionAndItsDefaults(t *testing.T) {
	h := start(t, options{})
	got := h.call(http.MethodGet, "/api/foyer/admin/adapters", h.superuser(), nil)
	if got.status != http.StatusOK {
		t.Fatalf("%d %v", got.status, got.body)
	}
	view := typeView(t, got.list, "tmdb")
	defaults := view["defaults"].(map[string]any)
	if len(view["connections"].([]any)) != 0 || defaults["language"] != "en-US" || defaults["apiKey"] != "" || view["notice"] == "" ||
		view["single"] != true || view["category"] != "metadata" {
		t.Fatalf("view %v", view)
	}
	// The page lists them by category, in its order.
	var categories []string
	for _, raw := range got.list {
		category := raw.(map[string]any)["category"].(string)
		if len(categories) == 0 || categories[len(categories)-1] != category {
			categories = append(categories, category)
		}
	}
	if strings.Join(categories, ",") != "sources,iptv,metadata,tracking,tools" {
		t.Fatalf("categories %v", categories)
	}
}

// What Foyer will reach is listed, planned, with the fields it will ask for
// — and cannot be set up yet.
func TestAPlannedAdapterIsListedAndCannotBeSetUp(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	got := h.call(http.MethodGet, "/api/foyer/admin/adapters", admin, nil)
	for key, category := range map[string]string{"jellyfin": "sources", "xtream": "iptv", "simkl": "tracking", "yattee": "sources"} {
		view := typeView(t, got.list, key)
		if view["planned"] != true || view["category"] != category || len(view["fields"].([]any)) == 0 {
			t.Errorf("%s: %v", key, view)
		}
	}
	// A portal — built now — keeps its MAC address a secret shown as typed.
	if stalker := typeView(t, got.list, "stalker"); stalker["planned"] != false || stalker["category"] != "iptv" {
		t.Errorf("stalker: %v", stalker)
	}
	for _, raw := range typeView(t, got.list, "stalker")["fields"].([]any) {
		if f := raw.(map[string]any); f["key"] == "mac" && (f["type"] != "password" || f["visible"] != true) {
			t.Errorf("mac %v", f)
		}
	}
	if made := h.call(http.MethodPost, "/api/foyer/admin/connections", admin, map[string]any{"adapter": "jellyfin", "enabled": true, "values": map[string]any{"serverUrl": "http://jf:8096", "username": "a"}}); made.status != http.StatusBadRequest {
		t.Fatalf("setting a planned adapter up: %d %v", made.status, made.body)
	}
	if got := h.call(http.MethodPut, "/api/foyer/admin/adapters/simkl", admin, map[string]any{"enabled": true, "values": map[string]any{"clientId": "x"}}); got.status != http.StatusBadRequest {
		t.Fatalf("saving a planned single adapter: %d %v", got.status, got.body)
	}
	if tested := h.call(http.MethodPost, "/api/foyer/admin/adapters/plex/test", admin, map[string]any{}); tested.status != http.StatusOK || tested.body["ok"] != false {
		t.Fatalf("testing a planned adapter: %d %v", tested.status, tested.body)
	}
	if n, _ := h.app.CountRecords("connections"); n != 0 {
		t.Fatalf("%d connections made", n)
	}
}

// A secret is stored as typed and never sent back: the page sees it masked,
// and a save that leaves it out — or sends the mask back — keeps it.
func TestASecretIsMaskedAndKeptWhenASaveLeavesItOut(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	got := h.call(http.MethodPut, "/api/foyer/admin/adapters/tmdb", admin, map[string]any{
		"enabled": true, "values": map[string]any{"apiKey": "a secret key", "language": "de-DE"},
	})
	if got.status != http.StatusOK || got.body["values"].(map[string]any)["apiKey"] != "******" || got.body["name"] != "TMDB" {
		t.Fatalf("save: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodPut, "/api/foyer/admin/adapters/tmdb", admin, map[string]any{
		"enabled": true, "values": map[string]any{"apiKey": "******", "language": "en-GB"},
	}); got.status != http.StatusOK {
		t.Fatalf("save again: %d %v", got.status, got.body)
	}
	// Saved twice, it is still the household's one TMDB connection.
	if n, _ := h.app.CountRecords("connections"); n != 1 {
		t.Fatalf("%d connections", n)
	}
	record, err := h.app.FindFirstRecordByData("connections", "adapter", "tmdb")
	if err != nil {
		t.Fatal(err)
	}
	secrets := map[string]any{}
	_ = record.UnmarshalJSONField("secrets", &secrets)
	config := map[string]any{}
	_ = record.UnmarshalJSONField("config", &config)
	if secrets["apiKey"] != "a secret key" || config["language"] != "en-GB" || config["apiKey"] != nil {
		t.Fatalf("secrets %v, config %v", secrets, config)
	}
	// A profile reads no adapter at all; a superuser sees everything, secrets
	// included, as the docs say.
	alex := h.makeUser("alex", nil)
	if listed := h.call(http.MethodGet, "/api/collections/connections/records", alex.token, nil); listed.status != http.StatusForbidden {
		t.Fatalf("a profile listing connections: %d %v", listed.status, listed.body)
	}
}

func TestASaveThatWillNotDoNamesTheField(t *testing.T) {
	h := start(t, options{})
	got := h.call(http.MethodPut, "/api/foyer/admin/adapters/tmdb", h.superuser(), map[string]any{
		"enabled": true, "values": map[string]any{"language": "German"},
	})
	values, _ := got.body["data"].(map[string]any)["values"].(map[string]any)
	if got.status != http.StatusBadRequest || values["language"] == nil || values["apiKey"] == nil {
		t.Fatalf("%d %v", got.status, got.body)
	}
}

func TestAnUnknownAdapterIsNotFound(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	if got := h.call(http.MethodPut, "/api/foyer/admin/adapters/nonesuch", admin, map[string]any{}); got.status != http.StatusNotFound {
		t.Fatalf("%d %v", got.status, got.body)
	}
	if got := h.call(http.MethodPost, "/api/foyer/admin/connections", admin, map[string]any{"adapter": "nonesuch"}); got.status != http.StatusNotFound {
		t.Fatalf("a connection: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodPut, "/api/foyer/admin/connections/nonesuch", admin, map[string]any{}); got.status != http.StatusNotFound {
		t.Fatalf("an update: %d %v", got.status, got.body)
	}
}

// --- Connections ------------------------------------------------------------------

// A WebDAV share is set up as often as the household has shares, each named
// and switched on its own; TMDB once.
func TestEachShareIsAConnectionOfItsOwn(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	zurg := h.share(admin, "Zurg", "http://zurg:9999/dav")
	nas := h.share(admin, "The NAS", "https://nas.example.com/dav")
	got := h.call(http.MethodGet, "/api/foyer/admin/adapters", admin, nil)
	connections := typeView(t, got.list, "webdav")["connections"].([]any)
	if len(connections) != 2 || connections[0].(map[string]any)["name"] != "Zurg" || connections[1].(map[string]any)["name"] != "The NAS" {
		t.Fatalf("connections %v", connections)
	}
	if got := h.call(http.MethodPut, "/api/foyer/admin/connections/"+nas, admin, map[string]any{
		"name": "NAS", "enabled": false, "values": map[string]any{"serverUrl": "https://nas.example.com/webdav"},
	}); got.status != http.StatusOK || got.body["name"] != "NAS" || got.body["enabled"] != false {
		t.Fatalf("update: %d %v", got.status, got.body)
	}
	if c, err := h.server.Adapters.Connection(zurg); err != nil || c.String("serverUrl") != "http://zurg:9999/dav" || !c.Enabled {
		t.Fatalf("the other share changed: %v %v", c, err)
	}
	if got := h.call(http.MethodDelete, "/api/foyer/admin/connections/"+nas, admin, nil); got.status != http.StatusNoContent {
		t.Fatalf("removing: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodDelete, "/api/foyer/admin/connections/"+nas, admin, nil); got.status != http.StatusNotFound {
		t.Fatalf("removing again: %d %v", got.status, got.body)
	}
	// A share's address is checked as ever, and named by its field.
	bad := h.call(http.MethodPost, "/api/foyer/admin/connections", admin, map[string]any{"adapter": "webdav", "enabled": true, "values": map[string]any{"serverUrl": "zurg"}})
	if values, _ := bad.body["data"].(map[string]any)["values"].(map[string]any); bad.status != http.StatusBadRequest || values["serverUrl"] == nil {
		t.Fatalf("a bad address: %d %v", bad.status, bad.body)
	}
	// TMDB is the household's once: a second connection is refused.
	if got := h.call(http.MethodPost, "/api/foyer/admin/connections", admin, map[string]any{"adapter": "tmdb", "enabled": true, "values": map[string]any{"apiKey": "k"}}); got.status != http.StatusOK {
		t.Fatalf("tmdb: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodPost, "/api/foyer/admin/connections", admin, map[string]any{"adapter": "tmdb", "enabled": true, "values": map[string]any{"apiKey": "k"}}); got.status != http.StatusBadRequest {
		t.Fatalf("a second tmdb: %d %v", got.status, got.body)
	}
	// A share is no single adapter: it is not saved by its key.
	if got := h.call(http.MethodPut, "/api/foyer/admin/adapters/webdav", admin, map[string]any{"enabled": true}); got.status != http.StatusBadRequest {
		t.Fatalf("a share by its key: %d %v", got.status, got.body)
	}
}

// A share whose folders are libraries stays until they are removed: their
// collections are Foyer's to remove, one library at a time.
func TestAShareWithLibrariesStays(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	share := h.share(admin, "Zurg", "http://zurg:9999/dav")
	lib := h.addLibrary(admin, "movies", "movies", "movies")
	if lib["connection"] != share {
		t.Fatalf("library %v", lib)
	}
	if got := h.call(http.MethodDelete, "/api/foyer/admin/connections/"+share, admin, nil); got.status != http.StatusConflict {
		t.Fatalf("removing a share in use: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodDelete, "/api/foyer/admin/libraries/"+lib["id"].(string), admin, nil); got.status != http.StatusNoContent {
		t.Fatalf("removing the library: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodDelete, "/api/foyer/admin/connections/"+share, admin, nil); got.status != http.StatusNoContent {
		t.Fatalf("removing the share: %d %v", got.status, got.body)
	}
}

// With two shares, a library says whose folder it is, and one folder name
// may be a library in each.
func TestTwoSharesMayHaveAFolderOfOneName(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	zurg := h.share(admin, "Zurg", "http://zurg:9999/dav")
	nas := h.share(admin, "NAS", "https://nas.example.com/dav")
	if got := h.call(http.MethodPost, "/api/foyer/admin/libraries", admin, map[string]any{"folder": "movies", "name": "movies", "kind": "movies"}); got.status != http.StatusBadRequest {
		t.Fatalf("no share named, with two: %d %v", got.status, got.body)
	}
	for share, name := range map[string]string{zurg: "movies", nas: "nas_movies"} {
		if got := h.call(http.MethodPost, "/api/foyer/admin/libraries", admin, map[string]any{"connection": share, "folder": "movies", "name": name, "kind": "movies"}); got.status != http.StatusOK {
			t.Fatalf("%s: %d %v", name, got.status, got.body)
		}
	}
	if got := h.call(http.MethodPost, "/api/foyer/admin/libraries", admin, map[string]any{"connection": zurg, "folder": "movies", "name": "again", "kind": "movies"}); got.status != http.StatusBadRequest {
		t.Fatalf("a folder twice in one share: %d %v", got.status, got.body)
	}
}

// --- The guard --------------------------------------------------------------------

func TestFoyersCollectionsRefuseEvenASuperusersWrites(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	if got := h.call(http.MethodPost, "/api/collections/connections/records", admin, map[string]any{"adapter": "webdav"}); got.status != http.StatusForbidden {
		t.Errorf("a record: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodPatch, "/api/collections/connections", admin, map[string]any{"listRule": ""}); got.status != http.StatusForbidden {
		t.Errorf("the collection: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodDelete, "/api/collections/connections/truncate", admin, nil); got.status != http.StatusForbidden {
		t.Errorf("truncating: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodDelete, "/api/collections/connections", admin, nil); got.status != http.StatusForbidden {
		t.Errorf("deleting: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodPut, "/api/collections/import", admin, map[string]any{
		"collections": []any{map[string]any{"name": "connections", "type": "base"}}, "deleteMissing": false,
	}); got.status != http.StatusForbidden {
		t.Errorf("importing: %d %v", got.status, got.body)
	}
	// Foyer's own code saves past the guard.
	collection, err := h.app.FindCollectionByNameOrId("connections")
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("adapter", "webdav")
	if err := h.app.Save(record); err != nil {
		t.Fatalf("a model save: %v", err)
	}
}

// A user is a profile because of its rules; loosened, one profile would read
// another.
func TestUsersRulesStayAsFoyerMadeThem(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	if got := h.call(http.MethodPatch, "/api/collections/users", admin, map[string]any{"listRule": ""}); got.status != http.StatusForbidden {
		t.Fatalf("opening users: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodDelete, "/api/collections/users", admin, nil); got.status != http.StatusForbidden {
		t.Fatalf("deleting users: %d %v", got.status, got.body)
	}
	// Its other options stay the superuser's, as PocketBase has them.
	if got := h.call(http.MethodPatch, "/api/collections/users", admin, map[string]any{"authToken": map[string]any{"duration": 86400}}); got.status != http.StatusOK {
		t.Fatalf("a token option: %d %v", got.status, got.body)
	}
}
