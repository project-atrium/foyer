package copies

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"foyer/internal/media"
)

// otto is a remux as a release has it: 4K Dolby Vision over HDR10, an
// Atmos track and its AC-3 core, a commentary, a dub, and subtitles as
// pictures and as text.
func otto() media.Version {
	return media.Version{
		File: "f7g8h9j0k1l2m3n", Label: "A Man Called Otto (2022) 2160p", Size: 18_732_101_632, Container: "mkv", DurationMs: 7_561_000, Bitrate: 19_820_000,
		Video: &media.Video{Codec: "hevc", Width: 3840, Height: 1600, FrameRate: 23.976, BitDepth: 10, HDR: "dolby-vision", Bitrate: 17_500_000},
		Audio: []media.Audio{
			{Codec: "truehd", Language: "en", Label: "English Atmos", Channels: 8, Bitrate: 4_000_000, Default: true},
			{Codec: "ac3", Language: "en", Label: "English", Channels: 6, Bitrate: 640_000},
			{Codec: "ac3", Language: "en", Label: "Commentary with the director", Channels: 2, Bitrate: 192_000},
			{Codec: "eac3", Language: "de", Channels: 6, Bitrate: 768_000},
		},
		Subtitles: []media.Subtitle{
			{Format: "pgs", Language: "en", Delivery: "embedded"},
			{Format: "srt", Language: "en", Label: "English", Delivery: "embedded"},
			{Format: "pgs", Language: "de", Forced: true, Delivery: "embedded"},
			{Format: "srt", Language: "en", Label: "English SDH", HearingImpaired: true, Delivery: "embedded"},
			{Format: "pgs", Language: "fr", Delivery: "embedded"},
		},
		Chapters: []media.Chapter{{StartMs: 0, Title: "Chapter 1"}, {StartMs: 612_000, Title: "Chapter 2"}},
		Probed:   true,
	}
}

var everything = Abilities{Encoders: map[string]bool{"libsvtav1": true, "libx265": true, "libx264": true, "libopus": true, "aac": true}, ToneMap: ToneMapX}

// asLoge is what a device asks by default.
var asLoge = Encode{Video: "av1", Preference: "balanced", MaxHeight: 1080, MaxBitrate: 8_000_000, Audio: "opus", Container: "mkv"}

func TestAnOptionNamesTheSettingsItIsMadeTo(t *testing.T) {
	id := asLoge.ID()
	if id != "copy.av1.balanced.1080.8000000.sdr.opus.mkv" {
		t.Fatalf("the id %q", id)
	}
	if back, ok := ParseID(id); !ok || back != asLoge {
		t.Fatalf("read back: %v %v", back, ok)
	}
	for _, wrong := range []string{
		"original", "copy.av1.balanced.1080.8000000.sdr.opus", "copy.vp9.balanced.1080.8000000.sdr.opus.mkv",
		"copy.av1.balanced.100.8000000.sdr.opus.mkv", "copy.av1.balanced.01080.8000000.sdr.opus.mkv",
		"copy.av1.balanced.1080.8000000.dv.opus.mkv", "copy.av1.balanced.1080.8000000.sdr.opus.avi", "copy.av1.balanced.1080.-1.sdr.opus.mkv",
	} {
		if _, ok := ParseID(wrong); ok {
			t.Errorf("%q read as an option", wrong)
		}
	}
}

func TestSettingsAreReadAsTheyMatterForTheFile(t *testing.T) {
	small := media.Version{Video: &media.Video{Width: 1280, Height: 720}}
	scope := media.Version{Video: &media.Video{Width: 1920, Height: 800, HDR: "hdr10"}}
	if got := asLoge.For(small); got.MaxHeight != 0 {
		t.Errorf("a 720p file under a 1080p ceiling: %d", got.MaxHeight)
	}
	if got := asLoge.For(scope); got.MaxHeight != 0 {
		t.Errorf("a scope film at 1920 by 800 is 1080p: %d", got.MaxHeight)
	}
	keep := asLoge
	keep.HDR = true
	if got := keep.For(small); got.HDR {
		t.Error("HDR kept for a file without it")
	}
	if got := keep.For(scope); !got.HDR {
		t.Error("HDR dropped for a file with it")
	}
	if got := asLoge.For(otto()); got != asLoge {
		t.Errorf("a 4K film: %v", got)
	}
}

