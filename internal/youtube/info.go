// Package youtube is the site's videos through Foyer: what yt-dlp says of a
// video, its streams relayed — the site's links are bound to the address
// that asked for them, and Foyer asks from home — and the routes Loge plays
// them by.
package youtube

import (
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Info is what yt-dlp says of a video: what it is, and every stream of it.
type Info struct {
	ID          string               `json:"id"`
	Title       string               `json:"title"`
	Description string               `json:"description"`
	Channel     string               `json:"channel"`
	ChannelID   string               `json:"channel_id"`
	Followers   int64                `json:"channel_follower_count"`
	Duration    float64              `json:"duration"`
	Timestamp   float64              `json:"timestamp"`
	Views       int64                `json:"view_count"`
	IsLive      bool                 `json:"is_live"`
	LiveStatus  string               `json:"live_status"`
	Language    string               `json:"language"`
	Thumbnails  []Thumbnail          `json:"thumbnails"`
	Formats     []Format             `json:"formats"`
	Subtitles   map[string][]Caption `json:"subtitles"`
	Automatic   map[string][]Caption `json:"automatic_captions"`
	Chapters    []Chapter            `json:"chapters"`
}

// Format is one stream of a video, as yt-dlp lists it.
type Format struct {
	ID       string  `json:"format_id"`
	Ext      string  `json:"ext"`
	Protocol string  `json:"protocol"`
	URL      string  `json:"url"`
	Manifest string  `json:"manifest_url"`
	VCodec   string  `json:"vcodec"`
	ACodec   string  `json:"acodec"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	FPS      float64 `json:"fps"`
	TBR      float64 `json:"tbr"`
	Size     int64   `json:"filesize"`
	Approx   int64   `json:"filesize_approx"`
	Language string  `json:"language"`
}

type Thumbnail struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type Caption struct {
	Ext  string `json:"ext"`
	URL  string `json:"url"`
	Name string `json:"name"`
}

type Chapter struct {
	Start float64 `json:"start_time"`
	Title string  `json:"title"`
}

// ValidID is a video's id as the site writes it: eleven letters, digits,
// dashes and underscores.
var ValidID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

func (f Format) hasVideo() bool { return f.VCodec != "" && f.VCodec != "none" }
func (f Format) hasAudio() bool { return f.ACodec != "" && f.ACodec != "none" }

// Master is the site's HLS master playlist — every rendition, sound and
// caption — where it offers one.
func (i *Info) Master() string {
	for _, f := range i.Formats {
		if strings.HasPrefix(f.Protocol, "m3u8") && f.Manifest != "" {
			return f.Manifest
		}
	}
	return ""
}

// Muxed is the tallest file the site muxed itself, picture and sound in one,
// where it still has one.
func (i *Info) Muxed() (Format, bool) {
	var best Format
	found := false
	for _, f := range i.Formats {
		if f.Protocol == "https" && f.hasVideo() && f.hasAudio() && f.URL != "" && (!found || f.Height > best.Height) {
			best, found = f, true
		}
	}
	return best, found
}

// Live says whether it is being streamed now.
func (i *Info) Live() bool {
	return i.IsLive || i.LiveStatus == "is_live"
}

// Expires is when the site's links stop working, the soonest of them: none
// said, six hours from now.
func (i *Info) Expires(now time.Time) time.Time {
	soonest := now.Add(6 * time.Hour)
	for _, f := range i.Formats {
		for _, link := range []string{f.URL, f.Manifest} {
			if at, ok := expiry(link); ok && at.Before(soonest) {
				soonest = at
			}
		}
	}
	return soonest
}

var pathExpiry = regexp.MustCompile(`/expire/(\d+)/`)

// expiry reads when a link of the site's expires: in its query, or in its
// path for an HLS one.
func expiry(link string) (time.Time, bool) {
	if link == "" {
		return time.Time{}, false
	}
	value := ""
	if u, err := url.Parse(link); err == nil {
		value = u.Query().Get("expire")
	}
	if value == "" {
		if m := pathExpiry.FindStringSubmatch(link); m != nil {
			value = m[1]
		}
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0), true
}

// Captions are the uploader's captions, each language once, and the one
// the site made in the video's own language — never its machine
// translations into every other.
func (i *Info) Captions() []CaptionChoice {
	var out []CaptionChoice
	languages := make([]string, 0, len(i.Subtitles))
	for language := range i.Subtitles {
		languages = append(languages, language)
	}
	slices.Sort(languages)
	for _, key := range languages {
		if c, ok := vtt(i.Subtitles[key]); ok && key != "live_chat" {
			out = append(out, CaptionChoice{Language: tagOf(key), Label: labelOf(c, tagOf(key)), URL: c.URL})
		}
	}
	if c, ok := i.heard(); ok {
		out = append(out, c)
	}
	return out
}

// heard is the caption the site made of what is said in the video's own
// language. The site marks what it heard "-orig" and translates the rest
// from it — and a video it dubbed has one heard caption for each dub, so
// it is the video's own language that chooses; none said, a single one.
func (i *Info) heard() (CaptionChoice, bool) {
	var keys []string
	if i.Language != "" {
		spoken := strings.SplitN(i.Language, "-", 2)[0]
		keys = []string{i.Language + "-orig", spoken + "-orig", i.Language, spoken}
	} else {
		for key := range i.Automatic {
			if strings.HasSuffix(key, "-orig") {
				keys = append(keys, key)
			}
		}
		if len(keys) != 1 {
			return CaptionChoice{}, false
		}
	}
	for _, key := range keys {
		if c, ok := vtt(i.Automatic[key]); ok {
			language := tagOf(strings.TrimSuffix(key, "-orig"))
			return CaptionChoice{Language: language, Label: labelOf(c, language), URL: c.URL, Auto: true}, true
		}
	}
	return CaptionChoice{}, false
}

var (
	script = regexp.MustCompile(`^[A-Z][a-z]{3}$`)
	region = regexp.MustCompile(`^([A-Z]{2}|[0-9]{3})$`)
)

// tagOf is a caption's language as a BCP 47 tag — en, pt-BR, zh-Hans: yt-dlp
// names a second track of one language by adding the site's own id for it,
// "en-nP7-2PuUl7o", which no player reads as a language.
func tagOf(key string) string {
	parts := strings.Split(key, "-")
	tag := parts[0]
	for _, part := range parts[1:] {
		if !script.MatchString(part) && !region.MatchString(part) {
			break
		}
		tag += "-" + part
	}
	return tag
}

// CaptionChoice is a caption a player is offered.
type CaptionChoice struct {
	Language, Label, URL string
	Auto                 bool
}

func vtt(list []Caption) (Caption, bool) {
	for _, c := range list {
		if c.Ext == "vtt" && c.URL != "" {
			return c, true
		}
	}
	return Caption{}, false
}

func labelOf(c Caption, language string) string {
	if c.Name != "" {
		return c.Name
	}
	return language
}

// Picture is the video's widest picture of the site's own.
func (i *Info) Picture() string {
	best := Thumbnail{}
	for _, t := range i.Thumbnails {
		if t.URL != "" && strings.HasSuffix(strings.SplitN(t.URL, "?", 2)[0], ".jpg") && t.Width >= best.Width {
			best = t
		}
	}
	return best.URL
}

// videoCodec is a format's picture codec as Loge names them.
func videoCodec(codec string) string {
	switch {
	case strings.HasPrefix(codec, "avc1"):
		return "h264"
	case strings.HasPrefix(codec, "hvc1"), strings.HasPrefix(codec, "hev1"):
		return "hevc"
	case strings.HasPrefix(codec, "av01"):
		return "av1"
	case strings.HasPrefix(codec, "vp09"), strings.HasPrefix(codec, "vp9"):
		return "vp9"
	}
	return ""
}

// audioCodec is a format's sound codec as Loge names them.
func audioCodec(codec string) string {
	switch {
	case strings.HasPrefix(codec, "mp4a"):
		return "aac"
	case strings.HasPrefix(codec, "opus"):
		return "opus"
	case strings.HasPrefix(codec, "ec-3"):
		return "eac3"
	case strings.HasPrefix(codec, "ac-3"):
		return "ac3"
	}
	return ""
}
