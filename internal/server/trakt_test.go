package server_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/trakt"
	"foyer/internal/trakt/trakttest"
	"foyer/internal/watch"
)

// state is a user's watch status for an identity, as Foyer keeps it.
func (h *harness) state(t *testing.T, user, identity string) *core.Record {
	t.Helper()
	r, err := h.app.FindFirstRecordByFilter(watch.Collection, "user = {:u} && identity = {:i}", dbx.Params{"u": user, "i": identity})
	if err != nil {
		return nil
	}
	return r
}

// played is a user watching here, as a player's reports would leave it.
func (h *harness) played(t *testing.T, user, identity string, watched bool, plays int, at time.Time) {
	t.Helper()
	r := h.state(t, user, identity)
	if r == nil {
		c, err := h.app.FindCollectionByNameOrId(watch.Collection)
		if err != nil {
			t.Fatal(err)
		}
		r = core.NewRecord(c)
		r.Set("user", user)
		r.Set("identity", identity)
	}
	r.Set("watched", watched)
	r.Set("play_count", plays)
	r.Set("last_played_at", at)
	if err := h.app.Save(r); err != nil {
		t.Fatal(err)
	}
}

// A user connects their own Trakt with a code typed on Trakt's site. Then
// Trakt's history comes in and what it lacks goes; every change after, on
// either side, reaches the other and is never sent back; sent only, nothing
// of Trakt's comes in. A refused refresh is said, never a 401; letting go
// revokes the token.
func TestAUsersTraktIsKeptInStepBothWays(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	tr := trakttest.New(t, "app-id", "app-secret")
	tr.Interval = 1
	h.server.Trakt.Site = tr.URL
	alex := h.makeUser("alex", map[string]any{"name": "Alex"})
	ctx := context.Background()
	view := func() map[string]any {
		t.Helper()
		got := h.call(http.MethodGet, "/api/foyer/me/integrations", alex.token, nil)
		if got.status != http.StatusOK {
			t.Fatalf("integrations: %d %v", got.status, got.body)
		}
		return got.body["integrations"].([]any)[0].(map[string]any)
	}
	sync := func() {
		t.Helper()
		// Due now, as ten minutes on would make it.
		if got := h.call(http.MethodPost, "/api/foyer/me/integrations/trakt/mode", alex.token, map[string]any{"mode": view()["mode"]}); got.status != http.StatusOK {
			t.Fatalf("mode: %d %v", got.status, got.body)
		}
		h.server.Trakt.Tick(ctx)
	}

	if v := view(); v["available"] != false || v["state"] != nil {
		t.Fatalf("before Trakt is set up: %v", v)
	}
	if got := h.call(http.MethodPost, "/api/foyer/me/integrations/trakt/connect", alex.token, nil); got.status != http.StatusConflict {
		t.Fatalf("connecting before: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodPut, "/api/foyer/admin/adapters/trakt", admin, map[string]any{"enabled": true, "values": map[string]any{"clientId": "app-id", "clientSecret": "app-secret"}}); got.status != http.StatusOK {
		t.Fatalf("setting Trakt up: %d %v", got.status, got.body)
	}

	// Watched elsewhere — Jellyfin's plugin fed Trakt for years — and here.
	matrix, inception, fightClub := "tmdb:movie:603", "tmdb:movie:27205", "tmdb:movie:550"
	pilot := "tmdb:tv:1396/s1e1"
	then := time.Date(2025, 3, 1, 20, 0, 0, 0, time.UTC)
	here := time.Date(2026, 10, 1, 21, 0, 0, 0, time.UTC)
	tr.AddUser("Alex")
	tr.Watch("Alex", 603, 2, then)
	tr.WatchEpisode("Alex", trakttest.Episode{Show: 1396, Season: 1, Number: 1}, 1, then)
	h.played(t, alex.id, inception, true, 1, here)
	h.played(t, alex.id, "title:movie:a home video:2020", true, 1, here)

	connecting := h.call(http.MethodPost, "/api/foyer/me/integrations/trakt/connect", alex.token, nil)
	fits(t, "connecting", connecting.body, fixtureAnswer(t, "routes/integration-connecting.json"))
	code, _ := connecting.body["code"].(map[string]any)
	if connecting.status != http.StatusOK || connecting.body["state"] != "connecting" || code["verificationUrl"] != "https://trakt.tv/activate" || code["userCode"] == "" {
		t.Fatalf("connecting: %d %v", connecting.status, connecting.body)
	}
	if again := h.call(http.MethodPost, "/api/foyer/me/integrations/trakt/connect", alex.token, nil); again.body["code"].(map[string]any)["userCode"] != code["userCode"] {
		t.Errorf("a second code while the first is good: %v", again.body)
	}
	h.server.Trakt.Tick(ctx)
	if v := view(); v["state"] != "connecting" {
		t.Fatalf("before the code is entered: %v", v)
	}
	tr.Approve(code["userCode"].(string), "Alex")
	time.Sleep(1100 * time.Millisecond)
	h.server.Trakt.Tick(ctx)
	if v := view(); v["state"] != "connected" || v["account"] != "Alex" || v["mode"] != "two-way" || v["code"] != nil || v["lastSync"] == nil {
		t.Fatalf("connected: %v", v)
	}
	fits(t, "integrations", h.call(http.MethodGet, "/api/foyer/me/integrations", alex.token, nil).body, fixtureAnswer(t, "routes/integrations.json"))
	// Trakt's history came in, as often and when; what it lacked went, at when Foyer heard; a title by its name stayed.
	if s := h.state(t, alex.id, matrix); s == nil || !s.GetBool("watched") || s.GetInt("play_count") != 2 || !s.GetDateTime("last_played_at").Time().Equal(then) {
		t.Errorf("the Matrix: %v", s)
	}
	if s := h.state(t, alex.id, pilot); s == nil || !s.GetBool("watched") {
		t.Errorf("the pilot: %v", s)
	}
	if posts := tr.Posts(); strings.Join(posts, ";") != "add movie 27205 at 2026-10-01T21:00:00Z" {
		t.Errorf("sent %q", posts)
	}
	// Read again with nothing changed, nothing goes.
	sync()
	if posts := tr.Posts(); len(posts) != 1 {
		t.Errorf("sent again %q", posts)
	}

	// Watched again here; taken out there; unwatched here.
	h.played(t, alex.id, matrix, true, 3, here.Add(time.Hour))
	tr.Unwatch("Alex", 27205)
	h.played(t, alex.id, pilot, false, 1, here)
	sync()
	if posts := tr.Posts()[1:]; strings.Join(posts, ";") != "add movie 603 at 2026-10-01T22:00:00Z;remove episode 1396 s1e1" {
		t.Errorf("sent %q", posts)
	}
	if s := h.state(t, alex.id, inception); s.GetBool("watched") || s.GetInt("play_count") != 1 {
		t.Errorf("Inception, taken out on Trakt: %v", s)
	}
	if tr.History("Alex")[603].Plays != 3 || tr.EpisodePlays("Alex", trakttest.Episode{Show: 1396, Season: 1, Number: 1}) != 0 {
		t.Errorf("Trakt: %v", tr.History("Alex"))
	}

	// Sent only: Trakt's new play stays there; a play here still goes.
	if got := h.call(http.MethodPost, "/api/foyer/me/integrations/trakt/mode", alex.token, map[string]any{"mode": "send"}); got.status != http.StatusOK || got.body["mode"] != "send" {
		t.Fatalf("send only: %d %v", got.status, got.body)
	}
	tr.Watch("Alex", 550, 1, here)
	h.played(t, alex.id, inception, true, 2, here.Add(2*time.Hour))
	h.server.Trakt.Tick(ctx)
	if s := h.state(t, alex.id, fightClub); s != nil && s.GetBool("watched") {
		t.Errorf("Fight Club came in: %v", s)
	}
	if posts := tr.Posts(); posts[len(posts)-1] != "add movie 27205 at 2026-10-01T23:00:00Z" {
		t.Errorf("sent %q", posts)
	}
	if got := h.call(http.MethodPost, "/api/foyer/me/integrations/trakt/mode", alex.token, map[string]any{"mode": "sideways"}); got.status != http.StatusBadRequest {
		t.Errorf("a mode that is none: %d", got.status)
	}

	// A day on, the token is refreshed; the app's access taken back, the user connects again.
	tr.Expire()
	sync()
	if v := view(); v["state"] != "connected" || v["error"] != nil {
		t.Errorf("refreshed: %v", v)
	}
	tr.Forget()
	sync()
	if v := view(); v["state"] != "refused" || !strings.Contains(v["error"].(string), "connect again") {
		t.Errorf("refused: %v", v)
	}
	listed := h.call(http.MethodGet, "/api/foyer/admin/integrations", admin, nil).body["integrations"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["user"].(map[string]any)["name"] != "Alex" || listed[0].(map[string]any)["state"] != "refused" {
		t.Errorf("the dashboard's %v", listed)
	}

	// Let go of: the record gone, what was agreed with it too.
	if got := h.call(http.MethodDelete, "/api/foyer/me/integrations/trakt", alex.token, nil); got.status != http.StatusNoContent {
		t.Fatalf("letting go: %d %v", got.status, got.body)
	}
	if v := view(); v["state"] != nil || v["available"] != true {
		t.Errorf("after: %v", v)
	}
	tracked, _ := watch.TrackedBy(h.app, alex.id, trakt.Key)
	if agreed := slices.ContainsFunc(mapValues(tracked), func(t watch.Tracked) bool { return t.Agreed != nil }); agreed {
		t.Error("what was agreed with Trakt kept")
	}
}

func mapValues[K comparable, V any](m map[K]V) []V {
	out := make([]V, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