func TestACopyOfAFilmIsSmallerInEveryWayThatDoesNotShow(t *testing.T) {
	v := otto()
	r, err := Plan(v, 8, asLoge, everything)
	if err != nil {
		t.Fatal(err)
	}
	video := r.Video
	if video.Codec != "av1" || video.Width != 1920 || video.Height != 800 || !video.Scale || video.BitDepth != 10 || video.CRF != 30 {
		t.Errorf("the picture %+v", video)
	}
	// Dolby Vision over HDR10, and SDR asked for: tone-mapped.
	if video.HDR != "" || video.ToneMap != ToneMapX {
		t.Errorf("HDR %q, tone-mapped by %q", video.HDR, video.ToneMap)
	}
	// One sound for each language, the first of it, and no commentary: the
	// Atmos track as 7.1 Opus, the dub as 5.1.
	var sounds []string
	for _, a := range r.Audio {
		sounds = append(sounds, strings.Join([]string{a.Language, a.Codec, a.Layout, itoa(a.Bitrate / 1000), map[bool]string{true: "default"}[a.Default]}, " "))
	}
	if want := []string{"en opus 7.1 384 default", "de opus 5.1 288 "}; !slices.Equal(sounds, want) {
		t.Errorf("the sounds %q", sounds)
	}
	if r.Audio[0].Track != 0 || r.Audio[1].Track != 3 {
		t.Errorf("the sounds' tracks %d %d", r.Audio[0].Track, r.Audio[1].Track)
	}
	// Text where the file has it; pictures only where it has none.
	var subtitles []string
	for _, s := range r.Subtitles {
		subtitles = append(subtitles, strings.Join([]string{itoa(int64(s.Track)), s.Language, s.Format, s.Codec}, " "))
	}
	if want := []string{"1 en srt copy", "2 de pgs copy", "3 en srt copy", "4 fr pgs copy"}; !slices.Equal(subtitles, want) {
		t.Errorf("the subtitles %q", subtitles)
	}
	// The ceiling is the picture's and the sound's together.
	if video.MaxRate != 8_000_000-672_000 {
		t.Errorf("the ceiling %d", video.MaxRate)
	}
	if r.Bytes <= 0 || !r.Smaller(v) || r.Bytes > v.Size/4 {
		t.Errorf("about %d bytes of %d", r.Bytes, v.Size)
	}
	made := r.Made(v, 2_400_000_000)
	if made.Video.HDR != "" || made.Video.Codec != "av1" || made.Video.Profile != "Main" || made.Quality != "fhd" || len(made.Audio) != 2 ||
		made.Audio[0].ChannelLayout != "7.1" || len(made.Subtitles) != 4 || len(made.Chapters) != 2 || made.Bitrate != 2_400_000_000*8*1000/7_561_000 {
		t.Errorf("what it is, made: %+v", made)
	}
}

func TestHDRIsKeptWhereAskedOrWhereNothingHereTakesItToSDR(t *testing.T) {
	keep := asLoge
	keep.HDR = true
	r, err := Plan(otto(), 8, keep, everything)
	if err != nil || r.Video.HDR != "hdr10" || r.Video.ToneMap != "" {
		t.Errorf("asked to keep it: %+v %v", r.Video, err)
	}
	plain := everything
	plain.ToneMap = ""
	r, err = Plan(otto(), 8, asLoge, plain)
	if err != nil || r.Video.HDR != "hdr10" || r.Video.ToneMap != "" || r.Video.BitDepth != 10 {
		t.Errorf("nothing to tone-map with: %+v %v", r.Video, err)
	}
	hlg := otto()
	hlg.Video.HDR = "hlg"
	if r, _ := Plan(hlg, 0, keep, everything); r.Video.HDR != "hlg" {
		t.Errorf("HLG kept as %q", r.Video.HDR)
	}
}

func TestDolbyVisionAloneGetsNoCopy(t *testing.T) {
	if _, err := Plan(otto(), 5, asLoge, everything); !errors.Is(err, ErrDolbyVisionOnly) {
		t.Errorf("profile 5: %v", err)
	}
	if _, err := Plan(media.Version{Audio: []media.Audio{{Codec: "flac"}}}, 0, asLoge, everything); !errors.Is(err, ErrNoPicture) {
		t.Errorf("no picture: %v", err)
	}
}

