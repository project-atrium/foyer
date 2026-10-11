package server_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"foyer/internal/youtube"
)

// theSite pretends to be YouTube's streams and captions: an HLS master with
// a dub and captions, a variant of segments behind a map, a muxed file by
// ranges, and a caption.
func theSite(t *testing.T) (*httptest.Server, []byte) {
	t.Helper()
	file := bytes.Repeat([]byte("0123456789abcdef"), 256)
	site := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/seg.m4s"), strings.HasSuffix(r.URL.Path, "/init.mp4"):
			_, _ = w.Write([]byte("segment " + r.URL.Path))
		case strings.Contains(r.URL.Path, "/hls_variant/"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-INDEPENDENT-SEGMENTS\n" +
				`#EXT-X-MEDIA:URI="https://manifest.googlevideo.com/api/manifest/hls_playlist/itag/233/index.m3u8",TYPE=AUDIO,GROUP-ID="233",LANGUAGE="en",NAME="English",DEFAULT=YES` + "\n" +
				`#EXT-X-STREAM-INF:BANDWIDTH=4000000,CODECS="avc1.640028,mp4a.40.2",RESOLUTION=1920x1080,AUDIO="233"` + "\n" +
				"https://manifest.googlevideo.com/api/manifest/hls_playlist/itag/137/index.m3u8\n"))
		case strings.Contains(r.URL.Path, "/hls_playlist/"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:6\n#EXT-X-PLAYLIST-TYPE:VOD\n" + `#EXT-X-MAP:URI="init.mp4"` + "\n" +
				"#EXTINF:5.0,\nhttps://rr1---sn-test.googlevideo.com/videoplayback/sq/0/seg.m4s\n#EXTINF:5.0,\nsq/1/seg.m4s\n#EXT-X-ENDLIST\n"))
		case r.URL.Path == "/videoplayback":
			http.ServeContent(w, r, "f.mp4", time.Time{}, bytes.NewReader(file))
		case r.URL.Path == "/api/timedtext":
			w.Header().Set("Content-Type", "text/vtt")
			_, _ = w.Write([]byte("WEBVTT\n\n00:00:01.000 --> 00:00:03.000\nHello there\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(site.Close)
	return site, file
}

// fakeYtDlp is a yt-dlp that answers for one video as the real one would,
// its links expiring at expire, and refuses any other as the site would.
func fakeYtDlp(t *testing.T, expire time.Time) string {
	t.Helper()
	dir := t.TempDir()
	answer, err := os.ReadFile("../youtube/testdata/video.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "video.json"), bytes.ReplaceAll(answer, []byte("EXPIRE"), []byte(strconv.FormatInt(expire.Unix(), 10))), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nfor last; do :; done\ncase \"$last\" in\n" +
		"  *x3TJFoQWCHA) cat \"" + filepath.Join(dir, "video.json") + "\" ;;\n" +
		"  *) echo 'ERROR: [youtube] dQw4w9WgXcR: Video unavailable. This video is no longer available' >&2; exit 1 ;;\nesac\n"
	tool := filepath.Join(dir, "yt-dlp")
	if err := os.WriteFile(tool, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return tool
}

// A video plays through Foyer: yt-dlp says where its streams are, and every
// link the device is handed is Foyer's own and sealed — the master and its
// playlists rewritten, segments, a muxed file by ranges, a caption — under
// the playing it belongs to, which a Stop ends. A video the site will not
// play says why, and nothing of an address reaches the log.
func TestAVideoPlaysThroughFoyer(t *testing.T) {
	h := start(t, options{})
	h.app.Settings().Logs.MaxDays = 7
	admin := h.superuser()
	site, file := theSite(t)
	tool := fakeYtDlp(t, time.Now().Add(5*time.Hour))
	if got := h.call(http.MethodPut, "/api/foyer/admin/adapters/youtube", admin, map[string]any{"enabled": true, "values": map[string]any{"ytdlp": tool}}); got.status != http.StatusOK {
		t.Fatalf("youtube: %d %v", got.status, got.body)
	}
	// Every host of the site's is the pretend site.
	h.server.YouTube.Relay.Client.Transport = &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", site.Listener.Addr().String())
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	alex := h.makeUser("alex", map[string]any{"name": "Alex"})
	device := deviceHeader(t, "phone-of-alex-1", "Alex’s phone")

	got := h.callFrom(device, http.MethodPost, "/api/foyer/videos/play", alex.token, map[string]any{"id": "x3TJFoQWCHA", "startMs": 60000})
	if got.status != http.StatusOK {
		t.Fatalf("play: %d %v", got.status, got.body)
	}
	fits(t, "video playback", got.body, fixtureAnswer(t, "routes/video-play.json"))
	sources := got.body["sources"].([]any)
	hls, muxed := sources[0].(map[string]any), sources[1].(map[string]any)
	if len(sources) != 2 || hls["protocol"] != "hls" || muxed["protocol"] != "progressive" || muxed["height"] != 360.0 || muxed["videoCodec"] != "h264" {
		t.Fatalf("sources %v", sources)
	}
	if subtitles := got.body["subtitles"].([]any); len(subtitles) != 2 || subtitles[1].(map[string]any)["auto"] != true {
		t.Errorf("subtitles %v", subtitles)
	}
	if got.body["durationMs"] != 5731000.0 || got.body["live"] != false || len(got.body["chapters"].([]any)) != 2 {
		t.Errorf("the playback %v", got.body)
	}
	for _, s := range sources {
		if address := s.(map[string]any)["url"].(string); !strings.HasPrefix(address, youtube.Prefix) {
			t.Errorf("a source outside Foyer: %s", address)
		}
	}

	// The master, rewritten: nothing of the site's in it.
	status, header, master := h.fetch(hls["url"].(string))
	if status != http.StatusOK || header.Get("Content-Type") != "application/vnd.apple.mpegurl" || bytes.Contains(master, []byte("googlevideo")) {
		t.Fatalf("the master: %d %q\n%s", status, header.Get("Content-Type"), master)
	}
	var variant string
	for _, line := range strings.Split(string(master), "\n") {
		if strings.HasPrefix(line, youtube.Prefix) {
			variant = line
		}
	}
	status, _, media := h.fetch(variant)
	if status != http.StatusOK || bytes.Contains(media, []byte("googlevideo")) || bytes.Count(media, []byte("/x/")) != 3 {
		t.Fatalf("the variant: %d\n%s", status, media)
	}
	var segments []string
	for _, line := range strings.Split(string(media), "\n") {
		if strings.HasPrefix(line, youtube.Prefix) {
			segments = append(segments, line)
		}
	}
	for i, want := range []string{"segment /videoplayback/sq/0/seg.m4s", "segment /api/manifest/hls_playlist/itag/137/sq/1/seg.m4s"} {
		if status, _, body := h.fetch(segments[i]); status != http.StatusOK || string(body) != want {
			t.Errorf("segment %d: %d %q", i, status, body)
		}
	}
	// The muxed file by ranges, as a player seeks it.
	status, header, part := h.fetchRange(muxed["url"].(string), "bytes=16-31")
	if status != http.StatusPartialContent || part != string(file[16:32]) || header.Get("Content-Range") != "bytes 16-31/"+strconv.Itoa(len(file)) {
		t.Errorf("a part: %d %v %q", status, header, part)
	}
	caption := got.body["subtitles"].([]any)[0].(map[string]any)["url"].(string)
	if status, _, body := h.fetch(caption); status != http.StatusOK || !strings.HasPrefix(string(body), "WEBVTT") {
		t.Errorf("the caption: %d %q", status, body)
	}
	// A token changed is no link; a ticket changed is no address.
	if status, _, _ := h.fetch(segments[0][:len(segments[0])-4] + "AAAA"); status != http.StatusNotFound {
		t.Errorf("a changed token: %d", status)
	}
	if status, _, _ := h.fetch(strings.Replace(segments[0], ".", ".x", 1)); status != http.StatusForbidden {
		t.Errorf("a changed ticket: %d", status)
	}

	// Live Activity shows it, relayed, on Alex's phone; a Stop ends its addresses.
	var playing map[string]any
	for _, p := range h.call(http.MethodGet, "/api/foyer/admin/sessions", admin, nil).body["sessions"].([]any) {
		if p := p.(map[string]any); p["mode"] == "relay" {
			playing = p
		}
	}
	if playing == nil || playing["item"].(map[string]any)["title"] != "The River, a Film" || playing["device"].(map[string]any)["name"] != "Alex’s phone" {
		t.Fatalf("the playing %v", playing)
	}
	if got := h.call(http.MethodPost, "/api/foyer/admin/sessions/"+playing["id"].(string)+"/stop", admin, nil); got.status != http.StatusNoContent {
		t.Fatalf("stop: %d", got.status)
	}
	if status, _, _ := h.fetch(segments[0]); status != http.StatusForbidden {
		t.Errorf("a stopped playing's segment: %d", status)
	}

	// A video the site will not play says why — never as a 401.
	refused := h.call(http.MethodPost, "/api/foyer/videos/play", alex.token, map[string]any{"id": "dQw4w9WgXcR"})
	if refused.status != http.StatusConflict || refused.body["reason"] != "unavailable" {
		t.Errorf("refused: %d %v", refused.status, refused.body)
	}
	fits(t, "a refusal", refused.body, fixtureAnswer(t, "routes/video-refused.json"))
	if bad := h.call(http.MethodPost, "/api/foyer/videos/play", alex.token, map[string]any{"id": "../../etc"}); bad.status != http.StatusConflict || bad.body["reason"] != "unavailable" {
		t.Errorf("no video's id: %d %v", bad.status, bad.body)
	}
	if guest := h.call(http.MethodPost, "/api/foyer/videos/play", "", map[string]any{"id": "x3TJFoQWCHA"}); guest.status != http.StatusUnauthorized {
		t.Errorf("a guest: %d", guest.status)
	}

	// Nothing under the prefix in the log.
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
			if strings.Contains(r.Data, "/api/foyer/stream/") || strings.Contains(r.Data, "googlevideo") {
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
