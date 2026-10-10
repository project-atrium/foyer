// Package probe asks ffprobe what each file is, a few at a time, through
// Foyer's loopback — and says it as Loge's MediaVersion does.
package probe

import (
	"encoding/json"
	"math"
	"path"
	"strconv"
	"strings"

	"foyer/internal/media"
)

type stream struct {
	Index            int               `json:"index"`
	CodecType        string            `json:"codec_type"`
	CodecName        string            `json:"codec_name"`
	Profile          string            `json:"profile"`
	Width            int               `json:"width"`
	Height           int               `json:"height"`
	PixFmt           string            `json:"pix_fmt"`
	BitsPerRawSample string            `json:"bits_per_raw_sample"`
	ColorTransfer    string            `json:"color_transfer"`
	AvgFrameRate     string            `json:"avg_frame_rate"`
	RFrameRate       string            `json:"r_frame_rate"`
	Channels         int               `json:"channels"`
	ChannelLayout    string            `json:"channel_layout"`
	BitRate          string            `json:"bit_rate"`
	Duration         string            `json:"duration"`
	Disposition      map[string]int    `json:"disposition"`
	Tags             map[string]string `json:"tags"`
	SideDataList     []map[string]any  `json:"side_data_list"`
}

type answer struct {
	Streams  []stream `json:"streams"`
	Chapters []struct {
		StartTime string            `json:"start_time"`
		Tags      map[string]string `json:"tags"`
	} `json:"chapters"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		Size       string `json:"size"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
}

const (
	maxTracks   = 64
	maxChapters = 300
)

// Map is a file's version from ffprobe's JSON, name its file name.
func Map(raw []byte, name string) (media.Version, error) {
	var a answer
	if err := json.Unmarshal(raw, &a); err != nil {
		return media.Version{}, err
	}
	v := media.Version{
		Container: container(a.Format.FormatName, name),
		Size:      atoi64(a.Format.Size),
		Bitrate:   atoi64(a.Format.BitRate),
		Audio:     []media.Audio{},
		Subtitles: []media.Subtitle{},
		Chapters:  []media.Chapter{},
	}
	v.DurationMs = seconds(a.Format.Duration)

	var video *stream
	for i := range a.Streams {
		s := &a.Streams[i]
		if s.Disposition["attached_pic"] == 1 {
			continue
		}
		switch s.CodecType {
		case "video":
			if video == nil || s.Disposition["default"] == 1 && video.Disposition["default"] != 1 {
				video = s
			}
		case "audio":
			if len(v.Audio) < maxTracks {
				v.Audio = append(v.Audio, audio(s))
			}
		case "subtitle":
			if len(v.Subtitles) < maxTracks {
				v.Subtitles = append(v.Subtitles, subtitle(s))
			}
		}
		if v.DurationMs == 0 {
			v.DurationMs = max(v.DurationMs, seconds(s.Duration))
			if d := s.Tags["DURATION"]; d != "" {
				v.DurationMs = max(v.DurationMs, clock(d))
			}
		}
	}
	if video != nil {
		v.Video = &media.Video{
			Codec:     codec(video.CodecName),
			Profile:   video.Profile,
			Width:     video.Width,
			Height:    video.Height,
			FrameRate: frameRate(video.AvgFrameRate, video.RFrameRate),
			BitDepth:  bitDepth(video.BitsPerRawSample, video.PixFmt),
			HDR:       hdr(video),
			Bitrate:   atoi64(video.BitRate),
		}
		v.Quality = media.Quality(video.Width, video.Height)
	}
	for _, c := range a.Chapters {
		if len(v.Chapters) >= maxChapters {
			break
		}
		v.Chapters = append(v.Chapters, media.Chapter{StartMs: seconds(c.StartTime), Title: c.Tags["title"]})
	}
	return v, nil
}

