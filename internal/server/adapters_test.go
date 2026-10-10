package server_test

import (
	"net/http"
	"testing"

	"github.com/pocketbase/pocketbase/core"
)

func tmdbView(t *testing.T, list []any) map[string]any {
	t.Helper()
	for _, raw := range list {
		if view, _ := raw.(map[string]any); view["key"] == "tmdb" {
			return view
		}
	}
	t.Fatalf("no tmdb adapter in %v", list)
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

func TestAnAdapterStartsOffWithItsDefaults(t *testing.T) {
	h := start(t, options{})
	got := h.call(http.MethodGet, "/api/foyer/admin/adapters", h.superuser(), nil)
	if got.status != http.StatusOK {
		t.Fatalf("%d %v", got.status, got.body)
	}
	view := tmdbView(t, got.list)
	values := view["values"].(map[string]any)
	if view["enabled"] != false || values["language"] != "en-US" || values["key"] != "" || view["notice"] == "" {
		t.Fatalf("view %v", view)
	}
}

// A secret is stored as typed and never sent back: the page sees it masked,
// and a save that leaves it out — or sends the mask back — keeps it.
func TestASecretIsMaskedAndKeptWhenASaveLeavesItOut(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	got := h.call(http.MethodPut, "/api/foyer/admin/adapters/tmdb", admin, map[string]any{
		"enabled": true, "values": map[string]any{"key": "a secret key", "language": "de-DE"},
	})
	if got.status != http.StatusOK || got.body["values"].(map[string]any)["key"] != "******" {
		t.Fatalf("save: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodPut, "/api/foyer/admin/adapters/tmdb", admin, map[string]any{
		"enabled": true, "values": map[string]any{"key": "******", "language": "en-GB"},
	}); got.status != http.StatusOK {
		t.Fatalf("save again: %d %v", got.status, got.body)
	}
	record, err := h.app.FindFirstRecordByData("adapters", "key", "tmdb")
	if err != nil {
		t.Fatal(err)
	}
	secrets := map[string]any{}
	_ = record.UnmarshalJSONField("secrets", &secrets)
	config := map[string]any{}
	_ = record.UnmarshalJSONField("config", &config)
	if secrets["key"] != "a secret key" || config["language"] != "en-GB" || config["key"] != nil {
		t.Fatalf("secrets %v, config %v", secrets, config)
	}
	// A profile reads no adapter at all; a superuser sees everything, secrets
	// included, as the docs say.
	alex := h.makeUser("alex", nil)
	if listed := h.call(http.MethodGet, "/api/collections/adapters/records", alex.token, nil); listed.status != http.StatusForbidden {
		t.Fatalf("a profile listing adapters: %d %v", listed.status, listed.body)
	}
}

func TestASaveThatWillNotDoNamesTheField(t *testing.T) {
	h := start(t, options{})
	got := h.call(http.MethodPut, "/api/foyer/admin/adapters/tmdb", h.superuser(), map[string]any{
		"enabled": true, "values": map[string]any{"language": "German"},
	})
	values, _ := got.body["data"].(map[string]any)["values"].(map[string]any)
	if got.status != http.StatusBadRequest || values["language"] == nil || values["key"] == nil {
		t.Fatalf("%d %v", got.status, got.body)
	}
}

func TestAnUnknownAdapterIsNotFound(t *testing.T) {
	h := start(t, options{})
	if got := h.call(http.MethodPut, "/api/foyer/admin/adapters/plex", h.superuser(), map[string]any{}); got.status != http.StatusNotFound {
		t.Fatalf("%d %v", got.status, got.body)
	}
}

// --- The guard --------------------------------------------------------------------

func TestFoyersCollectionsRefuseEvenASuperusersWrites(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	if got := h.call(http.MethodPost, "/api/collections/adapters/records", admin, map[string]any{"key": "webdav"}); got.status != http.StatusForbidden {
		t.Errorf("a record: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodPatch, "/api/collections/adapters", admin, map[string]any{"listRule": ""}); got.status != http.StatusForbidden {
		t.Errorf("the collection: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodDelete, "/api/collections/adapters/truncate", admin, nil); got.status != http.StatusForbidden {
		t.Errorf("truncating: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodDelete, "/api/collections/adapters", admin, nil); got.status != http.StatusForbidden {
		t.Errorf("deleting: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodPut, "/api/collections/import", admin, map[string]any{
		"collections": []any{map[string]any{"name": "adapters", "type": "base"}}, "deleteMissing": false,
	}); got.status != http.StatusForbidden {
		t.Errorf("importing: %d %v", got.status, got.body)
	}
	// Foyer's own code saves past the guard.
	collection, err := h.app.FindCollectionByNameOrId("adapters")
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("key", "webdav")
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
