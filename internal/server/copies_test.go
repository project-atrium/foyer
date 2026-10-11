package server_test

import (
	"archive/zip"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"foyer/internal/adapters/webdav/webdavtest"
	"foyer/internal/copies"
)

// A film kept smaller: its options say what a copy made to the device's
// settings would come to, then the file as it is; asking for the copy
// answers how far along it is until it is made, then its address — fetched
// by ranges, under a playing a Stop ends — and anyone else asking for the
// same is handed the same copy. A backup leaves the copies out, and nothing
// of their addresses reaches the log.
func TestADownloadIsMadeSmallerOnFoyer(t *testing.T) {
	mkv := remuxable(t)
	h := start(t, options{})
	h.app.Settings().Logs.MaxDays = 7
	admin := h.superuser()
	share := webdavtest.New()
	share.AsZurg = true
	share.File("movies/Some.Film.2022.1080p.BluRay.x264-GRP/Some.Film.2022.1080p.BluRay.x264-GRP.mkv", mkv, monday)
	share.Dir("movies/Some.Film.2022.1080p.BluRay.x264-GRP", monday)
	zurg := httptest.NewServer(share)
	t.Cleanup(zurg.Close)
	h.share(admin, "Zurg", zurg.URL+"/dav")
	movies := h.addLibrary(admin, "movies", "movies", "movies")["id"].(string)
	if err := h.server.Library.Scan(context.Background(), movies, true); err != nil {
		t.Fatal(err)
	}
	if got := h.call(http.MethodPut, "/api/foyer/admin/adapters/ffmpeg", admin, map[string]any{"enabled": true, "values": map[string]any{"copy_effort": "fast"}}); got.status != http.StatusOK {
		t.Fatalf("ffmpeg: %d %v", got.status, got.body)
	}
	if err := h.server.Streams.StartTwin(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.server.Streams.StopTwin(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	running := make(chan struct{})
	go func() { h.server.Copies.Run(ctx); close(running) }()
	t.Cleanup(func() { cancel(); h.server.Copies.Close(); <-running })
	alex := h.makeUser("alex", map[string]any{"name": "Alex"})
	film, err := h.app.FindFirstRecordByData("movies", "title", "Some Film")
	if err != nil {
		t.Fatal(err)
	}
	device := deviceHeader(t, "phone-of-alex-1", "Alex’s phone")

	encode := map[string]any{"video": "av1", "preference": "size", "maxHeight": 240, "maxBitrate": 2_000_000, "hdr": false, "audio": "opus", "container": "mkv"}
	asked := h.callFrom(device, http.MethodPost, "/api/foyer/download/options", alex.token, map[string]any{"collection": "movies", "id": film.Id, "encode": encode})
	if asked.status != http.StatusOK {
		t.Fatalf("options: %d %v", asked.status, asked.body)
	}
	fits(t, "options", asked.body, fixtureAnswer(t, "routes/download-options.json"))
	offered := asked.body["options"].([]any)
	smaller, original := offered[0].(map[string]any), offered[len(offered)-1].(map[string]any)
	if len(offered) != 2 || smaller["id"] != "copy.av1.size.240.2000000.sdr.opus.mkv" || smaller["transcoded"] != true || smaller["height"] != 240.0 ||
		smaller["width"] != 426.0 || original["id"] != copies.Original || original["transcoded"] != false || original["height"] != 360.0 {
		t.Fatalf("offered %v", offered)
	}
	if smaller["estimatedBytes"].(float64) >= original["estimatedBytes"].(float64) {
		t.Errorf("a copy of %v bytes for a file of %v", smaller["estimatedBytes"], original["estimatedBytes"])
	}
	// The file as it is, where nothing is asked to be made.
	if plain := h.call(http.MethodPost, "/api/foyer/download/options", alex.token, map[string]any{"collection": "movies", "id": film.Id}); len(plain.body["options"].([]any)) != 1 {
		t.Errorf("no settings: %v", plain.body)
	}
	if wrong := h.call(http.MethodPost, "/api/foyer/download", alex.token, map[string]any{"collection": "movies", "id": film.Id, "option": "copy.vp9"}); wrong.status != http.StatusBadRequest {
		t.Errorf("an option Foyer never offered: %d", wrong.status)
	}

	body := map[string]any{"collection": "movies", "id": film.Id, "option": smaller["id"]}
	first := h.callFrom(device, http.MethodPost, "/api/foyer/download", alex.token, body)
	if first.status != http.StatusAccepted {
		t.Fatalf("download: %d %v", first.status, first.body)
	}
	fits(t, "preparing", first.body, fixtureAnswer(t, "routes/download-preparing.json"))
	var made answer
	deadline := time.Now().Add(2 * time.Minute)
	for {
		made = h.callFrom(device, http.MethodPost, "/api/foyer/download", alex.token, body)
		if made.status == http.StatusOK {
			break
		}
		if made.status != http.StatusAccepted || time.Now().After(deadline) {
			t.Fatalf("waiting for the copy: %d %v", made.status, made.body)
		}
		time.Sleep(200 * time.Millisecond)
	}
	fits(t, "made", made.body, fixtureAnswer(t, "routes/download-made.json"))
	address := made.body["url"].(string)
	version := made.body["version"].(map[string]any)
	video := version["video"].(map[string]any)
	if !strings.HasPrefix(address, copies.Prefix) || video["codec"] != "av1" || video["height"] != 240.0 || len(version["audio"].([]any)) != 2 || version["probed"] != true {
		t.Fatalf("made %q %v", address, version)
	}
	size := int64(version["size"].(float64))

	status, header, part := h.fetchRange(address, "bytes=0-99")
	if status != http.StatusPartialContent || len(part) != 100 || header.Get("Content-Range") != "bytes 0-99/"+strconv.FormatInt(size, 10) || header.Get("Content-Type") != "video/x-matroska" {
		t.Fatalf("a part: %d %v %d", status, header, len(part))
	}
	status, _, whole := h.fetchRange(address, "")
	if status != http.StatusOK || int64(len(whole)) != size || !strings.HasPrefix(whole, "\x1a\x45\xdf\xa3") {
		t.Fatalf("the whole: %d %d of %d", status, len(whole), size)
	}
	// Made, it is offered at its size.
	again := h.call(http.MethodPost, "/api/foyer/download/options", alex.token, map[string]any{"collection": "movies", "id": film.Id, "encode": encode})
	if first := again.body["options"].([]any)[0].(map[string]any); int64(first["estimatedBytes"].(float64)) != size {
		t.Errorf("offered once made: %v", first)
	}
	// Someone else asking for the same is handed the same copy, at once.
	kids := h.makeUser("kids", map[string]any{"name": "Kids"})
	if theirs := h.call(http.MethodPost, "/api/foyer/download", kids.token, body); theirs.status != http.StatusOK || int64(theirs.body["version"].(map[string]any)["size"].(float64)) != size {
		t.Errorf("asked by another: %d %v", theirs.status, theirs.body)
	}
	if listed := h.call(http.MethodGet, "/api/foyer/admin/copies", admin, nil); len(listed.body["kept"].([]any)) != 1 ||
		listed.body["kept"].([]any)[0].(map[string]any)["by"] != "Alex" || len(listed.body["making"].([]any)) != 0 {
		t.Errorf("the copies: %v", listed.body)
	}

	// Its download is a playing, on Live Activity: a Stop ends its address.
	var playing map[string]any
	for _, p := range h.call(http.MethodGet, "/api/foyer/admin/sessions", admin, nil).body["sessions"].([]any) {
		if p := p.(map[string]any); p["mode"] == "copy" && p["kind"] == "download" && p["device"].(map[string]any)["name"] == "Alex’s phone" {
			playing = p
		}
	}
	if playing == nil {
		t.Fatal("no playing of the copy")
	}
	if got := h.call(http.MethodPost, "/api/foyer/admin/sessions/"+playing["id"].(string)+"/stop", admin, nil); got.status != http.StatusNoContent {
		t.Fatalf("stop: %d", got.status)
	}
	if status, _, _ := h.fetchRange(address, "bytes=0-99"); status != http.StatusForbidden {
		t.Errorf("a stopped copy's address: %d", status)
	}
	if status, _, _ := h.fetchRange(strings.Replace(address, "sig=", "sig=x", 1), ""); status != http.StatusForbidden {
		t.Errorf("a forged address: %d", status)
	}

	// A backup leaves the copies out.
	if err := h.app.CreateBackup(context.Background(), "test.zip"); err != nil {
		t.Fatal(err)
	}
	backup, err := zip.OpenReader(filepath.Join(h.app.DataDir(), "backups", "test.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	for _, f := range backup.File {
		if strings.HasPrefix(f.Name, copies.DirName) {
			t.Errorf("the backup holds %s", f.Name)
		}
	}

	// Nothing under the prefix in the log, refusals included.
	h.call(http.MethodGet, "/api/foyer/info?control=1", "", nil)
	deadline = time.Now().Add(8 * time.Second)
	for {
		var rows []struct {
			Data string `db:"data"`
		}
		if err := h.app.LogQuery().Select("data").All(&rows); err != nil {
			t.Fatal(err)
		}
		control := false
		for _, r := range rows {
			if strings.Contains(r.Data, "/api/foyer/stream/") {
				t.Fatalf("a stream address in the log: %s", r.Data)
			}
			control = control || strings.Contains(r.Data, "control=1")
		}
		if control {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("PocketBase wrote no log in time to compare against")
		}
		time.Sleep(200 * time.Millisecond)
	}
}
