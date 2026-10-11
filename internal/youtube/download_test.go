package youtube

import (
	"strings"
	"testing"
	"time"

	"foyer/internal/copies"
)

func TestAVideoIsKeptAsTheSitesOwnAtTheHeightsAllowed(t *testing.T) {
	info := video(t, time.Now().Add(5*time.Hour))
	said := func(ws []way) string {
		var out []string
		for _, w := range ws {
			out = append(out, w.id())
		}
		return strings.Join(out, " ")
	}
	// The site's AV1 at each height to 1080p — never its upscaled 1080p, nor
	// its HDR here — with the richest Opus in the video's own language, never
	// the quieter copy nor the dub.
	kept := ways(info, copies.Encode{Video: "av1", Preference: "balanced", MaxHeight: 1080, Audio: "opus", Container: "mkv"})
	if got := said(kept); got != "yt.399+251.mkv yt.398+251.mkv yt.397+251.mkv yt.396+251.mkv" {
		t.Errorf("AV1 to 1080p: %s", got)
	}
	if got := sizeOf(kept[0].Video, info.Duration) + sizeOf(kept[0].Audio, info.Duration); got != 81_700_000 {
		t.Errorf("1080p comes to %d", got)
	}
	if got := sizeOf(kept[2].Video, info.Duration); got != 20_200_000 {
		t.Errorf("a size yt-dlp thinks: %d", got)
	}
	// HDR kept: the HDR picture first at its height.
	if got := said(ways(info, copies.Encode{Video: "av1", Preference: "balanced", MaxHeight: 1080, HDR: true, Audio: "opus", Container: "mkv"})); !strings.HasPrefix(got, "yt.699+251.mkv ") {
		t.Errorf("HDR kept: %s", got)
	}
	// H.264 asked for, in MP4 with AAC: H.264 where the site has it, else the smallest kind there is.
	if got := said(ways(info, copies.Encode{Video: "h264", Preference: "balanced", MaxHeight: 720, Audio: "aac", Container: "mp4"})); got != "yt.136+140.mp4 yt.397+140.mp4 yt.396+140.mp4 yt.395+140.mp4" {
		t.Errorf("H.264 to 720p: %s", got)
	}
	// No height asked: from the tallest there is.
	if got := said(ways(info, copies.Encode{Video: "av1", Preference: "balanced", Audio: "opus", Container: "mkv"})); !strings.HasPrefix(got, "yt.399+251.mkv") {
		t.Errorf("any height: %s", got)
	}

	for id, want := range map[string][3]string{"yt.399+251-1.mkv": {"399", "251-1", "mkv"}, "yt.136+140.mp4": {"136", "140", "mp4"}} {
		v, a, c, ok := parseWay(id)
		if !ok || [3]string{v, a, c} != want {
			t.Errorf("%s read as %q %q %q", id, v, a, c)
		}
	}
	for _, id := range []string{"yt.399.mkv", "yt.399+251.avi", "copy.av1.balanced.1080.0.sdr.opus.mkv", "yt.+251.mkv", ""} {
		if _, _, _, ok := parseWay(id); ok {
			t.Errorf("%q read as a way", id)
		}
	}
	if k := keyOf(info.ID, kept[0]); len(k) != 32 || k == keyOf(info.ID, kept[1]) || k != keyOf(info.ID, kept[0]) {
		t.Errorf("keys %s %s", k, keyOf(info.ID, kept[1]))
	}
}
