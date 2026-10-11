package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/adapters/webdav/webdavtest"
	"foyer/internal/library"
)

// addLibrary makes a library of a folder of the household's one share — made
// here, pointing nowhere, if there is none yet.
func (h *harness) addLibrary(admin, folder, name, kind string) map[string]any {
	h.t.Helper()
	if shares, err := h.server.Adapters.Connections("webdav"); err != nil || len(shares) == 0 {
		h.share(admin, "Zurg", "http://127.0.0.1:9/dav")
	}
	got := h.call(http.MethodPost, "/api/foyer/admin/libraries", admin, map[string]any{"folder": folder, "name": name, "kind": kind, "enabled": true})
	if got.status != http.StatusOK {
		h.t.Fatalf("adding %s: %d %v", name, got.status, got.body)
	}
	return got.body
}

// share sets a WebDAV share up, as the Adapters page does, and answers its
// connection's id.
func (h *harness) share(admin, name, address string) string {
	h.t.Helper()
	got := h.call(http.MethodPost, "/api/foyer/admin/connections", admin, map[string]any{
		"adapter": "webdav", "name": name, "enabled": true, "values": map[string]any{"url": address},
	})
	if got.status != http.StatusOK {
		h.t.Fatalf("setting up %s: %d %v", name, got.status, got.body)
	}
	return got.body["id"].(string)
}

// save writes a record as Foyer's own code does, past the guard.
func (h *harness) save(collection string, fields map[string]any) *core.Record {
	h.t.Helper()
	c, err := h.app.FindCollectionByNameOrId(collection)
	if err != nil {
		h.t.Fatal(err)
	}
	r := core.NewRecord(c)
	for k, v := range fields {
		r.Set(k, v)
	}
	if err := h.app.Save(r); err != nil {
		h.t.Fatalf("saving into %s: %v", collection, err)
	}
	return r
}

func TestAShowsFolderIsTwoCollectionsAndAMoviesFolderOne(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	h.addLibrary(admin, "movies", "movies", "movies")
	h.addLibrary(admin, "shows", "shows", "shows")
	h.addLibrary(admin, "anime", "anime", "shows")

	movies, err := h.app.FindCollectionByNameOrId("movies")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"title", "tmdb_id", "imdb_id", "identity", "versions", "quality", "encoding", "container", "audio_tracks", "subtitles", "poster", "backdrop", "logo", "added", "last_added", "match_state", "release_date"} {
		if movies.Fields.GetByName(field) == nil {
			t.Errorf("movies has no %s", field)
		}
	}
	if !strings.HasPrefix(movies.Id, "pbc_") {
		t.Errorf("movies' id %q", movies.Id)
	}
	if _, err := h.app.FindCollectionByNameOrId("movies_episodes"); err == nil {
		t.Error("a movies library has episodes")
	}
	shows, err := h.app.FindCollectionByNameOrId("anime")
	if err != nil {
		t.Fatal(err)
	}
	episodes, err := h.app.FindCollectionByNameOrId("anime_episodes")
	if err != nil {
		t.Fatal(err)
	}
	relation, _ := episodes.Fields.GetByName("show").(*core.RelationField)
	if relation == nil || relation.CollectionId != shows.Id || shows.Fields.GetByName("seasons") == nil || shows.Fields.GetByName("release_date") != nil {
		t.Fatalf("anime's episodes point at %+v", relation)
	}
}

func TestALibraryNameThatWillNotDoIsRefused(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	h.addLibrary(admin, "movies", "movies", "movies")
	for name, body := range map[string]map[string]any{
		"upper case":            {"folder": "a", "name": "Movies", "kind": "movies"},
		"Foyer's own":           {"folder": "b", "name": "users", "kind": "movies"},
		"PocketBase's own":      {"folder": "c", "name": "sqlite_x", "kind": "movies"},
		"an episodes' name":     {"folder": "d", "name": "x_episodes", "kind": "movies"},
		"a library's":           {"folder": "e", "name": "movies", "kind": "shows"},
		"a folder taken":        {"folder": "movies", "name": "films", "kind": "movies"},
		"neither movies nor tv": {"folder": "f", "name": "music", "kind": "music"},
		// What Foyer's next migrations make: a library of that name would
		// stop them, and the server with them.
		"a collection to come": {"folder": "g", "name": "playlists", "kind": "movies"},
		"IPTV's":               {"folder": "h", "name": "iptv_movies", "kind": "movies"},
		"YouTube's":            {"folder": "i", "name": "youtube_videos", "kind": "movies"},
	} {
		if got := h.call(http.MethodPost, "/api/foyer/admin/libraries", admin, body); got.status != http.StatusBadRequest {
			t.Errorf("%s: %d %v", name, got.status, got.body)
		}
	}
}

