package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"foyer/internal/media"
)

// A 4K Matroska remux as ffprobe 7 describes one — every value invented.
const remux = `{
 "streams": [
  {"index":0,"codec_type":"video","codec_name":"hevc","profile":"Main 10","width":3840,"height":2160,"pix_fmt":"yuv420p10le",
   "color_transfer":"smpte2084","avg_frame_rate":"24000/1001","disposition":{"default":1},
   "side_data_list":[{"side_data_type":"DOVI configuration record","dv_profile":8,"dv_bl_signal_compatibility_id":1}]},
  {"index":1,"codec_type":"audio","codec_name":"truehd","profile":"Dolby TrueHD + Dolby Atmos","channels":8,"channel_layout":"7.1",
   "tags":{"language":"eng","title":"English Atmos"},"disposition":{"default":1}},
  {"index":2,"codec_type":"audio","codec_name":"dts","profile":"DTS-HD MA + DTS:X","channels":8,"channel_layout":"7.1",
   "tags":{"language":"ger"},"disposition":{"default":0}},
  {"index":3,"codec_type":"audio","codec_name":"ac3","channels":6,"channel_layout":"5.1(side)","tags":{"language":"jpn","BPS":"640000"}},
  {"index":4,"codec_type":"subtitle","codec_name":"hdmv_pgs_subtitle","tags":{"language":"eng"},"disposition":{"default":0}},
  {"index":5,"codec_type":"subtitle","codec_name":"subrip","tags":{"language":"ger","title":"Forced"},"disposition":{}},
  {"index":6,"codec_type":"subtitle","codec_name":"ass","tags":{"language":"eng","title":"English SDH"},"disposition":{}},
  {"index":7,"codec_type":"video","codec_name":"mjpeg","width":600,"height":900,"disposition":{"attached_pic":1}}
 ],
 "chapters":[{"start_time":"0.000000","tags":{"title":"Opening"}},{"start_time":"312.480000","tags":{"title":"The Turn"}}],
 "format":{"format_name":"matroska,webm","duration":"7512.345000","size":"64000000000","bit_rate":"68000000"}
}`

func TestMapsAFileAsLogeSaysIt(t *testing.T) {
	v, err := Map([]byte(remux), "Film.2160p.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if v.Container != "mkv" || v.DurationMs != 7_512_345 || v.Quality != "4k" || v.Bitrate != 68_000_000 {
		t.Fatalf("version %+v", v)
	}
	if want := (media.Video{Codec: "hevc", Profile: "Main 10", Width: 3840, Height: 2160, FrameRate: 23.976, BitDepth: 10, HDR: "dolby-vision"}); *v.Video != want {
		t.Fatalf("video %+v", *v.Video)
	}
	if len(v.Audio) != 3 ||
		v.Audio[0] != (media.Audio{Codec: "truehd", Profile: "Dolby TrueHD + Dolby Atmos", Language: "en", Label: "English Atmos", Channels: 8, ChannelLayout: "7.1", Spatial: "dolby-atmos", Default: true}) ||
		v.Audio[1].Spatial != "dts-x" || v.Audio[1].Language != "de" ||
		v.Audio[2].ChannelLayout != "5.1" || v.Audio[2].Bitrate != 640_000 || v.Audio[2].Language != "ja" {
		t.Fatalf("audio %+v", v.Audio)
	}
	if len(v.Subtitles) != 3 || v.Subtitles[0].Format != "pgs" || !v.Subtitles[1].Forced || v.Subtitles[1].Format != "srt" ||
		!v.Subtitles[2].HearingImpaired || v.Subtitles[2].Format != "ass" || v.Subtitles[0].Delivery != "embedded" {
		t.Fatalf("subtitles %+v", v.Subtitles)
	}
	if len(v.Chapters) != 2 || v.Chapters[1] != (media.Chapter{StartMs: 312_480, Title: "The Turn"}) {
		t.Fatalf("chapters %+v", v.Chapters)
	}
}

func TestReadsHDRAsLogeDoes(t *testing.T) {
	for want, raw := range map[string]string{
		"hlg":   `{"streams":[{"codec_type":"video","codec_name":"hevc","width":3840,"height":2160,"color_transfer":"arib-std-b67"}],"format":{"format_name":"matroska,webm"}}`,
		"hdr10": `{"streams":[{"codec_type":"video","codec_name":"hevc","width":3840,"height":2160,"color_transfer":"smpte2084"}],"format":{"format_name":"matroska,webm"}}`,
		// Dolby Vision on an SDR base layer is none, as Loge takes DOVIWithSDR.
		"": `{"streams":[{"codec_type":"video","codec_name":"hevc","width":1920,"height":1080,"side_data_list":[{"side_data_type":"DOVI configuration record","dv_bl_signal_compatibility_id":2}]}],"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2"}}`,
	} {
		v, err := Map([]byte(raw), "x.mkv")
		if err != nil || v.Video.HDR != want {
			t.Errorf("want %q: %+v %v", want, v.Video, err)
		}
	}
}

