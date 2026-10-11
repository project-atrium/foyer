package server_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/tools/security"

	"foyer/internal/library"
)

// fetchRange asks the server for a part of an address, as a player does.
func (h *harness) fetchRange(address, byteRange string) (int, http.Header, string) {
	h.t.Helper()
	request := httptest.NewRequest(http.MethodGet, address, nil)
	if byteRange != "" {
		request.Header.Set("Range", byteRange)
	}
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	body, _ := io.ReadAll(recorder.Result().Body)
	return recorder.Code, recorder.Header(), string(body)
}

func TestAUserPlaysATitleThroughFoyer(t *testing.T) {
	s := scanLibrary(t)
	alex := s.makeUser("alex", nil)
	film := s.title("movies", "Some Film")
	got := s.call(http.MethodPost, "/api/foyer/play", alex.token, map[string]any{"collection": "movies", "id": film.Id})
	if got.status != http.StatusOK {
		t.Fatalf("play: %d %v", got.status, got.body)
	}
	address := got.body["url"].(string)
	version := got.body["version"].(map[string]any)
	if !strings.HasPrefix(address, "/api/foyer/stream/") || version["quality"] != "4k" || len(got.body["versions"].([]any)) != 2 {
		t.Fatalf("playback %v", got.body)
	}
	// As the app reads it, and never where on the share the file lies.
	fits(t, "playback", got.body, fixtureAnswer(t, "routes/play.json"))
	for _, v := range append([]any{version}, got.body["versions"].([]any)...) {
		if _, ok := v.(map[string]any)["path"]; ok {
			t.Errorf("a version tells its path: %v", v)
		}
	}
	// Zurg's copy of the 4K file is "2160p": its bytes come through Foyer.
	status, header, body := s.fetchRange(address, "bytes=1-3")
	if status != http.StatusPartialContent || body != "160" || header.Get("Content-Range") != "bytes 1-3/5" {
		t.Fatalf("range: %d %q %v", status, body, header)
	}
	if status, _, body := s.fetchRange(address, ""); status != http.StatusOK || body != "2160p" {
		t.Fatalf("whole: %d %q", status, body)
	}
	// Another version, by its file.
	other := got.body["versions"].([]any)[1].(map[string]any)["file"].(string)
	picked := s.call(http.MethodPost, "/api/foyer/play", alex.token, map[string]any{"collection": "movies", "id": film.Id, "file": other})
	if picked.status != http.StatusOK || picked.body["version"].(map[string]any)["file"] != other {
		t.Fatalf("picking a version: %d %v", picked.status, picked.body)
	}
}

// A download is the file as it is, at an address asked for afresh each
// time: a copy that stopped asks again, and carries on from the bytes it has.
func TestADownloadAsksAgainAndCarriesOn(t *testing.T) {
	s := scanLibrary(t)
	alex := s.makeUser("alex", nil)
	film := s.title("movies", "Some Film")
	got := s.call(http.MethodPost, "/api/foyer/download", alex.token, map[string]any{"collection": "movies", "id": film.Id})
	if got.status != http.StatusOK {
		t.Fatalf("download: %d %v", got.status, got.body)
	}
	fits(t, "download", got.body, fixtureAnswer(t, "routes/download.json"))
	version := got.body["version"].(map[string]any)
	if _, ok := version["path"]; ok || version["quality"] != "4k" {
		t.Errorf("the version %v", version)
	}
	// Zurg's copy is "2160p": two bytes kept, the rest asked for.
	status, header, body := s.fetchRange(got.body["url"].(string), "bytes=2-")
	if status != http.StatusPartialContent || body != "60p" || header.Get("Content-Range") != "bytes 2-4/5" {
		t.Errorf("carrying on: %d %q %v", status, body, header)
	}
	// An episode, and a series' extra by its file.
	pack := s.title("shows", "Big Pack")
	episode := s.episode(pack, 1, 2)
	if got := s.call(http.MethodPost, "/api/foyer/download", alex.token, map[string]any{"collection": "shows_episodes", "id": episode.Id}); got.status != http.StatusOK {
		t.Errorf("an episode: %d %v", got.status, got.body)
	}
	if got := s.call(http.MethodPost, "/api/foyer/download", alex.token, map[string]any{"collection": "shows", "id": pack.Id, "file": s.extras(pack)[0]}); got.status != http.StatusOK {
		t.Errorf("an extra: %d %v", got.status, got.body)
	}
	// Nothing for a guess, or for a stranger.
	twin := s.title("movies", "Twin Title")
	if got := s.call(http.MethodPost, "/api/foyer/download", alex.token, map[string]any{"collection": "movies", "id": twin.Id}); got.status != http.StatusNotFound {
		t.Errorf("a guess: %d", got.status)
	}
	if got := s.call(http.MethodPost, "/api/foyer/download", "", map[string]any{"collection": "movies", "id": film.Id}); got.status != http.StatusUnauthorized {
		t.Errorf("a guest: %d", got.status)
	}
}

