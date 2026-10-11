package copies

import (
	"fmt"
	"strconv"
	"strings"
)

// Efforts are how long the server spends on a copy: a faster one comes out
// a little bigger.
var Efforts = []string{"fast", "balanced", "smallest"}

// presets are each encoder's preset for an effort.
var presets = map[string]map[string]string{
	"av1":  {"fast": "8", "balanced": "6", "smallest": "4"},
	"hevc": {"fast": "fast", "balanced": "medium", "smallest": "slow"},
	"h264": {"fast": "fast", "balanced": "slow", "smallest": "slower"},
}

// statistics are the tags mkvmerge writes on every track — bitrate, frames,
// bytes — which a copy would carry over, wrong: each is cleared, under its
// own name and the one ffmpeg reads it by.
var statistics = []string{"BPS", "DURATION", "NUMBER_OF_FRAMES", "NUMBER_OF_BYTES", "_STATISTICS_WRITING_APP", "_STATISTICS_WRITING_DATE_UTC", "_STATISTICS_TAGS"}

// Args is ffmpeg's command for the copy: the file through the loopback at
// input, written to output in the container asked, its progress on stdout.
func (r Recipe) Args(input, output, effort string) []string {
	if !contains(Efforts, effort) {
		effort = "balanced"
	}
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-nostats", "-progress", "pipe:1",
		// A file that is really a playlist must not send ffmpeg elsewhere.
		"-protocol_whitelist", "http,tcp",
		"-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_on_network_error", "1", "-reconnect_delay_max", "30",
		"-rw_timeout", "60000000", "-i", input}

	v := r.Video
	format := "yuv420p"
	if v.BitDepth == 10 {
		format = "yuv420p10le"
	}
	var filters []string
	if v.Scale {
		filters = append(filters, fmt.Sprintf("scale=%d:%d", v.Width, v.Height))
	}
	switch v.ToneMap {
	case ToneMapX:
		filters = append(filters, "tonemapx=tonemap=bt2390:desat=0:peak=100:t=bt709:m=bt709:p=bt709:format="+format)
	case ToneMapZ:
		filters = append(filters, "zscale=t=linear:npl=100", "format=gbrpf32le", "zscale=p=bt709", "tonemap=tonemap=hable:desat=0",
			"zscale=t=bt709:m=bt709:r=tv", "format="+format)
	default:
		filters = append(filters, "format="+format)
	}
	args = append(args, "-map", "0:V:0", "-filter:v", strings.Join(filters, ","), "-c:v", encoders[v.Codec],
		"-preset", presets[v.Codec][effort], "-crf", strconv.Itoa(v.CRF))
	if v.Codec == "hevc" {
		args = append(args, "-x265-params", "log-level=error")
		if r.Encode.Container == "mp4" {
			// Apple's players take HEVC tagged hvc1 alone.
			args = append(args, "-tag:v", "hvc1")
		}
	}
	if v.MaxRate > 0 {
		kbps := v.MaxRate / 1000
		args = append(args, "-maxrate", strconv.FormatInt(kbps, 10)+"k", "-bufsize", strconv.FormatInt(kbps*2, 10)+"k")
	}
	if v.ToneMap != "" {
		args = append(args, "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709")
	}

	for k, a := range r.Audio {
		spec := "a:" + strconv.Itoa(k)
		args = append(args, "-map", "0:a:"+strconv.Itoa(a.Track))
		if a.Copy {
			args = append(args, "-c:"+spec, "copy")
		} else {
			args = append(args, "-c:"+spec, audioEncoders[a.Codec], "-b:"+spec, strconv.FormatInt(a.Bitrate/1000, 10)+"k",
				"-filter:"+spec, "aformat=channel_layouts="+a.Layout)
			if a.Codec == "opus" && a.Channels > 2 {
				// Surround in Opus is its mapping family 1, in Vorbis's order.
				args = append(args, "-mapping_family:"+spec, "1")
			}
		}
		args = append(args, "-disposition:"+spec, disposition(a.Default, false, false))
	}
	fonts := false
	for k, s := range r.Subtitles {
		spec := "s:" + strconv.Itoa(k)
		args = append(args, "-map", "0:s:"+strconv.Itoa(s.Track), "-c:"+spec, s.Codec, "-disposition:"+spec, disposition(s.Default, s.Forced, s.HearingImpaired))
		fonts = fonts || s.Format == "ass"
	}
	if fonts && r.Encode.Container == "mkv" {
		// A styled subtitle draws with the fonts the file carries.
		args = append(args, "-map", "0:t?")
	}
	args = append(args, "-map_chapters", "0")
	for _, tag := range statistics {
		args = append(args, "-metadata:s", tag+"=", "-metadata:s", tag+"-eng=")
	}
	args = append(args, "-max_muxing_queue_size", "4096")
	if r.Encode.Container == "mp4" {
		args = append(args, "-f", "mp4", "-movflags", "+faststart")
	} else {
		args = append(args, "-f", "matroska")
	}
	return append(args, "-y", output)
}

// disposition is a track's flags as ffmpeg names them: none is 0.
func disposition(isDefault, forced, hearingImpaired bool) string {
	var flags []string
	if isDefault {
		flags = append(flags, "default")
	}
	if forced {
		flags = append(flags, "forced")
	}
	if hearingImpaired {
		flags = append(flags, "hearing_impaired")
	}
	if len(flags) == 0 {
		return "0"
	}
	return strings.Join(flags, "+")
}
