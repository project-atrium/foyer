package server_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/identity"
)

// fileEnding is a file of the library by the end of its path.
func (s *scanned) fileEnding(t *testing.T, end string) *core.Record {
	t.Helper()
	files, err := s.app.FindAllRecords("media_files")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f.GetString("path"), end) {
			return f
		}
	}
	t.Fatalf("no file ends %q", end)
	return nil
}

// episodeOf is where a file lies now: S1E3, extra, or what it is.
func (s *scanned) episodeOf(t *testing.T, end string) string {
	t.Helper()
	f := s.fileEnding(t, end)
	if f.GetString("role") != "version" {
		return f.GetString("role")
	}
	ep, err := s.app.FindRecordById(f.GetString("item_collection"), f.GetString("item_id"))
	if err != nil {
		return "nowhere"
	}
	return "S" + strconv.Itoa(ep.GetInt("season")) + "E" + strconv.Itoa(ep.GetInt("episode")) + " " + ep.GetString("mapped_by")
}

// A superuser says what one file of a series is — the episode, or an extra
// — and it holds: through every rescan, and when the file vanishes and comes
// back. Watch status follows the file; "as its name reads" lets go.
func TestOneEpisodeIsCorrectedByHandAndStaysSo(t *testing.T) {
	s := scanLibrary(t)
	ova := "Big Pack OVA - Something Else Entirely.mkv"
	// An OVA TMDB has no episode for: left unplaced, an extra of its series.
	if got := s.episodeOf(t, ova); got != "unmapped" {
		t.Fatalf("before: %s", got)
	}
	alex := s.makeUser("alex", map[string]any{"name": "Alex"})
	// Watched as the extra it was.
	pack := s.title("shows", "Big Pack")
	var extras []map[string]any
	_ = pack.UnmarshalJSONField("extras", &extras)
	extraIdentity := ""
	for _, x := range extras {
		if x["kind"] == "ova" {
			extraIdentity = identity.Extra(pack.GetString("identity"), "ova", x["title"].(string), x["path"].(string))
		}
	}
	if extraIdentity == "" {
		t.Fatalf("the OVA's identity: %v", extras)
	}
	s.played(t, alex.id, extraIdentity, true, 1, time.Date(2026, 9, 1, 20, 0, 0, 0, time.UTC))

	correct := func(end string, body map[string]any) answer {
		t.Helper()
		return s.call(http.MethodPost, "/api/foyer/admin/library/files/"+s.fileEnding(t, end).Id+"/correct", s.admin, body)
	}
	if got := correct(ova, map[string]any{"kind": "episode", "season": 1, "episode": 3}); got.status != http.StatusNoContent {
		t.Fatalf("correcting: %d %v", got.status, got.body)
	}
	if got := s.episodeOf(t, ova); got != "S1E3 manual" {
		t.Fatalf("after: %s", got)
	}
	if ep, err := s.app.FindFirstRecordByFilter("shows_episodes", "show = {:show} && season = 1 && episode = 3", dbx.Params{"show": pack.Id}); err != nil || ep.GetString("title") != "Big 3" {
		t.Errorf("the episode: %v %v", ep, err)
	}
	if state := s.state(alex, "tmdb:tv:2001/s1e3"); state == nil || !state.GetBool("watched") {
		t.Errorf("Alex's watch status did not follow the file")
	}
	detail := s.call(http.MethodGet, "/api/foyer/admin/library/shows/"+pack.Id, s.admin, nil)
	marked := 0
	for _, f := range detail.body["files"].([]any) {
		if c, ok := f.(map[string]any)["corrected"].(map[string]any); ok && c["kind"] == "episode" && c["season"] == 1.0 && c["episode"] == 3.0 {
			marked++
		}
	}
	if marked != 1 {
		t.Errorf("the panel's files: %v", detail.body["files"])
	}

	// A full rescan, then the file gone from Foyer's records and listed again.
	if err := s.server.Library.Scan(context.Background(), s.tvs, true); err != nil {
		t.Fatal(err)
	}
	if got := s.episodeOf(t, ova); got != "S1E3 manual" {
		t.Fatalf("after a rescan: %s", got)
	}
	if err := s.app.Delete(s.fileEnding(t, ova)); err != nil {
		t.Fatal(err)
	}
	if err := s.server.Library.Scan(context.Background(), s.tvs, true); err != nil {
		t.Fatal(err)
	}
	if got := s.episodeOf(t, ova); got != "S1E3 manual" {
		t.Fatalf("come back: %s", got)
	}

	// As its name reads again; a special made an extra.
	if got := correct(ova, map[string]any{"kind": "auto"}); got.status != http.StatusNoContent {
		t.Fatalf("letting go: %d %v", got.status, got.body)
	}
	if got := s.episodeOf(t, ova); got != "unmapped" {
		t.Errorf("let go: %s", got)
	}
	if got := correct("Special - Episode of Somebody.mkv", map[string]any{"kind": "extra"}); got.status != http.StatusNoContent {
		t.Fatalf("an extra: %d %v", got.status, got.body)
	}
	if got := s.episodeOf(t, "Special - Episode of Somebody.mkv"); got != "extra" {
		t.Errorf("the special: %s", got)
	}

	// Never a film's file, nor an episode numbered 0.
	if got := correct("Twin.Title.mkv", map[string]any{"kind": "episode", "season": 1, "episode": 1}); got.status != http.StatusBadRequest {
		t.Errorf("a film: %d", got.status)
	}
	if got := correct(ova, map[string]any{"kind": "episode", "season": 1, "episode": 0}); got.status != http.StatusBadRequest {
		t.Errorf("episode 0: %d", got.status)
	}
	if got := s.call(http.MethodPost, "/api/foyer/admin/library/files/nosuchfile12345/correct", s.admin, map[string]any{"kind": "extra"}); got.status != http.StatusNotFound {
		t.Errorf("no such file: %d", got.status)
	}
}
