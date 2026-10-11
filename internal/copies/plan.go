package copies

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"foyer/internal/media"
)

// Abilities are what this server's ffmpeg can do: the encoders a copy is
// made with, and how it takes HDR to SDR.
type Abilities struct {
	Encoders map[string]bool
	// ToneMap is the filter that takes HDR to SDR — jellyfin-ffmpeg's
	// tonemapx, or zscale's linear light with tonemap — or none, and HDR is
	// kept.
	ToneMap string
}

// The ways HDR is taken to SDR.
const (
	ToneMapX = "tonemapx"
	ToneMapZ = "zscale"
)

// encoders are what makes each codec: SVT-AV1, x265 and x264, on the CPU.
var encoders = map[string]string{"av1": "libsvtav1", "hevc": "libx265", "h264": "libx264"}

var audioEncoders = map[string]string{"opus": "libopus", "aac": "aac"}

// Why a file is offered no copy.
var (
	ErrNoPicture = errors.New("the file has no picture ffprobe could read")
	// ErrDolbyVisionOnly is Dolby Vision with no HDR10 layer beneath: made
	// anew, it would show in the wrong colours on everything.
	ErrDolbyVisionOnly = errors.New("the picture is Dolby Vision alone, which a copy would show in the wrong colours")
	ErrNoEncoder       = errors.New("this server's ffmpeg has none of the encoders a copy is made with")
)

// Recipe is a copy as it will be made: its picture, the sounds and
// subtitles it keeps, and what it is thought to come to.
type Recipe struct {
	// Encode is what was asked, as it matters for the file: what names the
	// copy.
	Encode    Encode         `json:"encode"`
	Video     VideoPlan      `json:"video"`
	Audio     []AudioPlan    `json:"audio"`
	Subtitles []SubtitlePlan `json:"subtitles"`
	// Bitrate and Bytes are an estimate, until it is made.
	Bitrate int64 `json:"bitrate"`
	Bytes   int64 `json:"bytes"`
}

