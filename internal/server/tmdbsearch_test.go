package server_test

import (
	"net/http"
	"testing"
	"time"

	"foyer/internal/adapters/tmdb"
)

// A superuser picks a title by searching TMDB rather than typing its id:
// its answers in TMDB's order, a year narrowing them, and each poster small
// and through Foyer — never at a size or a path a search could not name.
func TestASuperuserSearchesTMDBToPickATitle(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	if got := h.call(http.MethodGet, "/api/foyer/admin/tmdb/search?kind=movie&q=Twin%20Title", admin, nil); got.status != http.StatusConflict {
		t.Fatalf("before TMDB is set up: %d %v", got.status, got.body)
	}
	fake := fakeTMDB(t)
	h.server.Library.UseTMDB(tmdb.WithBase(fake.URL+"/3", fake.URL+"/t/p"), tmdb.WithBackoff(time.Millisecond))
	if got := h.call(http.MethodPut, "/api/foyer/admin/adapters/tmdb", admin, map[string]any{"enabled": true, "values": map[string]any{"key": "a-key.for.tests", "language": "en-US"}}); got.status != http.StatusOK {
		t.Fatalf("tmdb: %d %v", got.status, got.body)
	}

	both := h.call(http.MethodGet, "/api/foyer/admin/tmdb/search?kind=movie&q=Twin%20Title", admin, nil)
	results, _ := both.body["results"].([]any)
	if both.status != http.StatusOK || len(results) != 2 || results[0].(map[string]any)["year"] != 2010.0 || results[1].(map[string]any)["poster"] != "/twin2020.png" {
		t.Fatalf("both twins: %d %v", both.status, both.body)
	}
	later := h.call(http.MethodGet, "/api/foyer/admin/tmdb/search?kind=movie&q=Twin%20Title&year=2020", admin, nil).body["results"].([]any)
	if len(later) != 1 || later[0].(map[string]any)["id"] != 1004.0 || later[0].(map[string]any)["overview"] != "The later one." {
		t.Errorf("the later twin: %v", later)
	}
	if series := h.call(http.MethodGet, "/api/foyer/admin/tmdb/search?kind=tv&q=Big%20Pack", admin, nil).body["results"].([]any); len(series) != 2 || series[1].(map[string]any)["originalTitle"] != "ビッグ" {
		t.Errorf("a series: %v", series)
	}
	if none := h.call(http.MethodGet, "/api/foyer/admin/tmdb/search?kind=movie&q=", admin, nil).body["results"].([]any); len(none) != 0 {
		t.Errorf("nothing asked: %v", none)
	}

	status, header, body := h.fetchWithToken(t, "/api/foyer/admin/tmdb/image/w92/twin2020.png", admin)
	if status != http.StatusOK || header.Get("Content-Type") != "image/png" || len(body) == 0 {
		t.Errorf("a poster: %d %v", status, header)
	}
	for _, path := range []string{"/api/foyer/admin/tmdb/image/original/twin2020.png", "/api/foyer/admin/tmdb/image/w92/twin2020.gif", "/api/foyer/admin/tmdb/image/w92/a.png"} {
		if status, _, _ := h.fetchWithToken(t, path, admin); status != http.StatusNotFound {
			t.Errorf("%s: %d", path, status)
		}
	}
	// A user is no superuser.
	alex := h.makeUser("alex", map[string]any{"name": "Alex"})
	if status, _, _ := h.fetchWithToken(t, "/api/foyer/admin/tmdb/image/w92/twin2020.png", alex.token); status == http.StatusOK {
		t.Error("a user fetched through the dashboard's route")
	}
}