// A best guess is hidden from users until a superuser confirms or corrects
// it — its episodes too.
func TestAnUncertainTitleIsHiddenFromUsers(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	h.addLibrary(admin, "movies", "movies", "movies")
	h.addLibrary(admin, "shows", "shows", "shows")
	h.save("movies", map[string]any{"title": "Sure", "match_state": "matched"})
	h.save("movies", map[string]any{"title": "Unmatched", "match_state": "unmatched"})
	h.save("movies", map[string]any{"title": "Guess", "match_state": "uncertain"})
	guess := h.save("shows", map[string]any{"title": "Guessed Show", "match_state": "uncertain"})
	h.save("shows_episodes", map[string]any{"show": guess.Id, "title": "Pilot", "season": 1, "episode": 1})
	alex := h.makeUser("alex", nil)

	titlesOf := func(token, collection string) []string {
		got := h.call(http.MethodGet, "/api/collections/"+collection+"/records?sort=title", token, nil)
		if got.status != http.StatusOK {
			t.Fatalf("%s: %d %v", collection, got.status, got.body)
		}
		var names []string
		for _, item := range got.body["items"].([]any) {
			names = append(names, item.(map[string]any)["title"].(string))
		}
		return names
	}
	if got := strings.Join(titlesOf(alex.token, "movies"), ","); got != "Sure,Unmatched" {
		t.Errorf("alex's movies: %s", got)
	}
	if got := titlesOf(alex.token, "shows_episodes"); len(got) != 0 {
		t.Errorf("alex's episodes: %v", got)
	}
	if got := strings.Join(titlesOf(admin, "movies"), ","); got != "Guess,Sure,Unmatched" {
		t.Errorf("the superuser's movies: %s", got)
	}
	if got := h.call(http.MethodGet, "/api/collections/movies/records", "", nil); got.status != http.StatusOK || len(got.body["items"].([]any)) != 0 {
		t.Errorf("a guest's movies: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodGet, "/api/collections/media_files/records", alex.token, nil); got.status != http.StatusForbidden {
		t.Errorf("alex reading the files: %d", got.status)
	}
}

func TestALibrarysCollectionsRefuseEvenASuperusersWrites(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	h.addLibrary(admin, "shows", "shows", "shows")
	for _, c := range []string{"shows", "shows_episodes", "libraries", "media_files"} {
		if got := h.call(http.MethodPost, "/api/collections/"+c+"/records", admin, map[string]any{"title": "x"}); got.status != http.StatusForbidden {
			t.Errorf("a record in %s: %d %v", c, got.status, got.body)
		}
		if got := h.call(http.MethodPatch, "/api/collections/"+c, admin, map[string]any{"listRule": ""}); got.status != http.StatusForbidden {
			t.Errorf("changing %s: %d %v", c, got.status, got.body)
		}
	}
}

func TestEnsureRepairsALibrarysCollections(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	body := h.addLibrary(admin, "movies", "movies", "movies")
	movies, _ := h.app.FindCollectionByNameOrId("movies")
	movies.Fields.RemoveByName("tagline")
	if err := h.app.Save(movies); err != nil {
		t.Fatal(err)
	}
	lib, err := h.app.FindRecordById("libraries", body["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := library.Ensure(h.app, lib); err != nil {
			t.Fatal(err)
		}
	}
	movies, _ = h.app.FindCollectionByNameOrId("movies")
	if movies.Fields.GetByName("tagline") == nil || lib.GetInt("template") != library.TemplateVersion {
		t.Fatal("the template was not applied")
	}
}

func TestRemovingALibraryTakesItsCollectionsAndFiles(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	body := h.addLibrary(admin, "shows", "shows", "shows")
	id := body["id"].(string)
	entry := h.save("media_entries", map[string]any{"library": id, "name": "Some Show"})
	h.save("media_files", map[string]any{"library": id, "entry": entry.Id, "path": "Some Show/1.mkv"})
	before, _ := h.app.FindCollectionByNameOrId("shows")
	if got := h.call(http.MethodDelete, "/api/foyer/admin/libraries/"+id, admin, nil); got.status != http.StatusNoContent {
		t.Fatalf("%d %v", got.status, got.body)
	}
	for _, c := range []string{"shows", "shows_episodes"} {
		if _, err := h.app.FindCollectionByNameOrId(c); err == nil {
			t.Errorf("%s is still there", c)
		}
	}
	if n, _ := h.app.CountRecords("media_files"); n != 0 {
		t.Errorf("%d files left", n)
	}
	// Made again at once, it gets an id of its own: PocketBase removes the
	// old one's files in the background, under the old id.
	h.addLibrary(admin, "shows", "shows", "shows")
	after, _ := h.app.FindCollectionByNameOrId("shows")
	if after.Id == before.Id {
		t.Fatalf("the same id twice: %s", after.Id)
	}
}

func TestTheSharesFoldersComeWithTheirLibraries(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	fake := webdavtest.New()
	for _, folder := range []string{"__all__", "__unplayable__", "movies", "shows"} {
		fake.Dir(folder, time.Now())
	}
	server := httptest.NewServer(fake)
	defer server.Close()

	// Before the share is switched on, the page is told why there is nothing.
	made := h.call(http.MethodPost, "/api/foyer/admin/connections", admin, map[string]any{
		"adapter": "webdav", "name": "Zurg", "enabled": false, "values": map[string]any{"url": server.URL + "/dav"},
	})
	if made.status != http.StatusOK {
		t.Fatalf("setting up the share: %d %v", made.status, made.body)
	}
	id := made.body["id"].(string)
	if got := h.call(http.MethodGet, "/api/foyer/admin/connections/"+id+"/folders", admin, nil); got.status != http.StatusOK || got.body["ok"] != false || got.body["message"] == "" {
		t.Fatalf("switched off: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodPut, "/api/foyer/admin/connections/"+id, admin, map[string]any{"name": "Zurg", "enabled": true, "values": map[string]any{"url": server.URL + "/dav"}}); got.status != http.StatusOK {
		t.Fatalf("switching the share on: %d %v", got.status, got.body)
	}
	h.addLibrary(admin, "movies", "movies", "movies")
	got := h.call(http.MethodGet, "/api/foyer/admin/connections/"+id+"/folders", admin, nil)
	folders, _ := got.body["folders"].([]any)
	if got.status != http.StatusOK || got.body["ok"] != true || len(folders) != 4 {
		t.Fatalf("%d %v", got.status, got.body)
	}
	for _, raw := range folders {
		f := raw.(map[string]any)
		if (f["folder"] == "movies") != (f["library"] != nil) {
			t.Errorf("folder %v", f)
		}
	}
}
