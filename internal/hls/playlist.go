package hls

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"foyer/internal/media"
)

// TargetMs is how long a segment is meant to be: the first keyframe at
// least this long after a segment began ends it.
const TargetMs = 6000

// Segment is one segment of the timeline, from a keyframe to the next cut.
type Segment struct {
	StartMs int64
	EndMs   int64
}

// Segments cuts a file's timeline at its keyframes, about TargetMs apart:
// what every playlist of it lists, and what ffmpeg is told to cut at. A last
// segment shorter than a second joins the one before.
func Segments(idx Index) []Segment {
	cuts := []int64{0}
	for _, k := range idx.Keyframes {
		if k >= cuts[len(cuts)-1]+TargetMs {
			cuts = append(cuts, k)
		}
	}
	if len(cuts) > 1 && idx.DurationMs-cuts[len(cuts)-1] < 1000 {
		cuts = cuts[:len(cuts)-1]
	}
	segments := make([]Segment, len(cuts))
	for i, c := range cuts {
		end := idx.DurationMs
		if i+1 < len(cuts) {
			end = cuts[i+1]
		}
		segments[i] = Segment{StartMs: c, EndMs: end}
	}
	return segments
}

// SegmentAt is the segment a time falls in.
func SegmentAt(segments []Segment, ms int64) int {
	i := sort.Search(len(segments), func(i int) bool { return segments[i].EndMs > ms })
	return min(i, len(segments)-1)
}

func seconds(ms int64) string { return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64) }