func TestAnEncoderThisFFmpegLacksIsAnotherOneItHas(t *testing.T) {
	x264 := Abilities{Encoders: map[string]bool{"libx264": true, "aac": true}}
	sdr := otto()
	sdr.Video.HDR = ""
	r, err := Plan(sdr, 0, asLoge, x264)
	if err != nil || r.Video.Codec != "h264" || r.Video.BitDepth != 8 || r.Video.CRF != 21 {
		t.Fatalf("H.264 for AV1: %+v %v", r.Video, err)
	}
	// No Opus either: AAC, and never more than 5.1 of it.
	if r.Audio[0].Codec != "aac" || r.Audio[0].Channels != 6 || r.Audio[0].Layout != "5.1" {
		t.Errorf("the sound %+v", r.Audio[0])
	}
	if _, err := Plan(otto(), 0, asLoge, Abilities{Encoders: map[string]bool{"aac": true}}); !errors.Is(err, ErrNoEncoder) {
		t.Errorf("nothing to encode with: %v", err)
	}
}

func TestASoundAlreadySmallIsCopied(t *testing.T) {
	v := media.Version{Video: &media.Video{Codec: "h264", Width: 1920, Height: 1080}, DurationMs: 60_000,
		Audio: []media.Audio{{Codec: "opus", Language: "ja", Channels: 2, Bitrate: 96_000}, {Codec: "opus", Language: "en", Channels: 2, Bitrate: 320_000}}}
	r, err := Plan(v, 0, asLoge, everything)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Audio[0].Copy || r.Audio[0].Bitrate != 96_000 || r.Audio[1].Copy {
		t.Errorf("the sounds %+v", r.Audio)
	}
	// Nothing says a language: each track is its own.
	v.Audio = []media.Audio{{Codec: "ac3", Channels: 6}, {Codec: "ac3", Channels: 2}}
	if r, _ := Plan(v, 0, asLoge, everything); len(r.Audio) != 2 {
		t.Errorf("two tracks of no language: %+v", r.Audio)
	}
}

func TestAnMP4HoldsTextSubtitlesAlone(t *testing.T) {
	mp4 := asLoge
	mp4.Video, mp4.Container = "hevc", "mp4"
	r, err := Plan(otto(), 8, mp4, everything)
	if err != nil {
		t.Fatal(err)
	}
	var formats []string
	for _, s := range r.Subtitles {
		formats = append(formats, s.Format+" "+s.Codec)
	}
	if want := []string{"tx3g mov_text", "tx3g mov_text"}; !slices.Equal(formats, want) {
		t.Errorf("the subtitles %q", formats)
	}
	args := strings.Join(r.Args("http://127.0.0.1:1/g/x", "/tmp/out.part", "fast"), " ")
	for _, want := range []string{"-c:v libx265 -preset fast -crf 23", "-tag:v hvc1", "-f mp4 -movflags +faststart"} {
		if !strings.Contains(args, want) {
			t.Errorf("no %q in %s", want, args)
		}
	}
}

func TestFFmpegIsToldEverythingThePlanSays(t *testing.T) {
	v := otto()
	v.Subtitles = append(v.Subtitles, media.Subtitle{Format: "ass", Language: "ja", Delivery: "embedded"})
	r, err := Plan(v, 8, asLoge, everything)
	if err != nil {
		t.Fatal(err)
	}
	args := r.Args("http://127.0.0.1:1/g/x", "/tmp/out.part", "balanced")
	line := strings.Join(args, " ")
	for _, want := range []string{
		"-protocol_whitelist http,tcp", "-i http://127.0.0.1:1/g/x", "-map 0:V:0",
		"-filter:v scale=1920:800,tonemapx=tonemap=bt2390:desat=0:peak=100:t=bt709:m=bt709:p=bt709:format=yuv420p10le",
		"-c:v libsvtav1 -preset 6 -crf 30", "-maxrate 7328k -bufsize 14656k", "-color_primaries bt709 -color_trc bt709 -colorspace bt709",
		"-map 0:a:0 -c:a:0 libopus -b:a:0 384k -filter:a:0 aformat=channel_layouts=7.1 -mapping_family:a:0 1 -disposition:a:0 default",
		"-map 0:a:3 -c:a:1 libopus -b:a:1 288k -filter:a:1 aformat=channel_layouts=5.1 -mapping_family:a:1 1 -disposition:a:1 0",
		"-map 0:s:2 -c:s:1 copy -disposition:s:1 forced", "-map 0:s:3 -c:s:2 copy -disposition:s:2 hearing_impaired",
		// A styled subtitle's fonts come with it.
		"-map 0:t?", "-map_chapters 0", "-metadata:s BPS= -metadata:s BPS-eng=", "-f matroska -y /tmp/out.part",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("no %q in\n%s", want, line)
		}
	}
	if args[len(args)-1] != "/tmp/out.part" {
		t.Errorf("the output %q", args[len(args)-1])
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
