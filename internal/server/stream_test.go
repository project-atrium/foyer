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

func TestAStreamAddressIsItsSignature(t *testing.T) {
	s := scanLibrary(t)
	alex := s.makeUser("alex", nil)
	film := s.title("movies", "Some Film")
	address := s.call(http.MethodPost, "/api/foyer/play", alex.token, map[string]any{"collection": "movies", "id": film.Id}).body["url"].(string)
	u, _ := url.Parse(address)
	q := u.Query()

	tampered := url.Values{"u": {q.Get("u")}, "exp": {q.Get("exp")}, "sig": {"AAAAAAAAAAAAAAAAAAAAAA"}}
	if status, _, _ := s.fetchRange(u.Path+"?"+tampered.Encode(), ""); status != http.StatusForbidden {
		t.Errorf("a forged signature: %d", status)
	}
	later := url.Values{"u": {q.Get("u")}, "exp": {"9999999999"}, "sig": {q.Get("sig")}}
	if status, _, _ := s.fetchRange(u.Path+"?"+later.Encode(), ""); status != http.StatusForbidden {
		t.Errorf("a longer life: %d", status)
	}
	stale, err := s.server.Streams.Address(strings.TrimPrefix(u.Path, "/api/foyer/stream/"), q.Get("u"), time.Now().Add(-time.Minute))
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
// PocketBase's request log, refused ones included.
func TestNoStreamAddressReachesTheLog(t *testing.T) {
	s := scanLibrary(t)
	// A test app logs nothing; this one logs as a server does.
	s.app.Settings().Logs.MaxDays = 7
	alex := s.makeUser("alex", nil)
	film := s.title("movies", "Some Film")
	address := s.call(http.MethodPost, "/api/foyer/play", alex.token, map[string]any{"collection": "movies", "id": film.Id}).body["url"].(string)
	s.fetchRange(address, "bytes=0-1")
	s.fetchRange(address+"x", "")
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
			if strings.Contains(r.Data, "sig=") {
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
