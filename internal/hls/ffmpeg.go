package hls

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// seekLeadMs is how far past a segment's first keyframe ffmpeg is told to
// seek. Its command line seeks 3/23 of a second before what it is asked in a
// file with B-frames, and would land on the keyframe before.
const seekLeadMs = 135

// fragmented is how each segment is written: fragmented MP4 whose movie box
// waits for the first sample — AC-3 says nothing of itself before — with the
// file's own timestamps in every fragment, B-frames' offsets signed so no
// timestamp is shifted, and so every output keeps one clock.
const fragmented = "movflags=+frag_keyframe+empty_moov+delay_moov+default_base_moof+frag_discont+negative_cts_offsets"

// bitrateFor is what a sound made anew is made at, in kb/s.
func bitrateFor(a AudioPlan) int {
	surround := a.Channels == 0 || a.Channels > 2
	switch a.Codec {
	case "eac3", "ac3":
		if surround {
			return 640
		}
		return 224
	case "aac":
		switch {
		case a.Channels == 1:
			return 128
		case surround:
			return 384
		}
		return 192
	}
	return 192
}

// seekTo is where a run from a segment asks ffmpeg to seek: just past the
// segment's first keyframe, short of the next.
func seekTo(keyframes []int64, startMs int64) int64 {
	i := sort.Search(len(keyframes), func(i int) bool { return keyframes[i] > startMs })
	lead := int64(seekLeadMs)
	if i < len(keyframes) {
		lead = min(lead, (keyframes[i]-startMs)/2)
	}
	return startMs + lead
}

// cutsFrom are the cuts after a run's first segment, as ffmpeg's segmenter
// counts them: from the run's first packet, the segment's keyframe.
func cutsFrom(segments []Segment, start int) string {
	var cuts []string
	for _, s := range segments[start+1:] {
		cuts = append(cuts, seconds(s.StartMs-segments[start].StartMs))
	}
	return strings.Join(cuts, ",")
}

// segmenter is one output: its stream cut at the playlist's times into
// fragmented MP4, numbered from the run's first segment, each finished one
// listed as it finishes.
func segmenter(segments []Segment, start int, list, pattern string) []string {
	args := []string{"-avoid_negative_ts", "disabled", "-max_muxing_queue_size", "4096", "-map_chapters", "-1", "-map_metadata", "-1",
		"-f", "segment", "-segment_format", "mp4", "-segment_format_options", fragmented,
		"-reset_timestamps", "0", "-segment_time_delta", "0.002", "-segment_start_number", strconv.Itoa(start)}
	if cuts := cutsFrom(segments, start); cuts != "" {
		args = append(args, "-segment_times", cuts)
	} else {
		args = append(args, "-segment_time", "86400000")
	}
	return append(args, "-segment_list", list, "-segment_list_type", "csv", pattern)
}

// runFiles are where a run writes, every file its own: a run that passes
// segments an earlier one finished never writes over one being read.
type runFiles struct {
	dir string
	run int
}

func (f runFiles) segments(kind string) string {
	return filepath.Join(f.dir, fmt.Sprintf("%s-r%d_%%d.mp4", kind, f.run))
}
func (f runFiles) segment(kind string, n int) string {
	return filepath.Join(f.dir, fmt.Sprintf("%s-r%d_%d.mp4", kind, f.run, n))
}
func (f runFiles) list(kind string) string {
	return filepath.Join(f.dir, fmt.Sprintf("%s-r%d.csv", kind, f.run))
}
func (f runFiles) subtitles(k int) string {
	return filepath.Join(f.dir, fmt.Sprintf("s%d-r%d.vtt", k, f.run))
}

// commandFor is ffmpeg's command for a run from segment start: the file
// through the loopback, its picture copied, a rendition of sound per plan
// entry, the text subtitles as WebVTT, and its progress on stdout.
func commandFor(input string, idx Index, segments []Segment, start int, plan Plan, files runFiles) []string {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-nostats", "-progress", "pipe:1",
		// A file that is really a playlist must not send ffmpeg elsewhere.
		"-protocol_whitelist", "http,tcp",
		"-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_on_network_error", "1", "-reconnect_delay_max", "30",
		"-rw_timeout", "60000000", "-noaccurate_seek", "-copyts"}
	if start > 0 {
		args = append(args, "-ss", seconds(seekTo(idx.Keyframes, segments[start].StartMs)))
	}
	args = append(args, "-i", input)

	args = append(args, "-map", "0:V:0", "-c:v", "copy")
	switch {
	case strings.HasPrefix(idx.Video.Codec, "hvc1"):
		// AVPlayer takes HEVC tagged hvc1 alone.
		args = append(args, "-tag:v", "hvc1")
	case strings.HasPrefix(idx.Video.Codec, "av01"):
		args = append(args, "-tag:v", "av01")
	}
	args = append(args, segmenter(segments, start, files.list("v"), files.segments("v"))...)

	for k, a := range plan.Audio {
		kind := "a" + strconv.Itoa(k)
		args = append(args, "-map", "0:a:"+strconv.Itoa(a.Track))
		if a.Copy {
			args = append(args, "-c:a", "copy")
		} else {
			args = append(args, "-c:a", a.Codec, "-b:a", strconv.Itoa(bitrateFor(a))+"k")
			if a.Channels > 0 {
				args = append(args, "-ac", strconv.Itoa(a.Channels))
			}
		}
		args = append(args, segmenter(segments, start, files.list(kind), files.segments(kind))...)
	}
	for k, track := range plan.Subtitles {
		args = append(args, "-map", "0:s:"+strconv.Itoa(track), "-c:s", "webvtt", "-flush_packets", "1", "-f", "webvtt", files.subtitles(k))
	}
	return args
}
