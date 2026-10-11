package youtube

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// video is the synthetic answer yt-dlp gives, its links expiring at expire.
func video(t *testing.T, expire time.Time) *Info {
	t.Helper()
	b, err := os.ReadFile("testdata/video.json")
	if err != nil {
		t.Fatal(err)
	}
	var info Info
	if err := json.Unmarshal(bytes.ReplaceAll(b, []byte("EXPIRE"), []byte(strconv.FormatInt(expire.Unix(), 10))), &info); err != nil {
		t.Fatal(err)
	}
	return &info
}

func TestWhatYtDlpSaysIsReadForPlaying(t *testing.T) {
	now := time.Unix(1_760_000_000, 0)
	info := video(t, now.Add(5*time.Hour))
	if master := info.Master(); !strings.Contains(master, "/hls_variant/") {
		t.Errorf("the master %q", master)
	}
	muxed, ok := info.Muxed()
	if !ok || muxed.ID != "18" || videoCodec(muxed.VCodec) != "h264" || audioCodec(muxed.ACodec) != "aac" {
		t.Errorf("the muxed file %+v", muxed)
	}
	if got := info.Expires(now); !got.Equal(now.Add(5 * time.Hour)) {
		t.Errorf("expires %v", got)
	}
	// The uploader's captions, and the site's own in the video's language — never its translations.
	var captions []string
	for _, c := range info.Captions() {
		captions = append(captions, c.Language+" "+c.Label+" "+strconv.FormatBool(c.Auto))
	}
	if strings.Join(captions, "|") != "en English false|en English (Original) true" {
		t.Errorf("the captions %q", captions)
	}
	// A dubbed video has a heard caption for each dub: its own language chooses.
	dubbed := *info
	dubbed.Language = "en-US"
	dubbed.Automatic = map[string][]Caption{
		"de-DE-orig": {{Ext: "vtt", URL: "https://www.youtube.com/api/timedtext?lang=de"}},
		"en-orig":    {{Ext: "vtt", URL: "https://www.youtube.com/api/timedtext?lang=en", Name: "English (Original)"}},
		"it-orig":    {{Ext: "vtt", URL: "https://www.youtube.com/api/timedtext?lang=it"}},
	}
	if c, ok := dubbed.heard(); !ok || c.Language != "en" || !strings.HasSuffix(c.URL, "lang=en") {
		t.Errorf("a dubbed video's heard caption: %+v", c)
	}
	dubbed.Language = ""
	if c, ok := dubbed.heard(); ok {
		t.Errorf("one of several heard captions guessed: %+v", c)
	}
	for key, want := range map[string]string{"en": "en", "pt-BR": "pt-BR", "zh-Hans": "zh-Hans", "es-419": "es-419", "en-nP7-2PuUl7o": "en", "de-DE-x1": "de-DE"} {
		if got := tagOf(key); got != want {
			t.Errorf("%s: %s", key, got)
		}
	}
	if picture := info.Picture(); !strings.HasSuffix(picture, "/maxresdefault.jpg") {
		t.Errorf("the picture %q", picture)
	}
	if !ValidID.MatchString("x3TJFoQWCHA") || ValidID.MatchString("x3TJFoQWCH") || ValidID.MatchString("../etc/pass") {
		t.Error("ids")
	}
}

func TestTheSiteDecliningAVideoIsSaidWhy(t *testing.T) {
	for said, want := range map[string]string{
		"ERROR: [youtube] dQw4w9WgXcR: Video unavailable. This video is no longer available":                         Unavailable,
		"ERROR: [youtube] dQw4w9WgXcR: Private video. Sign in if you've been granted access to this video":           Unavailable,
		"ERROR: [youtube] dQw4w9WgXcR: Sign in to confirm your age. This video may be inappropriate for some users.": Age,
		"ERROR: [youtube] dQw4w9WgXcR: Join this channel to get access to members-only content like this video":      Members,
		"ERROR: [youtube] dQw4w9WgXcR: Premieres in 3 hours":                                                         Upcoming,
		"WARNING: something\nERROR: [youtube] dQw4w9WgXcR: Sign in to confirm you’re not a bot. Use --cookies":       Blocked,
		"ERROR: [youtube] dQw4w9WgXcR: Unable to download https://www.youtube.com/watch?v=dQw4w9WgXcR: boom":         Failed,
	} {
		got := refusalOf(said)
		if got.Reason != want {
			t.Errorf("%q: %s, %q", said, got.Reason, got.Message)
		}
		if strings.Contains(got.Message, "https://") {
			t.Errorf("an address in %q", got.Message)
		}
	}
}