// A series' extras play by their file: the film in its pack, and an OVA
// TMDB has no episode for.
func TestASeriesExtrasPlay(t *testing.T) {
	s := scanLibrary(t)
	alex := s.makeUser("alex", nil)
	pack := s.title("shows", "Big Pack")
	var extras []map[string]any
	_ = pack.UnmarshalJSONField("extras", &extras)
	if len(extras) != 2 {
		t.Fatalf("extras %v", extras)
	}
	for _, x := range extras {
		got := s.call(http.MethodPost, "/api/foyer/play", alex.token, map[string]any{"collection": "shows", "id": pack.Id, "file": x["file"]})
		if got.status != http.StatusOK || got.body["version"].(map[string]any)["file"] != x["file"] {
			t.Fatalf("%s: %d %v", x["kind"], got.status, got.body)
		}
		status, _, body := s.fetchRange(got.body["url"].(string), "")
		if status != http.StatusOK || !strings.HasSuffix(body, ".mkv") {
			t.Errorf("%s's bytes: %d %q", x["kind"], status, body)
		}
	}
	// A series itself has no version to pick without a file, nor a file of
	// another title.
	if got := s.call(http.MethodPost, "/api/foyer/play", alex.token, map[string]any{"collection": "shows", "id": pack.Id}); got.status != http.StatusNotFound {
		t.Errorf("a series without a file: %d", got.status)
	}
	film := s.title("movies", "Some Film")
	if got := s.call(http.MethodPost, "/api/foyer/play", alex.token, map[string]any{"collection": "movies", "id": film.Id, "file": extras[0]["file"]}); got.status != http.StatusNotFound {
		t.Errorf("another title's file: %d", got.status)
	}
}