// container is a file's container, as Loge names them, from the format
// ffprobe read and the file's extension.
func container(format, name string) string {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	switch {
	case strings.Contains(format, "matroska"):
		if ext == "webm" {
			return "webm"
		}
		return "mkv"
	case strings.Contains(format, "mov") || strings.Contains(format, "mp4"):
		if ext == "mov" {
			return "mov"
		}
		return "mp4"
	case format == "mpegts":
		if ext == "m2ts" || ext == "mts" {
			return "m2ts"
		}
		return "ts"
	case format == "avi":
		return "avi"
	case format == "asf":
		return "wmv"
	case strings.HasPrefix(format, "mpeg"):
		return "mpeg"
	case format == "flv":
		return "flv"
	case format == "ogg":
		return "ogg"
	}
	return "other"
}

func codec(name string) string {
	switch name {
	case "mpeg2video":
		return "mpeg2"
	case "msmpeg4v3", "msmpeg4v2":
		return "mpeg4"
	}
	return name
}

// hdr is a picture's HDR: Dolby Vision where its configuration record says —
// unless its base layer is SDR, which Loge takes for none — else HDR10 for PQ
// and HLG for its own curve. HDR10+ is told by a frame's metadata, which the
// queue asks for apart.
func hdr(s *stream) string {
	for _, side := range s.SideDataList {
		if t, _ := side["side_data_type"].(string); strings.Contains(t, "DOVI") {
			if id, _ := side["dv_bl_signal_compatibility_id"].(float64); id == 0 || id == 2 {
				break
			}
			return "dolby-vision"
		}
	}
	switch s.ColorTransfer {
	case "smpte2084":
		return "hdr10"
	case "arib-std-b67":
		return "hlg"
	}
	return ""
}

func audio(s *stream) media.Audio {
	a := media.Audio{
		Codec:         audioCodec(s.CodecName),
		Profile:       s.Profile,
		Language:      language(s.Tags["language"]),
		Label:         s.Tags["title"],
		Channels:      s.Channels,
		ChannelLayout: layout(s.ChannelLayout),
		Bitrate:       atoi64(s.BitRate),
		Default:       s.Disposition["default"] == 1,
	}
	if a.Bitrate == 0 {
		a.Bitrate = atoi64(s.Tags["BPS"])
	}
	// ffmpeg names an object track in its profile: "Dolby TrueHD + Dolby
	// Atmos", "Dolby Digital Plus + Dolby Atmos", "DTS-HD MA + DTS:X".
	switch profile := strings.ToLower(s.Profile); {
	case strings.Contains(profile, "atmos"):
		a.Spatial = "dolby-atmos"
	case strings.Contains(profile, "dts:x"):
		a.Spatial = "dts-x"
	}
	return a
}

func audioCodec(name string) string {
	if strings.HasPrefix(name, "pcm_") {
		return "pcm"
	}
	return name
}

func layout(l string) string {
	for _, suffix := range []string{"(side)", "(back)", "(wide)"} {
		l = strings.TrimSuffix(l, suffix)
	}
	return l
}

func subtitle(s *stream) media.Subtitle {
	title := strings.ToLower(s.Tags["title"])
	return media.Subtitle{
		Format:          subtitleFormat(s.CodecName),
		Language:        language(s.Tags["language"]),
		Label:           s.Tags["title"],
		Forced:          s.Disposition["forced"] == 1 || strings.Contains(title, "forced"),
		Default:         s.Disposition["default"] == 1,
		HearingImpaired: s.Disposition["hearing_impaired"] == 1 || strings.Contains(title, "sdh"),
		Delivery:        "embedded",
	}
}

// subtitleFormat names a subtitle's codec as Loge's players do.
func subtitleFormat(name string) string {
	switch name {
	case "subrip":
		return "srt"
	case "ass", "ssa":
		return "ass"
	case "hdmv_pgs_subtitle":
		return "pgs"
	case "dvd_subtitle":
		return "dvdsub"
	case "dvb_subtitle":
		return "dvbsub"
	case "webvtt":
		return "vtt"
	case "mov_text":
		return "tx3g"
	case "eia_608":
		return "cea608"
	}
	return name
}