func testSealer(t *testing.T) *sealer {
	t.Helper()
	s, err := newSealer(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestALinkIsSealedAndOpenedOnlyAsTheSites(t *testing.T) {
	s := testSealer(t)
	link := "https://rr1---sn-test.googlevideo.com/videoplayback?expire=1&ip=203.0.113.7&itag=18"
	token, err := s.seal(link)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(token, "203.0.113.7") || strings.ContainsAny(token, "/+=") {
		t.Errorf("the token %q", token)
	}
	if u, err := s.open(token); err != nil || u.String() != link {
		t.Errorf("opened %v %v", u, err)
	}
	// A token changed by a letter, sealed under another key, or leading elsewhere is no token.
	flipped := []byte(token)
	flipped[len(flipped)/2] ^= 1
	if _, err := s.open(string(flipped)); err == nil {
		t.Error("a changed token opened")
	}
	other, _ := newSealer(bytes.Repeat([]byte{8}, 32))
	if _, err := other.open(token); err == nil {
		t.Error("another key opened it")
	}
	for _, elsewhere := range []string{"https://example.com/x", "http://rr1---sn-test.googlevideo.com/x", "https://googlevideo.com.example.com/x"} {
		bad, _ := s.seal(elsewhere)
		if _, err := s.open(bad); err == nil {
			t.Errorf("%s opened", elsewhere)
		}
	}
}

func TestAPlaylistLeadsOnlyThroughFoyer(t *testing.T) {
	s := testSealer(t)
	base := "/api/foyer/stream/yt/TICKET"
	from, _ := url.Parse("https://manifest.googlevideo.com/api/manifest/hls_variant/expire/1/file/index.m3u8")
	master := "#EXTM3U\n#EXT-X-INDEPENDENT-SEGMENTS\n" +
		`#EXT-X-MEDIA:URI="https://manifest.googlevideo.com/api/manifest/hls_playlist/itag/233/index.m3u8",TYPE=AUDIO,GROUP-ID="233",LANGUAGE="en",NAME="English",DEFAULT=YES` + "\n" +
		`#EXT-X-MEDIA:URI="subs/en.m3u8",TYPE=SUBTITLES,GROUP-ID="vtt",LANGUAGE="en",NAME="English"` + "\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=4000000,CODECS="avc1.640028,mp4a.40.2",RESOLUTION=1920x1080,AUDIO="233",SUBTITLES="vtt"` + "\n" +
		"https://manifest.googlevideo.com/api/manifest/hls_playlist/itag/137/index.m3u8\n"
	out, err := rewrite([]byte(master), from, base, s, false)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if strings.Contains(text, "googlevideo") || strings.Count(text, base+"/p/") != 2 || strings.Count(text, base+"/s/") != 1 || !strings.Contains(text, `RESOLUTION=1920x1080,AUDIO="233"`) {
		t.Fatalf("the master:\n%s", text)
	}
	// Each link opens to where the site's pointed, a relative one too.
	var opened []string
	for _, token := range append(tokens(text, base+"/p/"), tokens(text, base+"/s/")...) {
		if !strings.HasSuffix(token, ".m3u8") {
			t.Errorf("a playlist's address without its extension: %s", token)
		}
		u, err := s.open(tokenOf(token))
		if err != nil {
			t.Fatal(err)
		}
		opened = append(opened, u.Path)
	}
	if strings.Join(opened, " ") != "/api/manifest/hls_playlist/itag/233/index.m3u8 /api/manifest/hls_playlist/itag/137/index.m3u8 /api/manifest/hls_variant/expire/1/file/subs/en.m3u8" {
		t.Errorf("opened %q", opened)
	}

	media := "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:6\n" + `#EXT-X-MAP:URI="init.mp4"` + "\n#EXTINF:5.0,\n" +
		"https://rr1---sn-test.googlevideo.com/videoplayback/sq/0/file/seg.ts\n#EXTINF:5.0,\nsq/1/file/seg.ts\n#EXT-X-ENDLIST\n"
	out, err = rewrite([]byte(media), from, base, s, false)
	if err != nil {
		t.Fatal(err)
	}
	text = string(out)
	if strings.Contains(text, base+"/p/") || strings.Count(text, base+"/x/") != 3 || !strings.HasSuffix(text, "#EXT-X-ENDLIST\n") {
		t.Fatalf("the media playlist:\n%s", text)
	}
	// Each with the extension ffmpeg's HLS reader — mpv's — asks of a segment.
	var exts []string
	for _, token := range tokens(text, base+"/x/") {
		exts = append(exts, token[strings.LastIndexByte(token, '.'):])
	}
	if strings.Join(exts, " ") != ".mp4 .ts .ts" {
		t.Errorf("extensions %q", exts)
	}
	// Segments named by no extension are taken for what the playlist holds.
	bare := "#EXTM3U\n#EXT-X-MAP:URI=\"init\"\n#EXTINF:5.0,\nsq/0\n"
	out, _ = rewrite([]byte(bare), from, base, s, false)
	exts = nil
	for _, token := range tokens(string(out), base+"/x/") {
		exts = append(exts, token[strings.LastIndexByte(token, '.'):])
	}
	if strings.Join(exts, " ") != ".mp4 .m4s" {
		t.Errorf("bare extensions %q", exts)
	}
	// A playlist of captions holds WebVTT, which ffmpeg checks a segment's extension against.
	out, _ = rewrite([]byte("#EXTM3U\n#EXTINF:5.0,\nsq/0\n"), from, base, s, true)
	if vtt := tokens(string(out), base+"/x/"); len(vtt) != 1 || !strings.HasSuffix(vtt[0], ".vtt") {
		t.Errorf("captions %q", vtt)
	}
}

// tokens are the tokens after prefix in a playlist, in order.
func tokens(text, prefix string) []string {
	var out []string
	for rest := text; ; {
		i := strings.Index(rest, prefix)
		if i < 0 {
			return out
		}
		rest = rest[i+len(prefix):]
		end := strings.IndexAny(rest, "\"\n")
		out = append(out, rest[:end])
		rest = rest[end:]
	}
}
