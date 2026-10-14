package youtube

import (
	"slices"
	"strings"
	"testing"
)

// A master as the site sends one: pictures in H.264 and VP9 at each height,
// two sounds, captions.
const siteMaster = "#EXTM3U\n#EXT-X-INDEPENDENT-SEGMENTS\n" +
	`#EXT-X-MEDIA:URI="a233.m3u8",TYPE=AUDIO,GROUP-ID="233",NAME="Default",DEFAULT=YES` + "\n" +
	`#EXT-X-MEDIA:URI="a234.m3u8",TYPE=AUDIO,GROUP-ID="234",NAME="Default",DEFAULT=YES` + "\n" +
	`#EXT-X-MEDIA:URI="en.m3u8",TYPE=SUBTITLES,GROUP-ID="vtt",LANGUAGE="en",NAME="English",DEFAULT=NO` + "\n" +
	`#EXT-X-STREAM-INF:BANDWIDTH=309496,CODECS="avc1.4D4015,mp4a.40.5",RESOLUTION=426x240,AUDIO="233",SUBTITLES="vtt"` + "\nh240a.m3u8\n" +
	`#EXT-X-STREAM-INF:BANDWIDTH=801182,CODECS="avc1.4D401E,mp4a.40.2",RESOLUTION=640x360,AUDIO="234",SUBTITLES="vtt"` + "\nh360.m3u8\n" +
	`#EXT-X-STREAM-INF:BANDWIDTH=2566638,CODECS="avc1.4D401F,mp4a.40.2",RESOLUTION=1280x720,AUDIO="234",SUBTITLES="vtt"` + "\nh720.m3u8\n" +
	`#EXT-X-STREAM-INF:BANDWIDTH=4800000,CODECS="avc1.640028,mp4a.40.2",RESOLUTION=1920x1080,AUDIO="234",SUBTITLES="vtt"` + "\nh1080.m3u8\n" +
	`#EXT-X-STREAM-INF:BANDWIDTH=87251,CODECS="vp09.00.10.08,mp4a.40.5",RESOLUTION=256x144,AUDIO="233",SUBTITLES="vtt"` + "\nv144.m3u8\n" +
	`#EXT-X-STREAM-INF:BANDWIDTH=612723,CODECS="vp09.00.21.08,mp4a.40.2",RESOLUTION=640x360,AUDIO="234",SUBTITLES="vtt"` + "\nv360.m3u8\n" +
	`#EXT-X-STREAM-INF:BANDWIDTH=1931139,CODECS="vp09.00.31.08,mp4a.40.2",RESOLUTION=1280x720,AUDIO="234",SUBTITLES="vtt"` + "\nv720.m3u8\n" +
	`#EXT-X-STREAM-INF:BANDWIDTH=12000000,CODECS="vp09.00.51.08,mp4a.40.2",RESOLUTION=3840x2160,AUDIO="234",SUBTITLES="vtt"` + "\nv2160.m3u8\n"

func addresses(master []byte) []string {
	var out []string
	for _, line := range strings.Split(string(master), "\n") {
		if strings.HasSuffix(line, ".m3u8") && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

func groups(master []byte) []string {
	var out []string
	for _, line := range strings.Split(string(master), "\n") {
		if m := groupID.FindStringSubmatch(line); m != nil && strings.HasPrefix(line, "#EXT-X-MEDIA") {
			out = append(out, m[1])
		}
	}
	return out
}

func TestAMasterHoldsOnlyWhatThePlayerPlays(t *testing.T) {
	cases := []struct {
		name   string
		fit    fit
		want   []string
		groups []string
	}{
		// mpv on Android: every codec, H.264 first, 1080p at most — H.264 reaches
		// 1080p where VP9 stops at 720p, so H.264 alone.
		{"mpv on a phone", fit{Height: 1080, Codecs: []string{"h264", "hevc", "vp9", "av1"}}, []string{"h240a.m3u8", "h360.m3u8", "h720.m3u8", "h1080.m3u8"}, []string{"233", "234", "vtt"}},
		// VLC to 4K, H.264 first: VP9 alone reaches 2160p, so VP9 alone — never
		// H.264 to 1080p and VP9 above it, which libVLC on Android dies switching across.
		{"VLC to 4K", fit{Height: 2160, Codecs: []string{"h264", "hevc", "vp8", "vp9", "av1"}}, []string{"v144.m3u8", "v360.m3u8", "v720.m3u8", "v2160.m3u8"}, []string{"233", "234", "vtt"}},
		// Both reach 720p: the one the player would rather.
		{"a tie at 720p", fit{Height: 720, Codecs: []string{"h264", "vp9"}}, []string{"h240a.m3u8", "h360.m3u8", "h720.m3u8"}, []string{"233", "234", "vtt"}},
		// No taller than 360: the sound only the taller ones named still goes with the 240p's.
		{"360p at most", fit{Height: 360, Codecs: []string{"h264"}}, []string{"h240a.m3u8", "h360.m3u8"}, []string{"233", "234", "vtt"}},
		// H.264 alone, 1080p: the HE-AAC group only the 144p VP9 and the 240p name stays with the 240p.
		{"H.264 alone", fit{Codecs: []string{"h264"}}, []string{"h240a.m3u8", "h360.m3u8", "h720.m3u8", "h1080.m3u8"}, []string{"233", "234", "vtt"}},
		// Said nothing: the master as the site sent it.
		{"nothing said", fit{}, []string{"h240a.m3u8", "h360.m3u8", "h720.m3u8", "h1080.m3u8", "v144.m3u8", "v360.m3u8", "v720.m3u8", "v2160.m3u8"}, []string{"233", "234", "vtt"}},
		// Nothing fits — a codec the site has none of: the master as it is, never an empty one.
		{"nothing fits", fit{Codecs: []string{"mpeg2video"}}, []string{"h240a.m3u8", "h360.m3u8", "h720.m3u8", "h1080.m3u8", "v144.m3u8", "v360.m3u8", "v720.m3u8", "v2160.m3u8"}, []string{"233", "234", "vtt"}},
	}
	for _, c := range cases {
		got := fitted([]byte(siteMaster), c.fit)
		if a := addresses(got); !slices.Equal(a, c.want) {
			t.Errorf("%s: %v", c.name, a)
		}
		if g := groups(got); !slices.Equal(g, c.groups) {
			t.Errorf("%s: groups %v", c.name, g)
		}
	}
	// A sound group no picture kept names goes: mpv opens every playlist a master names.
	trimmed := fitted([]byte(siteMaster), fit{Height: 720, Codecs: []string{"vp9"}})
	if g := groups(trimmed); !slices.Equal(g, []string{"233", "234", "vtt"}) {
		t.Errorf("vp9 to 720: groups %v", g)
	}
	// A player that decodes VP9 alone is handed no H.264, even where VP9 has no such height.
	vp9 := fitted([]byte(siteMaster), fit{Height: 1080, Codecs: []string{"vp9"}})
	if a := addresses(vp9); !slices.Equal(a, []string{"v144.m3u8", "v360.m3u8", "v720.m3u8"}) {
		t.Errorf("vp9 to 1080: %v", a)
	}
	// Not a master: as it is.
	media := "#EXTM3U\n#EXTINF:5.0,\nsq/0\n"
	if got := string(fitted([]byte(media), fit{Height: 360})); got != media {
		t.Errorf("a media playlist changed: %q", got)
	}
	if family(`avc1.4D401F,mp4a.40.2`) != "h264" || family("hev1.1.6.L93.B0") != "hevc" || family("av01.0.08M.08") != "av1" || family("mp4a.40.2") != "" {
		t.Error("families")
	}
}