// VideoPlan is the picture.
type VideoPlan struct {
	Codec    string `json:"codec"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Scale    bool   `json:"scale,omitzero"`
	BitDepth int    `json:"bitDepth"`
	// HDR is what the copy keeps — hdr10 or hlg — and ToneMap what takes the
	// file's to SDR instead.
	HDR     string `json:"hdr,omitempty"`
	ToneMap string `json:"toneMap,omitempty"`
	CRF     int    `json:"crf"`
	// MaxRate is a ceiling in bits a second, where one was asked for.
	MaxRate int64 `json:"maxRate,omitzero"`
}

// AudioPlan is one sound: a track of the file, copied or made anew.
type AudioPlan struct {
	Track    int    `json:"track"`
	Codec    string `json:"codec"`
	Copy     bool   `json:"copy,omitzero"`
	Channels int    `json:"channels"`
	// Layout is what a sound made anew is mixed to.
	Layout   string `json:"layout,omitempty"`
	Bitrate  int64  `json:"bitrate"`
	Language string `json:"language,omitempty"`
	Label    string `json:"label,omitempty"`
	Default  bool   `json:"default,omitzero"`
}

// SubtitlePlan is one subtitle track, as the copy holds it.
type SubtitlePlan struct {
	Track int `json:"track"`
	// Codec is what ffmpeg is told: copy, srt or mov_text.
	Codec           string `json:"codec"`
	Format          string `json:"format"`
	Language        string `json:"language,omitempty"`
	Label           string `json:"label,omitempty"`
	Forced          bool   `json:"forced,omitzero"`
	Default         bool   `json:"default,omitzero"`
	HearingImpaired bool   `json:"hearingImpaired,omitzero"`
}

// Plan is what a copy of the file made to e would be. v is what ffprobe
// read of it, and dolbyVision the profile its index names, 0 for none.
func Plan(v media.Version, dolbyVision int, e Encode, can Abilities) (Recipe, error) {
	if v.Video == nil || v.Video.Width <= 0 || v.Video.Height <= 0 {
		return Recipe{}, ErrNoPicture
	}
	if dolbyVision == 5 {
		return Recipe{}, ErrDolbyVisionOnly
	}
	e = e.For(v)
	codec, ok := videoCodec(e.Video, can)
	if !ok {
		return Recipe{}, ErrNoEncoder
	}
	r := Recipe{Encode: e, Audio: soundsOf(v, e, can), Subtitles: subtitlesOf(v, e.Container)}
	var audio int64
	for _, a := range r.Audio {
		audio += a.Bitrate
	}
	r.Video = pictureOf(v, e, codec, audio, can)
	r.estimate(v, audio)
	return r, nil
}

// Smaller says whether the copy is worth making: a fifth smaller than the
// file at least, where both sizes are known.
func (r Recipe) Smaller(v media.Version) bool {
	return r.Bytes == 0 || v.Size == 0 || r.Bytes < v.Size*4/5
}

// videoCodec is the codec asked for where this ffmpeg makes it, else the
// smallest it does.
func videoCodec(asked string, can Abilities) (string, bool) {
	for _, codec := range append([]string{asked}, VideoCodecs...) {
		if can.Encoders[encoders[codec]] {
			return codec, true
		}
	}
	return "", false
}

// crfs are each encoder's constant rate factor at 1080p, for a smaller
// file, a balance, and a better picture. A taller picture hides more and
// takes a little more; a smaller one shows every block.
var crfs = map[string][3]int{
	"av1":  {35, 30, 25},
	"hevc": {26, 23, 20},
	"h264": {24, 21, 18},
}

func pictureOf(v media.Version, e Encode, codec string, audio int64, can Abilities) VideoPlan {
	src := v.Video
	p := VideoPlan{Codec: codec, Width: even(src.Width), Height: even(src.Height)}
	if e.MaxHeight > 0 {
		f := math.Min(1, math.Min(float64(e.MaxHeight)/float64(src.Height), float64(boxWidth(e.MaxHeight))/float64(src.Width)))
		p.Width, p.Height = even(int(math.Round(float64(src.Width)*f))), even(int(math.Round(float64(src.Height)*f)))
	}
	p.Scale = p.Width != src.Width || p.Height != src.Height
	if hdr := hdrOf(src.HDR); hdr != "" {
		if e.HDR || can.ToneMap == "" {
			p.HDR = hdr
		} else {
			p.ToneMap = can.ToneMap
		}
	}
	// Ten bits keep gradients without banding at fewer bits for AV1 and
	// HEVC, which every decoder of theirs reads; H.264's ten-bit profile
	// few decode, so it has eight where it can.
	p.BitDepth = 10
	if codec == "h264" && p.HDR == "" {
		p.BitDepth = 8
	}
	i := map[string]int{"size": 0, "balanced": 1, "quality": 2}[e.Preference]
	p.CRF = crfs[codec][i]
	switch lines := classLines(p.Width, p.Height); {
	case lines >= 2000:
		p.CRF += map[string]int{"av1": 2, "hevc": 1, "h264": 1}[codec]
	case lines <= 576:
		p.CRF -= map[string]int{"av1": 2, "hevc": 1, "h264": 1}[codec]
	}
	if e.MaxBitrate > 0 {
		p.MaxRate = max(e.MaxBitrate-audio, 400_000)
	}
	return p
}

// hdrOf is the HDR a copy can keep: HDR10's curve — what HDR10+ and Dolby
// Vision over HDR10 have beneath — or HLG's.
func hdrOf(hdr string) string {
	switch hdr {
	case "":
		return ""
	case "hlg":
		return "hlg"
	}
	return "hdr10"
}

// classLines is a picture's lines as its class counts them: 1920 by 800 is
// 1080p.
func classLines(width, height int) int {
	return max(height, width*9/16)
}

func even(n int) int {
	return max(2, n&^1)
}

// commentary is a sound that talks over the film.
func commentary(label string) bool {
	l := strings.ToLower(label)
	return strings.Contains(l, "commentary") || strings.Contains(l, "kommentar") || strings.Contains(l, "commentaire") || strings.Contains(l, "comentario")
}

// soundsOf are the sounds a copy keeps: one for each language, the file's
// first of it — a film's English TrueHD and its English AC-3 are one sound
// made twice — and no commentary. A track that names no language is one of
// its own. Each is made anew, unless it already is what was asked for and
// no bigger.
func soundsOf(v media.Version, e Encode, can Abilities) []AudioPlan {
	codec := e.Audio
	if !can.Encoders[audioEncoders[codec]] {
		codec = "aac"
	}
	seen := map[string]bool{}
	var plans []AudioPlan
	for i, a := range v.Audio {
		if commentary(a.Label) {
			continue
		}
		key := strings.ToLower(a.Language)
		if key == "" {
			key = "#" + strconv.Itoa(i)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		plans = append(plans, soundOf(i, a, codec, e.Preference))
	}
	if len(plans) == 0 && len(v.Audio) > 0 {
		plans = append(plans, soundOf(0, v.Audio[0], codec, e.Preference))
	}
	chosen := 0
	for k, p := range plans {
		if v.Audio[p.Track].Default {
			chosen = k
			break
		}
	}
	if len(plans) > 0 {
		plans[chosen].Default = true
	}
	return plans
}

// perChannel is what a channel of each codec is made at, in bits a second:
// for a smaller file, a balance, a better sound.
var perChannel = map[string][3]int64{
	"opus": {32_000, 48_000, 64_000},
	"aac":  {48_000, 64_000, 96_000},
}

func soundOf(track int, a media.Audio, codec, preference string) AudioPlan {
	channels := a.Channels
	if channels <= 0 {
		channels = 2
	}
	p := AudioPlan{Track: track, Codec: codec, Language: a.Language, Label: a.Label}
	most := 8
	if codec == "aac" {
		most = 6
	}
	switch {
	case channels >= 7 && most >= 8:
		p.Channels, p.Layout = 8, "7.1"
	case channels >= 5:
		p.Channels, p.Layout = 6, "5.1"
	case channels >= 2:
		p.Channels, p.Layout = 2, "stereo"
	default:
		p.Channels, p.Layout = 1, "mono"
	}
	i := map[string]int{"size": 0, "balanced": 1, "quality": 2}[preference]
	p.Bitrate = min(perChannel[codec][i]*int64(p.Channels), 512_000)
	if strings.EqualFold(a.Codec, codec) && channels <= most && (a.Bitrate == 0 || a.Bitrate <= p.Bitrate*3/2) {
		p.Copy, p.Channels, p.Layout = true, channels, ""
		if a.Bitrate > 0 {
			p.Bitrate = a.Bitrate
		}
	}
	return p
}

var (
	textFormats    = []string{"srt", "ass", "vtt", "tx3g"}
	pictureFormats = []string{"pgs", "dvdsub", "dvbsub"}
)

// subtitlesOf are the subtitles a copy keeps: one for each language, forced
// or not, for the hearing-impaired or not — text where the file has it,
// pictures only where it has none, and pictures not at all in an MP4, which
// cannot hold them.
func subtitlesOf(v media.Version, container string) []SubtitlePlan {
	type kind struct {
		language            string
		forced, hearingLoss bool
	}
	keyOf := func(i int, s media.Subtitle) kind {
		language := strings.ToLower(s.Language)
		if language == "" {
			language = "#" + strconv.Itoa(i)
		}
		return kind{language, s.Forced, s.HearingImpaired}
	}
	hasText := map[kind]bool{}
	for i, s := range v.Subtitles {
		if contains(textFormats, s.Format) {
			hasText[keyOf(i, s)] = true
		}
	}
	taken := map[kind]bool{}
	plans := []SubtitlePlan{}
	for i, s := range v.Subtitles {
		key := keyOf(i, s)
		if taken[key] {
			continue
		}
		p := SubtitlePlan{Track: i, Language: s.Language, Label: s.Label, Forced: s.Forced, Default: s.Default, HearingImpaired: s.HearingImpaired}
		switch {
		case contains(textFormats, s.Format) && container == "mp4":
			p.Codec, p.Format = "mov_text", "tx3g"
		case s.Format == "tx3g":
			p.Codec, p.Format = "srt", "srt"
		case contains(textFormats, s.Format):
			p.Codec, p.Format = "copy", s.Format
		case contains(pictureFormats, s.Format) && container == "mkv" && !hasText[key]:
			p.Codec, p.Format = "copy", s.Format
		default:
			continue
		}
		taken[key] = true
		plans = append(plans, p)
	}
	return plans
}

func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

// videoBase is what a film's picture comes to at 1080p, in bits a second,
// for each codec and preference: what the estimate starts from.
var videoBase = map[string][3]float64{
	"av1":  {1_400_000, 2_200_000, 3_500_000},
	"hevc": {1_800_000, 2_800_000, 4_500_000},
	"h264": {2_800_000, 4_200_000, 6_500_000},
}

// need is how many bits each codec takes for a picture H.264 takes one for.
var need = map[string]float64{"h264": 1, "hevc": 0.65, "av1": 0.5, "vp9": 0.65, "vp8": 1, "mpeg2": 2, "mpeg4": 1.4, "vc1": 1.3}

// estimate is what the copy is thought to come to: the codec's usual rate
// for its picture's size, never more than the file's own picture would
// come to in that codec, nor than the ceiling asked; and its sounds.
func (r *Recipe) estimate(v media.Version, audio int64) {
	i := map[string]int{"size": 0, "balanced": 1, "quality": 2}[r.Encode.Preference]
	pixels := float64(r.Video.Width * r.Video.Height)
	bits := videoBase[r.Video.Codec][i] * math.Pow(pixels/(1920*1080), 0.75)
	if fps := v.Video.FrameRate; fps > 30 {
		bits *= math.Sqrt(fps / 24)
	}
	if src := pictureBitrate(v); src > 0 {
		from := need[strings.ToLower(v.Video.Codec)]
		if from == 0 {
			from = 1
		}
		scaled := math.Pow(pixels/float64(v.Video.Width*v.Video.Height), 0.75)
		bits = math.Min(bits, float64(src)*need[r.Video.Codec]/from*scaled)
	}
	if r.Video.MaxRate > 0 {
		bits = math.Min(bits, float64(r.Video.MaxRate))
	}
	r.Bitrate = int64(bits) + audio
	if v.DurationMs > 0 {
		// A container's own share, a percent or so.
		r.Bytes = int64(float64(r.Bitrate) / 8 * float64(v.DurationMs) / 1000 * 1.01)
	}
}

// pictureBitrate is what the file's picture comes to, as far as it says.
func pictureBitrate(v media.Version) int64 {
	if v.Video.Bitrate > 0 {
		return v.Video.Bitrate
	}
	total := v.Bitrate
	if total == 0 && v.Size > 0 && v.DurationMs > 0 {
		total = v.Size * 8 * 1000 / v.DurationMs
	}
	for _, a := range v.Audio {
		total -= a.Bitrate
	}
	return max(total, 0)
}

// Made is the version a copy is, once made: what a device keeps.
func (r Recipe) Made(v media.Version, size int64) media.Version {
	made := media.Version{
		File:       v.File,
		Label:      v.Label,
		Size:       size,
		Quality:    media.Quality(r.Video.Width, r.Video.Height),
		Container:  r.Encode.Container,
		DurationMs: v.DurationMs,
		Video: &media.Video{
			Codec:     r.Video.Codec,
			Profile:   profileOf(r.Video),
			Width:     r.Video.Width,
			Height:    r.Video.Height,
			FrameRate: v.Video.FrameRate,
			BitDepth:  r.Video.BitDepth,
			HDR:       r.Video.HDR,
		},
		Audio:     []media.Audio{},
		Subtitles: []media.Subtitle{},
		Chapters:  v.Chapters,
		Probed:    true,
	}
	if v.DurationMs > 0 {
		made.Bitrate = size * 8 * 1000 / v.DurationMs
	}
	for _, a := range r.Audio {
		src := v.Audio[a.Track]
		sound := media.Audio{Codec: a.Codec, Language: a.Language, Label: a.Label, Channels: a.Channels, Bitrate: a.Bitrate, Default: a.Default}
		if a.Copy {
			sound.Codec, sound.ChannelLayout = strings.ToLower(src.Codec), src.ChannelLayout
		} else {
			sound.ChannelLayout = a.Layout
		}
		made.Audio = append(made.Audio, sound)
	}
	for _, s := range r.Subtitles {
		made.Subtitles = append(made.Subtitles, media.Subtitle{Format: s.Format, Language: s.Language, Label: s.Label, Forced: s.Forced,
			Default: s.Default, HearingImpaired: s.HearingImpaired, Delivery: "embedded"})
	}
	return made
}

func profileOf(p VideoPlan) string {
	switch {
	case p.Codec == "av1":
		return "Main"
	case p.Codec == "hevc" && p.BitDepth == 10:
		return "Main 10"
	case p.Codec == "hevc":
		return "Main"
	case p.BitDepth == 10:
		return "High 10"
	}
	return "High"
}