func TestAStreamAddressIsItsSignature(t *testing.T) {
	s := scanLibrary(t)
	alex := s.makeUser("alex", nil)
	film := s.title("movies", "Some Film")
	address := s.call(http.MethodPost, "/api/foyer/play", alex.token, map[string]any{"collection": "movies", "id": film.Id}).body["url"].(string)
	u, _ := url.Parse(address)
	q := u.Query()

	tampered := url.Values{"u": {q.Get("u")}, "s": {q.Get("s")}, "exp": {q.Get("exp")}, "sig": {"AAAAAAAAAAAAAAAAAAAAAA"}}
	if status, _, _ := s.fetchRange(u.Path+"?"+tampered.Encode(), ""); status != http.StatusForbidden {
		t.Errorf("a forged signature: %d", status)
	}
	later := url.Values{"u": {q.Get("u")}, "s": {q.Get("s")}, "exp": {"9999999999"}, "sig": {q.Get("sig")}}
	if status, _, _ := s.fetchRange(u.Path+"?"+later.Encode(), ""); status != http.StatusForbidden {
		t.Errorf("a longer life: %d", status)
	}
	// The signature holds the playing too: another's id is no address.
	other := url.Values{"u": {q.Get("u")}, "s": {"someotherplay00"}, "exp": {q.Get("exp")}, "sig": {q.Get("sig")}}
	if status, _, _ := s.fetchRange(u.Path+"?"+other.Encode(), ""); status != http.StatusForbidden {
		t.Errorf("another playing's id: %d", status)
	}
	stale, err := s.server.Streams.Address(strings.TrimPrefix(u.Path, "/api/foyer/stream/"), q.Get("u"), q.Get("s"), time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if status, _, _ := s.fetchRange(stale, ""); status != http.StatusForbidden {
		t.Errorf("an expired address: %d", status)
	}
	// A new file-token secret for users ends every address at once.
	users, _ := s.app.FindCollectionByNameOrId("users")
	users.FileToken.Secret = security.RandomString(50)
	if err := s.app.Save(users); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := s.fetchRange(address, ""); status != http.StatusForbidden {
		t.Errorf("after a new secret: %d", status)
	}
}

func TestNothingPlaysThatAUserMayNotSee(t *testing.T) {
	s := scanLibrary(t)
	alex := s.makeUser("alex", nil)
	twin := s.title("movies", "Twin Title")
	if got := s.call(http.MethodPost, "/api/foyer/play", alex.token, map[string]any{"collection": "movies", "id": twin.Id}); got.status != http.StatusNotFound {
		t.Errorf("a guess: %d", got.status)
	}
	if got := s.call(http.MethodPost, "/api/foyer/play", "", map[string]any{"collection": "movies", "id": twin.Id}); got.status != http.StatusUnauthorized {
		t.Errorf("a guest: %d", got.status)
	}
	if got := s.call(http.MethodPost, "/api/foyer/play", alex.token, map[string]any{"collection": "media_files", "id": "x"}); got.status != http.StatusNotFound {
		t.Errorf("a collection that is no library's: %d", got.status)
	}
}

// A stream address opens the file to whoever holds it: none may land in
// PocketBase's request log — nothing under the prefix, refused ones and a
// stopped playing's included.
func TestNoStreamAddressReachesTheLog(t *testing.T) {
	s := scanLibrary(t)
	// A test app logs nothing; this one logs as a server does.
	s.app.Settings().Logs.MaxDays = 7
	alex := s.makeUser("alex", nil)
	film := s.title("movies", "Some Film")
	play := s.call(http.MethodPost, "/api/foyer/play", alex.token, map[string]any{"collection": "movies", "id": film.Id})
	address := play.body["url"].(string)
	s.fetchRange(address, "bytes=0-1")
	s.fetchRange(address+"x", "")
	s.call(http.MethodGet, "/api/foyer/admin/sessions", s.admin, nil)
	s.call(http.MethodPost, "/api/foyer/admin/sessions/"+play.body["session"].(string)+"/stop", s.admin, nil)
	if status, _, _ := s.fetchRange(address, ""); status != http.StatusForbidden {
		t.Fatalf("a stopped playing's address: %d", status)
	}
	download := s.call(http.MethodPost, "/api/foyer/download", alex.token, map[string]any{"collection": "movies", "id": film.Id}).body["url"].(string)
	s.fetchRange(download, "")
	s.call(http.MethodGet, "/api/foyer/info?control=1", "", nil)
	deadline := time.Now().Add(8 * time.Second)
	for {
		var rows []struct {
			Data string `db:"data"`
		}
		if err := s.app.LogQuery().Select("data").All(&rows); err != nil {
			t.Fatal(err)
		}
		control := false
		for _, r := range rows {
			if strings.Contains(r.Data, "/api/foyer/stream/") || strings.Contains(r.Data, "sig=") {
				t.Fatalf("a stream address in the log: %s", r.Data)
			}
			control = control || strings.Contains(r.Data, "control=1")
		}
		if control {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("PocketBase wrote no log in time to compare against")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestTheLoopbackServesAToolOneFileForAWhile(t *testing.T) {
	s := scanLibrary(t)
	if err := s.server.Streams.StartTwin(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.server.Streams.StopTwin(t.Context()) })
	file, err := s.app.FindFirstRecordByFilter(library.FilesCollection, "name ~ '2160p'")
	if err != nil {
		t.Fatal(err)
	}
	address, revoke, err := s.server.Streams.Grant(file.Id, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(address, "http://127.0.0.1:") {
		t.Fatalf("not a loopback: %s", address)
	}
	response, err := http.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "2160p" {
		t.Fatalf("%d %q", response.StatusCode, body)
	}
	revoke()
	if response, err := http.Get(address); err != nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("after revoking: %v %v", response, err)
	}
}
