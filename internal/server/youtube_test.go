package server_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
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
	// Handed back what it said, it fetches the two formats asked for — a
	// moment each, saying so as the template asks — and puts them together.
	fetch := "out=; formats=; into=; load=; prev=\nfor a; do case \"$prev\" in -o) out=$a ;; -f) formats=$a ;; --merge-output-format) into=$a ;; --load-info-json) load=$a ;; esac; prev=$a; done\n" +
		"if [ -n \"$load\" ]; then\n" +
		"  file=$(echo \"$out\" | sed \"s/%(ext)s/$into/\")\n" +
		"  echo \"FOYER ${formats%%+*} downloading 1000 2000\"; sleep 1\n" +
		"  echo \"FOYER ${formats%%+*} finished 2000 2000\"; echo \"FOYER ${formats#*+} finished 500 2000\"\n" +
		"  printf 'the picture and the sound, put together' > \"$file\"; exit 0\nfi\n"
	script := "#!/bin/sh\n" + fetch + "for last; do :; done\ncase \"$last\" in\n" +
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

// theSitesAPI pretends to be the site's own API and channels' feeds,
// answering from the shapes in the youtube package's test data.
func theSitesAPI(t *testing.T) *httptest.Server {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile("../youtube/testdata/innertube/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			BrowseID     string `json:"browseId"`
			Continuation string `json:"continuation"`
			Context      struct {
				Client struct {
					Version string `json:"clientVersion"`
					Lang    string `json:"hl"`
				} `json:"client"`
			} `json:"context"`
		}
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Context.Client.Version != "2.20261010.00.00" || body.Context.Client.Lang != "en" {
				http.Error(w, "no client", http.StatusBadRequest)
				return
			}
		}
		switch {
		case r.URL.Path == "/":
			_, _ = w.Write([]byte(`<script>ytcfg.set({"INNERTUBE_CLIENT_VERSION":"2.20261010.00.00","VISITOR_DATA":"CgtWaXNpdG9y"})</script>`))
		case r.URL.Path == "/youtubei/v1/search":
			_, _ = w.Write(read("search.json"))
		case r.URL.Path == "/youtubei/v1/next":
			_, _ = w.Write(read("next.json"))
		case r.URL.Path == "/youtubei/v1/browse" && strings.HasPrefix(body.BrowseID, "VL"):
			_, _ = w.Write(read("playlist.json"))
		case r.URL.Path == "/youtubei/v1/browse":
			_, _ = w.Write(read("channel.json"))
		case r.URL.Path == "/feeds/videos.xml":
			id := r.URL.Query().Get("channel_id")
			doc := strings.NewReplacer("CHANNEL", id, "NAME", "Rivers of the World", "VIDEO1", "x3TJFoQWCHA", "VIDEO2", "Sh0rtVideo1", "TITLE1", "The River, a Film",
				"TITLE2", "The river in a minute", "PUBLISHED1", "2026-10-08T09:00:00+00:00", "PUBLISHED2", "2026-09-30T12:00:00+00:00").Replace(string(read("feed.xml")))
			_, _ = w.Write([]byte(doc))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(site.Close)
	return site
}

