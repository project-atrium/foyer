package server_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"foyer/internal/adapters/webdav/webdavtest"
	"foyer/internal/hls"
)

// remuxable makes forty seconds of a Matroska film AVPlayer cannot open as
// it is: H.264, AC-3 5.1 and AAC, English subtitles.
func remuxable(t *testing.T) []byte {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not on the PATH")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not on the PATH")
	}
	dir := t.TempDir()
	srt := filepath.Join(dir, "subs.srt")
	if err := os.WriteFile(srt, []byte("1\n00:00:01,000 --> 00:00:03,000\nHello there\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "film.mkv")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-i", srt,
		"-t", "40", "-map", "0:v", "-map", "1:a", "-map", "1:a", "-map", "2:s",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "48", "-bf", "2", "-pix_fmt", "yuv420p",
		"-c:a:0", "ac3", "-ac:a:0", "6", "-c:a:1", "aac", "-ac:a:1", "2", "-c:s", "srt",
		"-metadata:s:a:0", "language=eng", "-metadata:s:a:1", "language=deu", "-metadata:s:s:0", "language=eng", out)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, output)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// avPlayer is the built-in player's profile on an iPhone, as the app says it.
var avPlayer = map[string]any{
	"protocols": []string{"progressive", "hls"}, "containers": []string{"mp4", "m4v", "mov"}, "videoCodecs": []string{"h264", "hevc"},
	"audioCodecs": []string{"aac", "mp3", "ac3", "eac3", "alac", "flac"}, "subtitleFormats": []string{"vtt"},
	"hdr": []string{"hdr10", "hlg", "dolby-vision"}, "maxHeight": 2160,
}

// A player that plays the picture and not the file — AVPlayer, a Matroska
// film — is handed it remuxed to HLS, the file probed first where ffprobe
// had not read it: its sounds and subtitles as renditions, its segments on
// its own clock; the playing it belongs to shows as a remux, and its Stop
// ends ffmpeg. Nothing of the addresses reaches the log.
func TestAPlayerThatCannotOpenTheFileGetsItRemuxed(t *testing.T) {
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
	if got := h.call(http.MethodPut, "/api/foyer/admin/adapters/ffmpeg", admin, map[string]any{"enabled": true, "values": map[string]any{}}); got.status != http.StatusOK {
		t.Fatalf("ffmpeg: %d %v", got.status, got.body)
	}
	if err := h.server.Streams.StartTwin(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.server.Streams.StopTwin(context.Background()) })
	alex := h.makeUser("alex", map[string]any{"name": "Alex"})
	film, err := h.app.FindFirstRecordByData("movies", "title", "Some Film")
	if err != nil {
		t.Fatal(err)
	}

	device := deviceHeader(t, "iphone-of-alex-1", "Alex’s iPhone")
	got := h.callFrom(device, http.MethodPost, "/api/foyer/play", alex.token, map[string]any{"collection": "movies", "id": film.Id, "profile": avPlayer, "startMs": 12_500})
	if got.status != http.StatusOK || got.body["mode"] != "remux" {
		t.Fatalf("play: %d %v", got.status, got.body)
	}
	fits(t, "playback", got.body, fixtureAnswer(t, "routes/play-remux.json"))
	master, file := got.body["url"].(string), got.body["file"].(string)
	if !strings.HasPrefix(master, hls.Prefix) || !strings.HasSuffix(master, "/main.m3u8") || !strings.HasPrefix(file, "/api/foyer/stream/") || strings.HasPrefix(file, hls.Prefix) {
		t.Fatalf("the addresses %q %q", master, file)
	}
	remux := got.body["remux"].(map[string]any)
	if audio := remux["audio"].([]any); len(audio) != 2 || audio[0] != "ac3" || audio[1] != "ac3" || len(remux["subtitles"].([]any)) != 1 {
		t.Fatalf("the remux %v", remux)
	}
	// ffprobe read the file for it: the version is its word now.
	if version := got.body["version"].(map[string]any); version["probed"] != true || len(version["audio"].([]any)) != 2 {
		t.Fatalf("the version %v", version)
	}
	// The same player asking for an MP4 it opens is handed the file.
	plain := h.call(http.MethodPost, "/api/foyer/play", alex.token, map[string]any{"collection": "movies", "id": film.Id})
	if plain.body["mode"] != nil || strings.HasPrefix(plain.body["url"].(string), hls.Prefix) {
		t.Errorf("without a profile: %v", plain.body)
	}

	base := strings.TrimSuffix(master, "main.m3u8")
	status, header, body := h.fetch(master)
	if status != http.StatusOK || header.Get("Content-Type") != "application/vnd.apple.mpegurl" || !bytes.Contains(body, []byte(`,ac-3"`)) || !bytes.Contains(body, []byte(`TYPE=SUBTITLES`)) {
		t.Fatalf("the master playlist: %d %q\n%s", status, header.Get("Content-Type"), body)
	}
	for _, part := range []string{"v.m3u8", "a1.m3u8", "s0.m3u8", "v.mp4", "a0.mp4", "v2.m4s", "a1-2.m4s", "s0-0.vtt"} {
		status, header, body := h.fetch(base + part)
		if status != http.StatusOK || len(body) == 0 {
			t.Fatalf("%s: %d %s", part, status, body)
		}
		switch {
		case strings.HasSuffix(part, ".m4s") && string(body[4:8]) != "moof":
			t.Errorf("%s begins %q", part, body[4:8])
		case strings.HasSuffix(part, ".mp4") && string(body[4:8]) != "ftyp":
			t.Errorf("%s begins %q", part, body[4:8])
		case part == "s0-0.vtt" && !bytes.Contains(body, []byte("Hello there")):
			t.Errorf("the subtitles %s", body)
		case strings.HasPrefix(part, "a") && strings.HasSuffix(part, ".mp4") && header.Get("Content-Type") != "audio/mp4":
			t.Errorf("%s served as %q", part, header.Get("Content-Type"))
		}
	}
	if status, _, _ := h.fetch(base + "v999.m4s"); status != http.StatusNotFound {
		t.Errorf("a segment past the end: %d", status)
	}

	// Live Activity: Alex's iPhone, remuxed, at ffmpeg's speed.
	var playing map[string]any
	waitFor(t, "the remux's speed", func() bool {
		for _, p := range h.call(http.MethodGet, "/api/foyer/admin/sessions", admin, nil).body["sessions"].([]any) {
			if p := p.(map[string]any); p["mode"] == "remux" {
				playing = p
				return p["speed"] != nil
			}
		}
		return false
	})
	if playing["device"].(map[string]any)["name"] != "Alex’s iPhone" {
		t.Errorf("the playing %v", playing)
	}
	if h.server.Remux.Active() != 1 {
		t.Errorf("%d remuxes", h.server.Remux.Active())
	}
	if got := h.call(http.MethodPost, "/api/foyer/admin/sessions/"+playing["id"].(string)+"/stop", admin, nil); got.status != http.StatusNoContent {
		t.Fatalf("stop: %d", got.status)
	}
	if status, _, _ := h.fetch(base + "v3.m4s"); status != http.StatusForbidden {
		t.Errorf("a stopped remux's segment: %d", status)
	}
	waitFor(t, "the remux ended with its playing", func() bool { return h.server.Remux.Active() == 0 && h.server.Remux.Running() == 0 })

	// Nothing under the prefix in the log, refusals included.
	h.call(http.MethodGet, "/api/foyer/info?control=1", "", nil)
	deadline := time.Now().Add(8 * time.Second)
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
