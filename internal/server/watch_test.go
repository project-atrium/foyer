package server_test

import (
	"net/http"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

func (s *scanned) state(user user, identity string) *core.Record {
	s.t.Helper()
	r, err := s.app.FindFirstRecordByFilter("watch_states", "user = {:user} && identity = {:identity}", dbx.Params{"user": user.id, "identity": identity})
	if err != nil {
		return nil
	}
	return r
}

func (s *scanned) report(u user, collection, id, kind string, position, duration int64) answer {
	s.t.Helper()
	return s.call(http.MethodPost, "/api/foyer/watch/progress", u.token, map[string]any{
		"collection": collection, "id": id, "kind": kind, "positionMs": position, "durationMs": duration, "session": "s1",
	})
}

func TestEachUserKeepsWhatTheyWatched(t *testing.T) {
	s := scanLibrary(t)
	alex, sam := s.makeUser("alex", nil), s.makeUser("sam", nil)
	film := s.title("movies", "Some Film")
	if got := s.report(alex, "movies", film.Id, "progress", 1_200_000, 7_200_000); got.status != http.StatusNoContent {
		t.Fatalf("progress: %d %v", got.status, got.body)
	}
	state := s.state(alex, "tmdb:movie:1001")
	if state == nil || state.GetInt("position_ms") != 1_200_000 || state.GetBool("watched") {
		t.Fatalf("alex's state: %v", state)
	}
	if s.state(sam, "tmdb:movie:1001") != nil {
		t.Fatal("sam has alex's state")
	}
	// A user reads its own states, and nobody else's.
	if got := s.call(http.MethodGet, "/api/collections/watch_states/records", sam.token, nil); got.status != http.StatusOK || len(got.body["items"].([]any)) != 0 {
		t.Fatalf("sam's list: %d %v", got.status, got.body)
	}
	// Nor writes them past Foyer's rules.
	if got := s.call(http.MethodPost, "/api/collections/watch_states/records", alex.token, map[string]any{"user": alex.id, "identity": "x", "watched": true}); got.status != http.StatusForbidden {
		t.Fatalf("a direct write: %d", got.status)
	}

	resume := s.call(http.MethodGet, "/api/foyer/catalogue/resume", alex.token, nil)
	items, _ := resume.body["items"].([]any)
	if resume.status != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["title"].(map[string]any)["title"] != "Some Film" {
		t.Fatalf("resume: %d %v", resume.status, resume.body)
	}

	if got := s.report(alex, "movies", film.Id, "stopped", 6_600_000, 7_200_000); got.status != http.StatusNoContent {
		t.Fatalf("stop: %d", got.status)
	}
	if state := s.state(alex, "tmdb:movie:1001"); !state.GetBool("watched") || state.GetInt("play_count") != 1 || state.GetInt("position_ms") != 0 {
		t.Fatalf("after the end: %v", state.PublicExport())
	}
	if items := s.call(http.MethodGet, "/api/foyer/catalogue/resume", alex.token, nil).body["items"].([]any); len(items) != 0 {
		t.Fatalf("still resumed: %v", items)
	}
}

func TestASeriesIsMarkedByItsEpisodes(t *testing.T) {
	s := scanLibrary(t)
	alex := s.makeUser("alex", nil)
	pack := s.title("shows", "Big Pack")
	if got := s.call(http.MethodPost, "/api/foyer/watch/played", alex.token, map[string]any{"collection": "shows", "id": pack.Id, "played": true}); got.status != http.StatusNoContent {
		t.Fatalf("played: %d %v", got.status, got.body)
	}
	for _, identity := range []string{"tmdb:tv:2001/s1e1", "tmdb:tv:2001/s1e2", "tmdb:tv:2001/s2e13", "tmdb:tv:2001/s0e7"} {
		state := s.state(alex, identity)
		var ids map[string]string
		if state != nil {
			_ = state.UnmarshalJSONField("external_ids", &ids)
		}
		if state == nil || !state.GetBool("watched") {
			t.Errorf("%s: %v", identity, state)
		}
	}
}

func TestNothingIsKeptForATitleAUserMayNotSee(t *testing.T) {
	s := scanLibrary(t)
	alex := s.makeUser("alex", nil)
	twin := s.title("movies", "Twin Title")
	if got := s.report(alex, "movies", twin.Id, "progress", 1000, 2000); got.status != http.StatusNotFound {
		t.Fatalf("a guess: %d", got.status)
	}
	if got := s.report(alex, "users", alex.id, "progress", 1000, 2000); got.status != http.StatusNotFound {
		t.Fatalf("not a title: %d", got.status)
	}
}

// A title TMDB named at last — here a superuser did — takes what was
// watched under its old name with it.
func TestWatchStatusFollowsACorrection(t *testing.T) {
	s := scanLibrary(t)
	alex := s.makeUser("alex", nil)
	nothing := s.title("movies", "Nothing Known")
	s.report(alex, "movies", nothing.Id, "progress", 300_000, 6_000_000)
	if s.state(alex, "title:movie:nothing known:1999") == nil {
		t.Fatal("no state under the title")
	}
	if got := s.call(http.MethodPost, "/api/foyer/admin/library/movies/"+nothing.Id+"/match", s.admin, map[string]any{"tmdbId": 1009}); got.status != http.StatusNoContent {
		t.Fatalf("match: %d %v", got.status, got.body)
	}
	if s.state(alex, "title:movie:nothing known:1999") != nil {
		t.Fatal("the old state stayed")
	}
	moved := s.state(alex, "tmdb:movie:1009")
	if moved == nil || moved.GetInt("position_ms") != 300_000 {
		t.Fatalf("moved: %v", moved)
	}
	// Merged into a film the user watched too: one state, both playings.
	film := s.title("movies", "Some Film")
	s.report(alex, "movies", film.Id, "end", 0, 7_200_000)
	known := s.title("movies", "Known After All")
	s.report(alex, "movies", known.Id, "end", 0, 6_000_000)
	if got := s.call(http.MethodPost, "/api/foyer/admin/library/movies/"+known.Id+"/match", s.admin, map[string]any{"tmdbId": 1001}); got.status != http.StatusNoContent {
		t.Fatalf("merge: %d %v", got.status, got.body)
	}
	merged := s.state(alex, "tmdb:movie:1001")
	if merged == nil || merged.GetInt("play_count") != 2 || !merged.GetBool("watched") || s.state(alex, "tmdb:movie:1009") != nil {
		t.Fatalf("merged: %v", merged)
	}
}