// twoLetter is ISO 639-2 to its two-letter code, as Loge's Jellyfin adapter
// has it, with the rest of both forms where they differ.
var twoLetter = map[string]string{
	"ara": "ar", "bul": "bg", "cat": "ca", "ces": "cs", "cze": "cs", "chi": "zh", "zho": "zh", "dan": "da", "deu": "de", "ger": "de",
	"ell": "el", "gre": "el", "eng": "en", "spa": "es", "est": "et", "fas": "fa", "per": "fa", "fin": "fi", "fra": "fr", "fre": "fr",
	"heb": "he", "hin": "hi", "hrv": "hr", "hun": "hu", "ind": "id", "ita": "it", "jpn": "ja", "kor": "ko", "lit": "lt", "lav": "lv",
	"msa": "ms", "may": "ms", "nld": "nl", "dut": "nl", "nor": "no", "nob": "nb", "nno": "nn", "pol": "pl", "por": "pt", "ron": "ro",
	"rum": "ro", "rus": "ru", "slk": "sk", "slo": "sk", "slv": "sl", "srp": "sr", "swe": "sv", "tha": "th", "tur": "tr", "ukr": "uk",
	"vie": "vi", "kur": "ku", "sqi": "sq", "alb": "sq", "bos": "bs", "mkd": "mk", "mac": "mk", "isl": "is", "ice": "is", "gle": "ga",
	"hye": "hy", "arm": "hy", "eus": "eu", "baq": "eu", "mya": "my", "bur": "my", "kat": "ka", "geo": "ka", "mri": "mi", "mao": "mi",
	"bod": "bo", "tib": "bo", "cym": "cy", "wel": "cy", "tgl": "tl", "fil": "fil", "ben": "bn", "tam": "ta", "tel": "te", "urd": "ur",
	"mal": "ml", "kan": "kn", "mar": "mr", "guj": "gu", "pan": "pa", "swa": "sw", "afr": "af", "aze": "az", "kaz": "kk", "uzb": "uz",
	"mon": "mn", "nep": "ne", "sin": "si", "khm": "km", "lao": "lo", "glg": "gl", "lat": "la", "epo": "eo", "yue": "yue",
}

// language is a stream's language in BCP 47: two letters where it has them,
// nothing where it was never said.
func language(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	switch code {
	case "", "und", "mul", "zxx", "mis":
		return ""
	}
	if two, ok := twoLetter[code]; ok {
		return two
	}
	return code
}

func bitDepth(raw, pixFmt string) int {
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return n
	}
	switch {
	case strings.Contains(pixFmt, "12le"), strings.Contains(pixFmt, "12be"), strings.Contains(pixFmt, "p012"):
		return 12
	case strings.Contains(pixFmt, "10le"), strings.Contains(pixFmt, "10be"), strings.Contains(pixFmt, "p010"):
		return 10
	case pixFmt != "":
		return 8
	}
	return 0
}

func frameRate(values ...string) float64 {
	for _, v := range values {
		num, den, ok := strings.Cut(v, "/")
		n, err1 := strconv.ParseFloat(num, 64)
		d, err2 := strconv.ParseFloat(den, 64)
		if ok && err1 == nil && err2 == nil && n > 0 && d > 0 {
			return math.Round(n/d*1000) / 1000
		}
	}
	return 0
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func seconds(s string) int64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0
	}
	return int64(math.Round(f * 1000))
}

// clock reads a Matroska DURATION tag: 00:23:40.123000000.
func clock(s string) int64 {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0
	}
	h, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	sec, _ := strconv.ParseFloat(parts[2], 64)
	return int64(h)*3_600_000 + int64(m)*60_000 + int64(math.Round(sec*1000))
}
