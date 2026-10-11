package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"foyer/internal/adapters/tmdb"
	"foyer/internal/adapters/webdav/webdavtest"
)

// Two series of one name, both with room for the files' episodes: the
// titles the files give them say which — TMDB's 1963 Doctor Who first,
// and its 2005 one whose first season begins "Rose" — and the panel shows
// why.
func TestEpisodeTitlesTellTwoSeriesOfOneNameApart(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	fake := fakeTMDB(t)
	h.server.Library.UseTMDB(tmdb.WithBase(fake.URL+"/3", fake.URL+"/t/p"), tmdb.WithBackoff(time.Millisecond))
	if got := h.call(http.MethodPut, "/api/foyer/admin/adapters/tmdb", admin, map[string]any{"enabled": true, "values": map[string]any{"key": "a-key.for.tests", "language": "en-US"}}); got.status != http.StatusOK {
		t.Fatalf("tmdb: %d %v", got.status, got.body)
	}
	share := webdavtest.New()
	share.AsZurg = true
	pack := "shows/Doctor.Who.S01.1080p.BluRay.x264-GRP"
	for _, name := range []string{"Doctor.Who.S01E01.Rose.1080p.BluRay.x264-GRP.mkv", "Doctor.Who.S01E02.The.End.of.the.World.1080p.BluRay.x264-GRP.mkv",
		"Doctor.Who.S01E03.The.Unquiet.Dead.1080p.BluRay.x264-GRP.mkv"} {
		share.File(pack+"/"+name, []byte(name), monday)
	}
	share.Dir(pack, monday)
	server := httptest.NewServer(share)
	t.Cleanup(server.Close)
	h.share(admin, "Zurg", server.URL+"/dav")
	shows := h.addLibrary(admin, "shows", "shows", "shows")["id"].(string)
	if err := h.server.Library.Scan(context.Background(), shows, true); err != nil {
		t.Fatal(err)
	}
	who, err := h.app.FindFirstRecordByData("shows", "title", "Doctor Who")
	if err != nil {
		t.Fatal(err)
	}
	if who.GetInt("tmdb_id") != 57243 || who.GetString("match_state") != "matched" {
		t.Fatalf("Doctor Who is %d, %s", who.GetInt("tmdb_id"), who.GetString("match_state"))
	}
	detail := h.call(http.MethodGet, "/api/foyer/admin/library/shows/"+who.Id, admin, nil).body
	identity, _ := detail["identity"].(map[string]any)
	hits := map[float64]float64{}
	for _, c := range identity["candidates"].([]any) {
		hits[c.(map[string]any)["id"].(float64)], _ = c.(map[string]any)["titleHits"].(float64)
	}
	if identity["reason"] != "episode titles" || hits[57243] != 3 || hits[121] != 0 {
		t.Errorf("why: %v", identity)
	}
}
