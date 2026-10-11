package hls

import (
	"slices"
	"strings"

	"foyer/internal/media"
)

// Modes a playing is served in.
const (
	// Direct is the file as it is.
	Direct = "direct"
	// Remux is the file as HLS: its picture copied, its sound copied or
	// made one the player plays.
	Remux = "remux"
)

// Profile is what a player plays, as Loge's PlayerProfile says it.
type Profile struct {
	Protocols       []string `json:"protocols"`
	Containers      []string `json:"containers"`
	VideoCodecs     []string `json:"videoCodecs"`
	AudioCodecs     []string `json:"audioCodecs"`
	SubtitleFormats []string `json:"subtitleFormats"`
	HDR             []string `json:"hdr,omitempty"`
	MaxHeight       int      `json:"maxHeight,omitzero"`
}

// Plan is what a remux makes of a file. It travels in the remux's address,
// so whichever Foyer answers — this one after a restart — makes the same.
type Plan struct {
	Audio     []AudioPlan `json:"a,omitempty"`
	Subtitles []int       `json:"s,omitempty"`
	// StartMs is where the player starts: the first run begins there.
	StartMs int64 `json:"t,omitzero"`
}

// AudioPlan is one audio rendition: a track of the file, as the segments
// carry it.
type AudioPlan struct {
	// Track is the track's index among the file's sounds.
	Track int    `json:"i"`
	Codec string `json:"c"`
	Copy  bool   `json:"k,omitzero"`
	// Channels is how many it is made with: none said, as many as it has.
	Channels int `json:"n,omitzero"`
}

// Decision is what play answers for a profile: the file as it is, or a
// remux and its plan.
type Decision struct {
	Mode string
	Plan Plan
}

// Remuxable picture codecs: those a playlist can name from the stream's own
// configuration.
var remuxable = []string{"h264", "hevc", "av1"}

// copyable sounds go into a remux as they are, where the player plays them;
// any other is made one of them.
var copyable = []string{"aac", "ac3", "eac3"}

// textSubtitles become WebVTT; pictures wait for transcoding.
var textSubtitles = []string{"srt", "subrip", "ass", "ssa", "vtt", "webvtt", "tx3g", "mov_text"}

// MaxChannels is the most an encoder here makes: AC-3's and E-AC-3's 5.1.
const MaxChannels = 6

// Plays says whether a player opens the file as it is — as the app's
// canPlay judges the source it makes of it: its protocol, container,
// picture, one of its sounds, and its height. What the file does not say is
// no reason it would not.
func (p Profile) Plays(v media.Version) bool {
	if !has(p.Protocols, "progressive") {
		return false
	}
	if c := v.Container; c != "" && !has(p.Containers, c) {
		return false
	}
	if v.Video != nil {
		if c := v.Video.Codec; c != "" && !has(p.VideoCodecs, c) {
			return false
		}
		if p.MaxHeight > 0 && v.Video.Height > p.MaxHeight {
			return false
		}
	}
	codecs := soundsOf(v)
	return len(codecs) == 0 || slices.ContainsFunc(codecs, func(c string) bool { return has(p.AudioCodecs, c) })
}

// MayRemux says whether a remux could play where the file does not, before
// anything is read of the file: HLS, a picture the player plays and a
// playlist can name, no taller than it shows. The keyframe index decides
// the rest.
func (p Profile) MayRemux(v media.Version) bool {
	if p.Plays(v) || !has(p.Protocols, "hls") || v.Video == nil {
		return false
	}
	codec := strings.ToLower(v.Video.Codec)
	if !has(remuxable, codec) || !has(p.VideoCodecs, codec) {
		return false
	}
	return p.MaxHeight == 0 || v.Video.Height <= p.MaxHeight
}

// Decide is what play answers: the file as it is where the player opens it;
// a remux where it plays the picture and not the file, idx being where the
// file's keyframes lie — nil where it keeps no index; and the file as it is
// where neither plays, so the app says which player would.
func Decide(v media.Version, p Profile, idx *Index) Decision {
	direct := Decision{Mode: Direct}
	if !p.MayRemux(v) || idx == nil {
		return direct
	}
	// Dolby Vision with no HDR10 layer beneath: copied, it would show in the
	// wrong colours on anything that does not read Dolby Vision.
	if idx.Video.DolbyVision == 5 {
		return direct
	}
	audio, ok := p.sound(v.Audio)
	if !ok {
		return direct
	}
	plan := Plan{Audio: audio, Subtitles: []int{}}
	if has(p.SubtitleFormats, "vtt") {
		for i, s := range v.Subtitles {
			if has(textSubtitles, strings.ToLower(s.Format)) {
				plan.Subtitles = append(plan.Subtitles, i)
			}
		}
	}
	return Decision{Mode: Remux, Plan: plan}
}

// sound is every audio track as a rendition, one codec for all: each track
// as it is where every one is the same codec the player plays and a remux
// carries; else the first's codec where it is such, else E-AC-3 where the
// player plays it, else AAC. A player that plays none of those has no
// remux.
func (p Profile) sound(tracks []media.Audio) ([]AudioPlan, bool) {
	plans := make([]AudioPlan, 0, len(tracks))
	if len(tracks) == 0 {
		return plans, true
	}
	first := strings.ToLower(tracks[0].Codec)
	target := ""
	switch {
	case has(copyable, first) && has(p.AudioCodecs, first):
		target = first
	case has(p.AudioCodecs, "eac3"):
		target = "eac3"
	case has(p.AudioCodecs, "aac"):
		target = "aac"
	default:
		return nil, false
	}
	for i, t := range tracks {
		plan := AudioPlan{Track: i, Codec: target, Copy: strings.ToLower(t.Codec) == target}
		if plan.Copy {
			plan.Channels = t.Channels
		} else if t.Channels > 0 {
			plan.Channels = min(t.Channels, MaxChannels)
		}
		plans = append(plans, plan)
	}
	return plans, true
}

// soundsOf are a file's sounds' codecs, each once.
func soundsOf(v media.Version) []string {
	var codecs []string
	for _, a := range v.Audio {
		if c := strings.ToLower(a.Codec); c != "" && !slices.Contains(codecs, c) {
			codecs = append(codecs, c)
		}
	}
	return codecs
}

func has(list []string, value string) bool {
	return slices.Contains(list, strings.ToLower(value))
}
