// Package media is what a file is — Loge's MediaVersion, in snake_case — as
// a release's name says it, and as ffprobe finds it. A title's `versions`
// hold one per file, and its lists are drawn from them.
package media

import (
	"path"
	"strings"

	"foyer/internal/parse"
)

type Video struct {
	Codec     string  `json:"codec,omitempty"`
	Profile   string  `json:"profile,omitempty"`
	Width     int     `json:"width,omitzero"`
	Height    int     `json:"height,omitzero"`
	FrameRate float64 `json:"frame_rate,omitzero"`
	BitDepth  int     `json:"bit_depth,omitzero"`
	HDR       string  `json:"hdr,omitempty"`
	Bitrate   int64   `json:"bitrate,omitzero"`
}

type Audio struct {
	Codec         string `json:"codec,omitempty"`
	Profile       string `json:"profile,omitempty"`
	Language      string `json:"language,omitempty"`
	Label         string `json:"label,omitempty"`
	Channels      int    `json:"channels,omitzero"`
	ChannelLayout string `json:"channel_layout,omitempty"`
	Bitrate       int64  `json:"bitrate,omitzero"`
	Spatial       string `json:"spatial,omitempty"`
	Default       bool   `json:"default,omitzero"`
}

type Subtitle struct {
	Format          string `json:"format,omitempty"`
	Language        string `json:"language,omitempty"`
	Label           string `json:"label,omitempty"`
	Forced          bool   `json:"forced,omitzero"`
	Default         bool   `json:"default,omitzero"`
	HearingImpaired bool   `json:"hearing_impaired,omitzero"`
	Delivery        string `json:"delivery"`
}

type Chapter struct {
	StartMs int64  `json:"start_ms"`
	Title   string `json:"title,omitempty"`
}

// Version is one file: what it is, and whether ffprobe said so or only its
// name did.
type Version struct {
	File       string     `json:"file"`
	Path       string     `json:"path"`
	Label      string     `json:"label,omitempty"`
	Size       int64      `json:"size,omitzero"`
	Quality    string     `json:"quality,omitempty"`
	Container  string     `json:"container,omitempty"`
	DurationMs int64      `json:"duration_ms,omitzero"`
	Bitrate    int64      `json:"bitrate,omitzero"`
	Video      *Video     `json:"video,omitzero"`
	Audio      []Audio    `json:"audio"`
	Subtitles  []Subtitle `json:"subtitles"`
	Chapters   []Chapter  `json:"chapters,omitempty"`
	Probed     bool       `json:"probed"`
}

// Public is a version as a user is handed it: what the file is, never where
// on the share it lies — its path, kept for Foyer, is shadowed here.
type Public struct {
	Version
	Path string `json:"path,omitempty"`
}

// Public is the version as a user is handed it.
func (v Version) Public() Public { return Public{Version: v} }

// PublicAll is versions as a user is handed them.
func PublicAll(versions []Version) []Public {
	out := make([]Public, len(versions))
	for i, v := range versions {
		out[i] = v.Public()
	}
	return out
}

// Quality is a picture's class, by Loge's own lines (`resolutionName`): a
// scope film's 1920 by 800 is still FHD.
func Quality(width, height int) string {
	switch {
	case width == 0 && height == 0:
		return ""
	case height >= 4000 || width >= 7000:
		return "8k"
	case height >= 2000 || width >= 3600:
		return "4k"
	case height >= 1400 || width >= 2400:
		return "2k"
	case height >= 900 || width >= 1800:
		return "fhd"
	case height >= 700 || width >= 1200:
		return "hd"
	default:
		return "sd"
	}
}

// QualityOfLines is the class a release's name gives: 1080p is FHD.
func QualityOfLines(lines int) string {
	if lines == 0 {
		return ""
	}
	return Quality(0, lines)
}

// FromName is what a file is, as far as its name says, until ffprobe has
// looked: its quality, codec, container, HDR and audio.
func FromName(file, path string, size int64, r parse.Release) Version {
	v := Version{
		File:      file,
		Path:      path,
		Size:      size,
		Quality:   QualityOfLines(r.Resolution),
		Container: containerOf(path),
		Audio:     []Audio{},
		Subtitles: []Subtitle{},
	}
	if r.VideoCodec != "" || r.Resolution != 0 || len(r.HDR) > 0 {
		v.Video = &Video{Codec: r.VideoCodec, Height: r.Resolution, BitDepth: r.BitDepth}
		if len(r.HDR) > 0 {
			v.Video.HDR = bestHDR(r.HDR)
		}
	}
	for _, a := range r.Audio {
		switch a {
		case "atmos":
			continue
		case "dts-hd ma", "dts-hd", "dts:x":
			v.Audio = append(v.Audio, Audio{Codec: "dts", Profile: strings.ToUpper(a)})
		default:
			v.Audio = append(v.Audio, Audio{Codec: a})
		}
	}
	if len(v.Audio) > 0 {
		if strings.Contains(strings.Join(r.Audio, ","), "atmos") {
			v.Audio[0].Spatial = "dolby-atmos"
		}
		if r.Channels != "" {
			v.Audio[0].ChannelLayout = r.Channels
		}
	}
	return v
}

// bestHDR is the most a name's HDR tags say: Dolby Vision over HDR10+ over
// HDR10.
func bestHDR(tags []string) string {
	for _, want := range []string{"dolby-vision", "hdr10+", "hdr10", "hlg"} {
		for _, t := range tags {
			if t == want {
				return want
			}
		}
	}
	return ""
}

// containerOf is a file's container by its extension, as Loge names them.
func containerOf(name string) string {
	switch ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), ".")); ext {
	case "mkv", "mp4", "avi", "ts", "m2ts", "mov", "webm", "wmv", "flv", "ogg":
		return ext
	case "m4v":
		return "mp4"
	case "mts":
		return "m2ts"
	case "mpg", "mpeg", "vob":
		return "mpeg"
	case "ogm":
		return "ogg"
	case "":
		return ""
	default:
		return "other"
	}
}