func TestNamesContainersAndQualitiesAsLogeDoes(t *testing.T) {
	for _, c := range []struct{ format, name, want string }{
		{"matroska,webm", "a.mkv", "mkv"}, {"matroska,webm", "a.webm", "webm"}, {"mov,mp4,m4a,3gp,3g2,mj2", "a.mp4", "mp4"},
		{"mov,mp4,m4a,3gp,3g2,mj2", "a.mov", "mov"}, {"mpegts", "a.ts", "ts"}, {"mpegts", "a.m2ts", "m2ts"}, {"avi", "a.avi", "avi"},
	} {
		if got := container(c.format, c.name); got != c.want {
			t.Errorf("%s %s: %s", c.format, c.name, got)
		}
	}
	for _, c := range []struct {
		w, h int
		want string
	}{{1920, 804, "fhd"}, {3840, 1600, "4k"}, {2560, 1440, "2k"}, {1280, 720, "hd"}, {720, 576, "sd"}, {7680, 4320, "8k"}} {
		if got := media.Quality(c.w, c.h); got != c.want {
			t.Errorf("%dx%d: %s", c.w, c.h, got)
		}
	}
	for code, want := range map[string]string{"ger": "de", "deu": "de", "und": "", "en-US": "en-us", "tur": "tr", "": ""} {
		if got := language(code); got != want {
			t.Errorf("%q: %q", code, got)
		}
	}
}

func TestSaysWhoseAFailureIs(t *testing.T) {
	for said, want := range map[string]string{
		"<input>: Server returned 404 Not Found":                     "gone",
		"<input>: Server returned 5XX Server Error reply":            "source",
		"Connection to tcp://127.0.0.1:1 failed: Connection refused": "source",
		"<input>: Invalid data found when processing input":          "file",
	} {
		if got := kind(said); got != want {
			t.Errorf("%q: %s", said, got)
		}
	}
	if got := redact("http://127.0.0.1:5000/g/abc: x; see https://zurg/dav/a", "http://127.0.0.1:5000/g/abc", "127.0.0.1:5000"); strings.Contains(got, "zurg") || strings.Contains(got, "abc") {
		t.Errorf("redacted: %q", got)
	}
}

// sample makes a two-second Matroska file with ffmpeg's own encoders: a
// picture, German audio with a title, a forced Japanese subtitle, two
// chapters.
func sample(t *testing.T) string {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}
	dir := t.TempDir()
	srt := filepath.Join(dir, "sub.srt")
	meta := filepath.Join(dir, "meta.txt")
	out := filepath.Join(dir, "sample.mkv")
	_ = os.WriteFile(srt, []byte("1\n00:00:00,000 --> 00:00:01,000\nこんにちは\n"), 0o600)
	_ = os.WriteFile(meta, []byte(";FFMETADATA1\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=1000\ntitle=One\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=1000\nEND=2000\ntitle=Two\n"), 0o600)
	cmd := exec.Command("ffmpeg", "-hide_banner", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=s=320x240:d=2", "-f", "lavfi", "-i", "sine=d=2", "-i", srt, "-i", meta,
		"-map", "0:v", "-map", "1:a", "-map", "2:s", "-map_metadata", "3", "-map_chapters", "3",
		"-c:v", "mpeg4", "-c:a", "aac", "-c:s", "srt",
		"-metadata:s:a:0", "language=ger", "-metadata:s:a:0", "title=Deutsch",
		"-metadata:s:s:0", "language=jpn", "-disposition:s:0", "forced", out)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, output)
	}
	return out
}

func TestTheRealFFprobeReadsAFileThroughAnAddress(t *testing.T) {
	path := sample(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sample.mkv" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, path)
	}))
	defer server.Close()
	raw, err := Run(context.Background(), "ffprobe", server.URL+"/sample.mkv", "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := Map(raw, "sample.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if v.Container != "mkv" || v.Video == nil || v.Video.Codec != "mpeg4" || v.Quality != "sd" || v.DurationMs < 1900 {
		t.Fatalf("version %+v video %+v", v, v.Video)
	}
	if len(v.Audio) != 1 || v.Audio[0].Language != "de" || v.Audio[0].Label != "Deutsch" || v.Audio[0].Codec != "aac" {
		t.Fatalf("audio %+v", v.Audio)
	}
	if len(v.Subtitles) != 1 || v.Subtitles[0].Language != "ja" || !v.Subtitles[0].Forced || v.Subtitles[0].Format != "srt" {
		t.Fatalf("subtitles %+v", v.Subtitles)
	}
	if len(v.Chapters) != 2 || v.Chapters[1].Title != "Two" || v.Chapters[1].StartMs != 1000 {
		t.Fatalf("chapters %+v", v.Chapters)
	}
	// An address that answers nothing is a failure, said without it.
	_, err = Run(context.Background(), "ffprobe", server.URL+"/nothing.mkv?secret=1", "")
	if err == nil || strings.Contains(err.Error(), "secret") || kind(err.Error()) != "gone" {
		t.Fatalf("failure: %v", err)
	}
}
