package youtube

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// fit is what a player plays, as it said when it asked to: the tallest
// picture, and the picture codecs it decodes, in the order it would rather.
type fit struct {
	Height int      `json:"h,omitzero"`
	Codecs []string `json:"c,omitzero"`
}

func (f fit) none() bool { return f.Height == 0 && len(f.Codecs) == 0 }

var (
	resolution = regexp.MustCompile(`RESOLUTION=\d+x(\d+)`)
	codecsAttr = regexp.MustCompile(`CODECS="([^"]*)"`)
	groupAttr  = regexp.MustCompile(`(AUDIO|SUBTITLES)="([^"]*)"`)
	groupID    = regexp.MustCompile(`GROUP-ID="([^"]*)"`)
)

// family is a picture codec's name as a player's profile says it, from its
// name in a master's CODECS: "" for one not known.
func family(codecs string) string {
	for _, c := range strings.Split(codecs, ",") {
		switch c = strings.TrimSpace(c); {
		case strings.HasPrefix(c, "avc1"), strings.HasPrefix(c, "avc3"):
			return "h264"
		case strings.HasPrefix(c, "hvc1"), strings.HasPrefix(c, "hev1"):
			return "hevc"
		case strings.HasPrefix(c, "vp09"), strings.HasPrefix(c, "vp9"):
			return "vp9"
		case strings.HasPrefix(c, "av01"):
			return "av1"
		}
	}
	return ""
}

type variant struct {
	at     int // its line, and the address on the line after
	height int
	family string
}

// fitted is a master holding only what the player plays: pictures no taller
// than it decodes, in codecs it decodes — at each height, those of the codec
// it would rather — and the sounds and captions they name. ffmpeg's HLS
// reader, mpv's, opens every playlist a master names and reads the first
// segment of each before it starts: YouTube's name a dozen pictures and more,
// each fetched through Foyer, so a phone waited on every one. A master with
// nothing that fits is left as it is, and so is anything not a master.
func fitted(body []byte, f fit) []byte {
	text := string(body)
	if f.none() || !strings.Contains(text, "#EXT-X-STREAM-INF") {
		return body
	}
	lines := strings.Split(text, "\n")
	var variants []variant
	for i, line := range lines {
		if !strings.HasPrefix(line, "#EXT-X-STREAM-INF") || i+1 >= len(lines) {
			continue
		}
		v := variant{at: i}
		if m := resolution.FindStringSubmatch(line); m != nil {
			v.height, _ = strconv.Atoi(m[1])
		}
		if m := codecsAttr.FindStringSubmatch(line); m != nil {
			v.family = family(m[1])
		}
		variants = append(variants, v)
	}
	rank := func(v variant) int {
		if v.family == "" || len(f.Codecs) == 0 {
			return len(f.Codecs)
		}
		if i := slices.Index(f.Codecs, v.family); i >= 0 {
			return i
		}
		return -1
	}
	// At each height, the codec the player would rather.
	best := map[int]int{}
	for _, v := range variants {
		r := rank(v)
		if r < 0 || f.Height > 0 && v.height > f.Height {
			continue
		}
		if b, ok := best[v.height]; !ok || r < b {
			best[v.height] = r
		}
	}
	keep := map[int]bool{}
	for _, v := range variants {
		if b, ok := best[v.height]; ok && rank(v) == b && (f.Height == 0 || v.height <= f.Height) {
			keep[v.at] = true
		}
	}
	if len(keep) == 0 || len(keep) == len(variants) {
		return body
	}
	// The sound and caption groups a picture kept names; those none does go.
	named := map[string]bool{}
	for _, v := range variants {
		if keep[v.at] {
			for _, m := range groupAttr.FindAllStringSubmatch(lines[v.at], -1) {
				named[m[2]] = true
			}
		}
	}
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF") && i+1 < len(lines) && !keep[i]:
			i++ // its address too
			continue
		case strings.HasPrefix(line, "#EXT-X-MEDIA:") && (strings.Contains(line, "TYPE=AUDIO") || strings.Contains(line, "TYPE=SUBTITLES")):
			if m := groupID.FindStringSubmatch(line); m != nil && len(named) > 0 && !named[m[1]] {
				continue
			}
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n"))
}