// mediaPlaylist lists a rendition's segments; init, where it is fragmented
// MP4, is its initialization segment.
func mediaPlaylist(segments []Segment, name func(int) string, init string) string {
	longest := int64(0)
	for _, s := range segments {
		longest = max(longest, s.EndMs-s.StartMs)
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", int64(math.Ceil(float64(longest)/1000)))
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	if init != "" {
		fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q\n", init)
	}
	for i, s := range segments {
		fmt.Fprintf(&b, "#EXTINF:%s,\n%s\n", seconds(s.EndMs-s.StartMs), name(i))
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// audioCodecName is what a master playlist calls a rendition's sound.
func audioCodecName(codec string, track media.Audio) string {
	switch codec {
	case "ac3":
		return "ac-3"
	case "eac3":
		return "ec-3"
	case "aac":
		if strings.Contains(strings.ToLower(track.Profile), "he") {
			return "mp4a.40.5"
		}
		return "mp4a.40.2"
	}
	return codec
}

// videoRange is how a master playlist says the picture's light: what
// AVPlayer needs to show HDR as HDR.
func videoRange(v *media.Video) string {
	if v == nil {
		return "SDR"
	}
	switch v.HDR {
	case "hdr10", "hdr10+", "dolby-vision":
		return "PQ"
	case "hlg":
		return "HLG"
	}
	return "SDR"
}

// masterPlaylist names the picture, a rendition of sound per audio track and
// one of text per subtitle track the plan carries.
func masterPlaylist(idx Index, v media.Version, plan Plan) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	codecs := []string{idx.Video.Codec}
	audioPeak := int64(0)
	names := names{}
	for k, a := range plan.Audio {
		track := media.Audio{}
		if a.Track < len(v.Audio) {
			track = v.Audio[a.Track]
		}
		name := audioCodecName(a.Codec, track)
		if k == 0 {
			codecs = append(codecs, name)
		}
		attrs := []string{`TYPE=AUDIO`, `GROUP-ID="audio"`, fmt.Sprintf("NAME=%q", names.unique("audio", rendition(track.Label, track.Language, "Sound", k)))}
		if track.Language != "" {
			attrs = append(attrs, fmt.Sprintf("LANGUAGE=%q", track.Language))
		}
		channels := a.Channels
		if channels == 0 {
			channels = track.Channels
		}
		switch {
		case a.Copy && a.Codec == "eac3" && track.Spatial == "dolby-atmos":
			attrs = append(attrs, `CHANNELS="16/JOC"`)
		case channels > 0:
			attrs = append(attrs, fmt.Sprintf(`CHANNELS="%d"`, channels))
		}
		attrs = append(attrs, yesNo("DEFAULT", k == defaultAudio(v, plan)), `AUTOSELECT=YES`, fmt.Sprintf("URI=\"a%d.m3u8\"", k))
		fmt.Fprintf(&b, "#EXT-X-MEDIA:%s\n", strings.Join(attrs, ","))
		audioPeak = max(audioPeak, audioBitrate(a, track))
	}
	for k, track := range plan.Subtitles {
		s := media.Subtitle{}
		if track < len(v.Subtitles) {
			s = v.Subtitles[track]
		}
		attrs := []string{`TYPE=SUBTITLES`, `GROUP-ID="subs"`, fmt.Sprintf("NAME=%q", names.unique("subs", rendition(s.Label, s.Language, "Subtitles", k)))}
		if s.Language != "" {
			attrs = append(attrs, fmt.Sprintf("LANGUAGE=%q", s.Language))
		}
		attrs = append(attrs, yesNo("DEFAULT", s.Default), `AUTOSELECT=YES`, yesNo("FORCED", s.Forced), fmt.Sprintf("URI=\"s%d.m3u8\"", k))
		fmt.Fprintf(&b, "#EXT-X-MEDIA:%s\n", strings.Join(attrs, ","))
	}
	average := v.Bitrate
	if average == 0 && v.DurationMs > 0 {
		average = v.Size * 8 * 1000 / v.DurationMs
	}
	if average == 0 {
		average = 8_000_000
	}
	stream := []string{
		fmt.Sprintf("BANDWIDTH=%d", average*2+audioPeak),
		fmt.Sprintf("AVERAGE-BANDWIDTH=%d", average+audioPeak),
		fmt.Sprintf("CODECS=%q", strings.Join(codecs, ",")),
	}
	if idx.Video.Width > 0 && idx.Video.Height > 0 {
		stream = append(stream, fmt.Sprintf("RESOLUTION=%dx%d", idx.Video.Width, idx.Video.Height))
	}
	if idx.Video.FrameRate > 0 {
		stream = append(stream, "FRAME-RATE="+strconv.FormatFloat(idx.Video.FrameRate, 'f', 3, 64))
	}
	stream = append(stream, "VIDEO-RANGE="+videoRange(v.Video))
	if len(plan.Audio) > 0 {
		stream = append(stream, `AUDIO="audio"`)
	}
	if len(plan.Subtitles) > 0 {
		stream = append(stream, `SUBTITLES="subs"`)
	}
	fmt.Fprintf(&b, "#EXT-X-STREAM-INF:%s\nv.m3u8\n", strings.Join(stream, ","))
	return b.String()
}

// defaultAudio is the rendition a player starts with: the file's default
// track's, else its first.
func defaultAudio(v media.Version, plan Plan) int {
	for k, a := range plan.Audio {
		if a.Track < len(v.Audio) && v.Audio[a.Track].Default {
			return k
		}
	}
	return 0
}

// audioBitrate is what a rendition of sound adds to the stream.
func audioBitrate(a AudioPlan, track media.Audio) int64 {
	if a.Copy && track.Bitrate > 0 {
		return track.Bitrate
	}
	return int64(bitrateFor(a)) * 1000
}

// rendition is a rendition's name: its label, its language, or its place.
func rendition(label, language, kind string, k int) string {
	switch {
	case label != "":
		return label
	case language != "":
		return language
	}
	return fmt.Sprintf("%s %d", kind, k+1)
}

// names keeps each group's names apart: a playlist may not name two
// renditions of one group alike, and two tracks are often both "English".
type names map[string]int

func (n names) unique(group, name string) string {
	key := group + "\n" + strings.ToLower(name)
	n[key]++
	if n[key] == 1 {
		return name
	}
	return fmt.Sprintf("%s (%d)", name, n[key])
}

func yesNo(attr string, yes bool) string {
	if yes {
		return attr + "=YES"
	}
	return attr + "=NO"
}