// The site's videos as a household's device browses them through Foyer: a
// search narrowed or not, a video's page with what the site suggests, a
// channel and its tabs, a playlist, the newest of the channels followed —
// each with the user's own watch status — and what was begun, kept with what
// it is. Foyer serves them while YouTube is switched on.
func TestTheSitesVideosAreBrowsedThroughFoyer(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	site := theSitesAPI(t)
	tool := fakeYtDlp(t, time.Now().Add(5*time.Hour))
	alex := h.makeUser("alex", map[string]any{"name": "Alex"})
	if served := h.call(http.MethodGet, "/api/foyer/household", alex.token, nil); len(served.body["served"].([]any)) != 0 {
		t.Fatalf("served before YouTube is on: %v", served.body["served"])
	}
	if got := h.call(http.MethodGet, "/api/foyer/videos/search?q=river", alex.token, nil); got.status != http.StatusNotFound {
		t.Errorf("a search with YouTube off: %d", got.status)
	}
	if got := h.call(http.MethodPut, "/api/foyer/admin/adapters/youtube", admin, map[string]any{"enabled": true, "values": map[string]any{"ytdlp": tool, "region": "DE"}}); got.status != http.StatusOK {
		t.Fatalf("youtube: %d %v", got.status, got.body)
	}
	h.server.YouTube.Tube.Site, h.server.YouTube.Feeds.Site = site.URL, site.URL
	if served := h.call(http.MethodGet, "/api/foyer/household", alex.token, nil).body["served"].([]any); len(served) != 1 || served[0].(map[string]any)["type"] != "videos" {
		t.Errorf("served %v", served)
	}

	// Alex began the film: its watch status rides on every answer naming it.
	if got := h.call(http.MethodPost, "/api/foyer/videos/play", alex.token, map[string]any{"id": "x3TJFoQWCHA"}); got.status != http.StatusOK {
		t.Fatalf("play: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodPost, "/api/foyer/watch/progress", alex.token, map[string]any{"collection": "youtube", "id": "x3TJFoQWCHA", "kind": "stopped", "positionMs": 1843000, "durationMs": 5731000}); got.status != http.StatusNoContent {
		t.Fatalf("progress: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodPost, "/api/foyer/watch/progress", alex.token, map[string]any{"collection": "youtube", "id": "nope", "kind": "stopped", "positionMs": 1}); got.status != http.StatusNotFound {
		t.Errorf("progress of no video: %d", got.status)
	}

	search := h.call(http.MethodGet, "/api/foyer/videos/search?q=river+documentary", alex.token, nil)
	if search.status != http.StatusOK {
		t.Fatalf("search: %d %v", search.status, search.body)
	}
	fits(t, "a search", search.body, fixtureAnswer(t, "routes/video-search.json"))
	results := search.body["items"].([]any)
	var kinds []string
	for _, r := range results {
		kinds = append(kinds, r.(map[string]any)["kind"].(string))
	}
	// Never a short, and the film once.
	if strings.Join(kinds, ",") != "video,channel,playlist,video" || search.body["next"] == "" {
		t.Errorf("searched %v", kinds)
	}
	film := results[0].(map[string]any)["video"].(map[string]any)
	if film["watch"].(map[string]any)["positionMs"] != 1843000.0 {
		t.Errorf("the film's watch status %v", film["watch"])
	}
	if videos := h.call(http.MethodGet, "/api/foyer/videos/search?q=river&scope=video", alex.token, nil).body["items"].([]any); len(videos) != 2 {
		t.Errorf("videos alone: %v", videos)
	}
	if more := h.call(http.MethodGet, "/api/foyer/videos/search?scope=channel&cursor=EpoDEhFyaXZlciBkb2N1bWVudGFy", alex.token, nil); more.status != http.StatusOK || len(more.body["items"].([]any)) != 1 {
		t.Errorf("a page of channels: %d %v", more.status, more.body)
	}
	if bad := h.call(http.MethodGet, "/api/foyer/videos/search?q=river&scope=song", alex.token, nil); bad.status != http.StatusBadRequest {
		t.Errorf("no such scope: %d", bad.status)
	}

	detail := h.call(http.MethodGet, "/api/foyer/videos/v/x3TJFoQWCHA", alex.token, nil)
	fits(t, "a video's page", detail.body, fixtureAnswer(t, "routes/video-detail.json"))
	video := detail.body["video"].(map[string]any)
	// Its length from what yt-dlp said when it played.
	if video["durationMs"] != 5731000.0 || video["watch"] == nil || len(detail.body["related"].([]any)) != 2 {
		t.Errorf("the page %v", detail.body)
	}

	channel := h.call(http.MethodGet, "/api/foyer/videos/channels/UCabcdefghijklmnopqrstuv", alex.token, nil)
	fits(t, "a channel", channel.body, fixtureAnswer(t, "routes/video-channel.json"))
	if tabs := channel.body["tabs"].([]any); len(tabs) != 4 || channel.body["channel"].(map[string]any)["followers"] != 182000.0 {
		t.Errorf("the channel %v", channel.body)
	}
	tab := h.call(http.MethodGet, "/api/foyer/videos/channels/UCabcdefghijklmnopqrstuv/videos", alex.token, nil)
	fits(t, "a channel's tab", tab.body, fixtureAnswer(t, "routes/video-channel-tab.json"))
	if len(tab.body["items"].([]any)) != 2 || tab.body["next"] == nil {
		t.Errorf("the tab %v", tab.body)
	}
	if none := h.call(http.MethodGet, "/api/foyer/videos/channels/UCabcdefghijklmnopqrstuv/posts", alex.token, nil); none.status != http.StatusNotFound {
		t.Errorf("a tab Foyer does not read: %d", none.status)
	}

	playlist := h.call(http.MethodGet, "/api/foyer/videos/playlists/PLlaN88a7y2_qHDbY9eQbuNTAuEJUSEeuu", alex.token, nil)
	fits(t, "a playlist", playlist.body, fixtureAnswer(t, "routes/video-playlist.json"))
	if playlist.body["playlist"].(map[string]any)["videoCount"] != 12.0 || len(playlist.body["items"].([]any)) != 2 {
		t.Errorf("the playlist %v", playlist.body)
	}

	feed := h.call(http.MethodPost, "/api/foyer/videos/feed", alex.token, map[string]any{"channels": []string{"UCabcdefghijklmnopqrstuv", "../x"}, "limit": 30})
	fits(t, "the feed", feed.body, fixtureAnswer(t, "routes/video-feed.json"))
	if items := feed.body["items"].([]any); len(items) != 1 || items[0].(map[string]any)["video"].(map[string]any)["publishedAt"] != "2026-10-08T09:00:00Z" {
		t.Errorf("the feed %v", feed.body)
	}

	resume := h.call(http.MethodGet, "/api/foyer/videos/resume", alex.token, nil)
	fits(t, "what was begun", resume.body, fixtureAnswer(t, "routes/video-resume.json"))
	begun := resume.body["items"].([]any)
	if len(begun) != 1 || begun[0].(map[string]any)["title"] != "The River, a Film" || begun[0].(map[string]any)["channel"].(map[string]any)["name"] != "Rivers of the World" {
		t.Errorf("begun %v", begun)
	}
	// Watched, it is begun no longer.
	if got := h.call(http.MethodPost, "/api/foyer/watch/played", alex.token, map[string]any{"collection": "youtube", "id": "x3TJFoQWCHA", "played": true}); got.status != http.StatusNoContent {
		t.Fatalf("played: %d", got.status)
	}
	if after := h.call(http.MethodGet, "/api/foyer/videos/resume", alex.token, nil).body["items"].([]any); len(after) != 0 {
		t.Errorf("still begun %v", after)
	}
	// Another user's is their own.
	kids := h.makeUser("kids", map[string]any{"name": "Kids"})
	if theirs := h.call(http.MethodGet, "/api/foyer/videos/v/x3TJFoQWCHA", kids.token, nil).body["video"].(map[string]any); theirs["watch"] != nil {
		t.Errorf("Kids see Alex's: %v", theirs["watch"])
	}
}

// A video is kept as the site's own renditions: the ways it can be, at each
// height the settings allow; then one, fetched and put together while the
// device asks again; then an address of Foyer's own it downloads by ranges,
// a download on Live Activity. A way the site no longer offers says so, and
// a Foyer making no copies keeps no videos.
func TestAVideoIsKeptAsTheSitesOwnRenditions(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	tool := fakeYtDlp(t, time.Now().Add(5*time.Hour))
	if got := h.call(http.MethodPut, "/api/foyer/admin/adapters/youtube", admin, map[string]any{"enabled": true, "values": map[string]any{"ytdlp": tool}}); got.status != http.StatusOK {
		t.Fatalf("youtube: %d %v", got.status, got.body)
	}
	alex := h.makeUser("alex", map[string]any{"name": "Alex"})
	device := deviceHeader(t, "phone-of-alex-1", "Alex’s phone")
	encode := map[string]any{"video": "av1", "preference": "balanced", "maxHeight": 1080, "hdr": true, "audio": "opus", "container": "mkv"}

	// Making copies off — the FFmpeg adapter not set up — no video is kept.
	if off := h.callFrom(device, http.MethodPost, "/api/foyer/videos/download/options", alex.token, map[string]any{"id": "x3TJFoQWCHA", "encode": encode}); off.status != http.StatusConflict || off.body["reason"] != "failed" {
		t.Fatalf("copies off: %d %v", off.status, off.body)
	}
	if got := h.call(http.MethodPut, "/api/foyer/admin/adapters/ffmpeg", admin, map[string]any{"enabled": true, "values": map[string]any{}}); got.status != http.StatusOK {
		t.Fatalf("ffmpeg: %d %v", got.status, got.body)
	}
	ctx, cancel := context.WithCancel(context.Background())
	running := make(chan struct{})
	go func() { h.server.Copies.Run(ctx); close(running) }()
	t.Cleanup(func() { cancel(); h.server.Copies.Close(); <-running })

	asked := h.callFrom(device, http.MethodPost, "/api/foyer/videos/download/options", alex.token, map[string]any{"id": "x3TJFoQWCHA", "encode": encode})
	if asked.status != http.StatusOK {
		t.Fatalf("options: %d %v", asked.status, asked.body)
	}
	fits(t, "a video's ways", asked.body, fixtureAnswer(t, "routes/video-download-options.json"))
	ways := asked.body["options"].([]any)
	first := ways[0].(map[string]any)
	if len(ways) != 4 || first["id"] != "yt.699+251.mkv" || first["estimatedBytes"] != 119_700_000.0 || first["hdr"] != "hdr10" || first["transcoded"] != false {
		t.Fatalf("ways %v", ways)
	}

	want := map[string]any{"id": "x3TJFoQWCHA", "option": "yt.699+251.mkv"}
	preparing := h.callFrom(device, http.MethodPost, "/api/foyer/videos/download", alex.token, want)
	if preparing.status != http.StatusAccepted {
		t.Fatalf("first ask: %d %v", preparing.status, preparing.body)
	}
	fits(t, "a video being kept", preparing.body, fixtureAnswer(t, "routes/download-preparing.json"))
	var made answer
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		made = h.callFrom(device, http.MethodPost, "/api/foyer/videos/download", alex.token, want)
		if made.status == http.StatusOK || time.Now().After(deadline) {
			break
		}
		if made.status != http.StatusAccepted {
			t.Fatalf("asked again: %d %v", made.status, made.body)
		}
	}
	if made.status != http.StatusOK {
		t.Fatalf("never kept: %d %v", made.status, made.body)
	}
	fits(t, "a video kept", made.body, fixtureAnswer(t, "routes/video-download.json"))
	version := made.body["version"].(map[string]any)
	if version["file"] != "youtube:x3TJFoQWCHA" || version["container"] != "mkv" || version["size"] != 39.0 ||
		version["video"].(map[string]any)["codec"] != "av1" || version["audio"].([]any)[0].(map[string]any)["codec"] != "opus" {
		t.Errorf("the version %v", version)
	}
	address := made.body["url"].(string)
	if !strings.HasPrefix(address, "/api/foyer/stream/copy/") {
		t.Fatalf("address %q", address)
	}
	if status, _, body := h.fetchRange(address, "bytes=4-10"); status != http.StatusPartialContent || body != "picture" {
		t.Errorf("a part: %d %q", status, body)
	}
	sessions := h.call(http.MethodGet, "/api/foyer/admin/sessions", admin, nil).body["sessions"].([]any)
	if len(sessions) != 1 || sessions[0].(map[string]any)["kind"] != "download" || sessions[0].(map[string]any)["item"].(map[string]any)["title"] != "The River, a Film" {
		t.Errorf("Live Activity %v", sessions)
	}

	// Formats the site does not offer, and a way that is no way.
	if gone := h.callFrom(device, http.MethodPost, "/api/foyer/videos/download", alex.token, map[string]any{"id": "x3TJFoQWCHA", "option": "yt.400+251.mkv"}); gone.status != http.StatusConflict || gone.body["reason"] != "failed" {
		t.Errorf("a way gone: %d %v", gone.status, gone.body)
	}
	if bad := h.callFrom(device, http.MethodPost, "/api/foyer/videos/download", alex.token, map[string]any{"id": "x3TJFoQWCHA", "option": "original"}); bad.status != http.StatusBadRequest {
		t.Errorf("no way: %d %v", bad.status, bad.body)
	}
	// A video the site will not play is kept no more than played.
	if refused := h.callFrom(device, http.MethodPost, "/api/foyer/videos/download/options", alex.token, map[string]any{"id": "dQw4w9WgXcR"}); refused.status != http.StatusConflict || refused.body["reason"] != "unavailable" {
		t.Errorf("refused: %d %v", refused.status, refused.body)
	}
}
