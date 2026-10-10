// Package parse reads what a release's name says — the title, the year, the
// season and episode, what the file is — as a scanner must before it can ask
// TMDB anything. It is what CineSync did by renaming; Foyer reads the names as
// they are and renames nothing.
//
// It is pure, and wrong at times: a name is whatever its uploader typed. The
// library checks what it reads against TMDB, and a superuser corrects the
// rest.
package parse

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"foyer/internal/titles"
)

// Release is what one name says. A number never said is 0.
type Release struct {
	Title string `json:"title,omitempty"`
	Year  int    `json:"year,omitempty"`
	// Alternate is the title with a year it may have swallowed — "Class of
	// 1984" — to try when the title alone finds nothing.
	Alternate string `json:"alternate,omitempty"`

	Seasons  []int `json:"seasons,omitempty"`
	Episodes []int `json:"episodes,omitempty"`
	// Absolute is an episode counted from the series' start, as anime
	// releases number them. Counted says the name numbered it so, after a
	// dash; an "Episode 5" with no season may yet belong to a season folder.
	Absolute []int `json:"absolute,omitempty"`
	Counted  bool  `json:"counted,omitempty"`
	// Pack is a range a pack's name covers, "(0001-1071)": a hint, never an
	// episode.
	Pack         [2]int `json:"pack,omitempty"`
	EpisodeTitle string `json:"episode_title,omitempty"`
	// Special is "special", "ova" or "recap", from the name or a folder.
	Special     string `json:"special,omitempty"`
	MovieNumber int    `json:"movie_number,omitempty"`
	AirDate     string `json:"air_date,omitempty"`

	Resolution int      `json:"resolution,omitempty"`
	Source     string   `json:"source,omitempty"`
	Remux      bool     `json:"remux,omitempty"`
	VideoCodec string   `json:"video_codec,omitempty"`
	BitDepth   int      `json:"bit_depth,omitempty"`
	HDR        []string `json:"hdr,omitempty"`
	Audio      []string `json:"audio,omitempty"`
	Channels   string   `json:"channels,omitempty"`
	Edition    string   `json:"edition,omitempty"`
	Repack     bool     `json:"repack,omitempty"`
	Upscaled   bool     `json:"upscaled,omitempty"`
	DualAudio  bool     `json:"dual_audio,omitempty"`
	MultiSub   bool     `json:"multi_sub,omitempty"`
	Group      string   `json:"group,omitempty"`
	CRC        string   `json:"crc,omitempty"`
	Container  string   `json:"container,omitempty"`
	Sample     bool     `json:"sample,omitempty"`
	Extra      string   `json:"extra,omitempty"`
	// Anime counts what marks an anime release: a group in brackets first,
	// numbers counted from the start, dual audio, an anime service.
	Anime int `json:"anime,omitempty"`
}

var (
	videoExt   = regexp.MustCompile(`(?i)\.(mkv|mp4|m4v|avi|mov|wmv|ts|m2ts|mts|webm|mpe?g|vob|ogm|divx|flv|3gp|rmvb)$`)
	otherExt   = regexp.MustCompile(`(?i)\.(srt|ass|ssa|sub|idx|vtt|nfo|txt|jpe?g|png|rar|zip|7z|iso|exe|url|sfv|md5)$`)
	leading    = regexp.MustCompile(`^\s*\[([^\]]{1,64})\]\s*`)
	channelDot = regexp.MustCompile(`\b([1-9])\.([0-2])\b`)
	codecDot   = regexp.MustCompile(`(?i)\bH\.(26[45])\b`)
	spaces     = regexp.MustCompile(`\s+`)
	collision  = regexp.MustCompile(`^(.*) \((\d{1,6})\)(\.[A-Za-z0-9]{2,4})$`)
	seasonEp   = regexp.MustCompile(`(?i)\bS(\d{1,3})[ _-]*E(\d{1,4})`)
	more       = regexp.MustCompile(`(?i)^(?:[ _]*-?[ _]*E(\d{1,4})\b|-(\d{1,4})\b)`)
	crossEp    = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{2,3})\b`)
	// A spaced range needs its S — "S01 - S03" — or " - 05" would be one.
	seasonPack  = regexp.MustCompile(`(?i)\bS(\d{1,3})\b(?:(?:-S?|[ _]+-[ _]+S)(\d{1,3})\b)?`)
	seasonWord  = regexp.MustCompile(`(?i)\b(?:Seasons?|Series|Saison|Staffel|Temporada|Stagione)[ _]*(\d{1,3})(?:[ _]*(?:-|to|&)[ _]*(\d{1,3}))?\b`)
	ordinal     = regexp.MustCompile(`(?i)\b(\d{1,2})(?:st|nd|rd|th)[ _]+Season\b`)
	episodeWord = regexp.MustCompile(`(?i)\b(?:Episode|Ep)\.?[ _]*(\d{1,4})(?:[ _]*[-~][ _]*(\d{1,4}))?\b`)
	bareEp      = regexp.MustCompile(`(?i)\bE(\d{2,4})\b`)
	absolute    = regexp.MustCompile(`^(\d{1,4})(?:[ _]*[-~][ _]*(\d{1,4}))?(?:v(\d))?(?:\s|$|\[|\()`)
	recap       = regexp.MustCompile(`^(\d{1,4})\.5(?:\s|$|\[|\()`)
	special     = regexp.MustCompile(`(?i)\b(OVA|OAD|OAV|ONA|Specials?)\b(?:[ _]*(\d{1,3})\b)?`)
	movieNumber = regexp.MustCompile(`(?i)\b(?:Movie|Film|Gekijouban)[ _]*(\d{1,2})\b`)
	packRange   = regexp.MustCompile(`\((\d{1,4})\s*-\s*(\d{1,4})(?:\+[^)]*)?\)`)
	// "Black Clover 1-170": a pack's range unbracketed, its dash unspaced —
	// " - 05" is an episode's.
	bareRange = regexp.MustCompile(`(?:^|\s)(\d{1,4})-(\d{1,4})(?:\s|$)`)
	// "Doctor Who S04SP1 The Next Doctor": a season's special, by its name.
	seasonSpecial = regexp.MustCompile(`(?i)\bS(\d{1,3})[ _]?SP(\d{1,3})\b`)
	// What may follow a pack's film number: nothing, a bracket, a dash, a
	// year or a resolution — not more of a title, as "The Lego Movie 2 The
	// Second Part" has.
	afterNumber = regexp.MustCompile(`(?i)^(?:$|[(\[–—-]|(?:19|20)\d\d\b|\d{3,4}p\b)`)
	// "Seinfeld 1989-1998": the years a series ran; the first is its year.
	yearSpan   = regexp.MustCompile(`\b((?:19|20)\d\d)-((?:19|20)\d\d)\b`)
	airDate    = regexp.MustCompile(`\b((?:19|20)\d\d)[ _-](0[1-9]|1[0-2])[ _-](0[1-9]|[12]\d|3[01])\b`)
	yearMark   = regexp.MustCompile(`[(\[]((?:19|20)\d\d)[)\]]`)
	yearDash   = regexp.MustCompile(`\s-\s((?:19|20)\d\d)(?:\s|$)`)
	yearBare   = regexp.MustCompile(`\b((?:19|20)\d\d)\b`)
	crc        = regexp.MustCompile(`\[([0-9A-Fa-f]{8})\]`)
	groupTail  = regexp.MustCompile(`(?:\s|^)?-\s?([A-Za-z0-9][A-Za-z0-9_.]{1,24})$`)
	bracketTag = regexp.MustCompile(`[\[(][^\])]*[\])]`)
)

// tech is every token that ends a title, with what it says.
var tech = []struct {
	re   *regexp.Regexp
	read func(r *Release, m []string)
}{
	{regexp.MustCompile(`(?i)\b(\d{3,4})[pi]\b`), func(r *Release, m []string) {
		if n, _ := strconv.Atoi(m[1]); n >= 240 && r.Resolution == 0 {
			r.Resolution = n
		}
	}},
	{regexp.MustCompile(`(?i)\b(4K|UHD)\b`), func(r *Release, m []string) {
		if r.Resolution == 0 {
			r.Resolution = 2160
		}
	}},
	{regexp.MustCompile(`(?i)\b8K\b`), func(r *Release, m []string) {
		if r.Resolution == 0 {
			r.Resolution = 4320
		}
	}},
	{regexp.MustCompile(`(?i)\bREMUX\b`), func(r *Release, m []string) { r.Remux = true; setOnce(&r.Source, "bluray") }},
	{regexp.MustCompile(`(?i)\b(Blu-?Ray|BDRip|BRRip|BD(?:25|50)?)\b`), func(r *Release, m []string) { setOnce(&r.Source, "bluray") }},
	{regexp.MustCompile(`(?i)\bWEB-?DL\b`), func(r *Release, m []string) { setOnce(&r.Source, "web-dl") }},
	{regexp.MustCompile(`(?i)\b(WEBRip|WEB)\b`), func(r *Release, m []string) { setOnce(&r.Source, "webrip") }},
	{regexp.MustCompile(`(?i)\b(HDTV|PDTV)\b`), func(r *Release, m []string) { setOnce(&r.Source, "hdtv") }},
	{regexp.MustCompile(`(?i)\bDVD(?:Rip|5|9)?\b`), func(r *Release, m []string) { setOnce(&r.Source, "dvd") }},
	{regexp.MustCompile(`(?i)\b(CR|HIDIVE|Funimation|FUNi)\b`), func(r *Release, m []string) { r.Anime++ }},
	{regexp.MustCompile(`(?i)\b(AMZN|NF|DSNP|HMAX|ATVP|HULU|PCOK|PMTP)\b`), nil},
	{regexp.MustCompile(`(?i)\b(x264|H264|AVC)\b`), func(r *Release, m []string) { setOnce(&r.VideoCodec, "h264") }},
	{regexp.MustCompile(`(?i)\b(x265|H265|HEVC)\b`), func(r *Release, m []string) { setOnce(&r.VideoCodec, "hevc") }},
	{regexp.MustCompile(`(?i)\bAV1\b`), func(r *Release, m []string) { setOnce(&r.VideoCodec, "av1") }},
	{regexp.MustCompile(`(?i)\bVP9\b`), func(r *Release, m []string) { setOnce(&r.VideoCodec, "vp9") }},
	{regexp.MustCompile(`(?i)\b(XviD|DivX)\b`), func(r *Release, m []string) { setOnce(&r.VideoCodec, "mpeg4") }},
	{regexp.MustCompile(`(?i)\b(10|12)[ -]?bits?\b`), func(r *Release, m []string) { r.BitDepth, _ = strconv.Atoi(m[1]) }},
	{regexp.MustCompile(`(?i)\bHi10P?\b`), func(r *Release, m []string) { r.BitDepth = 10 }},
	{regexp.MustCompile(`(?i)\bHDR10(?:\+|Plus)`), func(r *Release, m []string) { add(&r.HDR, "hdr10+") }},
	{hdrPlain, nil},
	{regexp.MustCompile(`(?i)\b(DV|DoVi|Dolby[ ]?Vision)\b`), func(r *Release, m []string) { add(&r.HDR, "dolby-vision") }},
	{regexp.MustCompile(`(?i)\bHLG\b`), func(r *Release, m []string) { add(&r.HDR, "hlg") }},
	{regexp.MustCompile(`(?i)\bDTS-HD[ _.]?MA\b`), func(r *Release, m []string) { add(&r.Audio, "dts-hd ma") }},
	{regexp.MustCompile(`(?i)\bDTS-HD(?:[ _.]?HRA)?\b`), func(r *Release, m []string) { add(&r.Audio, "dts-hd") }},
	{regexp.MustCompile(`(?i)\bDTS[-:]?X\b`), func(r *Release, m []string) { add(&r.Audio, "dts:x") }},
	{regexp.MustCompile(`(?i)\bDTS\b`), func(r *Release, m []string) { add(&r.Audio, "dts") }},
	{regexp.MustCompile(`(?i)\bTrueHD\b`), func(r *Release, m []string) { add(&r.Audio, "truehd") }},
	{regexp.MustCompile(`(?i)\bAtmos\b`), func(r *Release, m []string) { add(&r.Audio, "atmos") }},
	{regexp.MustCompile(`(?i)\b(DDP|EAC3|E-AC-3)|\bDD\+`), func(r *Release, m []string) { add(&r.Audio, "eac3") }},
	{regexp.MustCompile(`(?i)\b(DD|AC3|AC-3)(?:\b|\d)`), func(r *Release, m []string) { add(&r.Audio, "ac3") }},
	{regexp.MustCompile(`(?i)\bAAC`), func(r *Release, m []string) { add(&r.Audio, "aac") }},
	{regexp.MustCompile(`(?i)\bFLAC`), func(r *Release, m []string) { add(&r.Audio, "flac") }},
	{regexp.MustCompile(`(?i)\bOpus`), func(r *Release, m []string) { add(&r.Audio, "opus") }},
	{regexp.MustCompile(`(?i)\bMP3\b`), func(r *Release, m []string) { add(&r.Audio, "mp3") }},
	{regexp.MustCompile(`(?i)\bL?PCM\b`), func(r *Release, m []string) { add(&r.Audio, "pcm") }},
	{regexp.MustCompile(`(?i)\bDual[ -]?Audio\b`), func(r *Release, m []string) { r.DualAudio = true }},
	{regexp.MustCompile(`(?i)\bMulti[ -]?Subs?\b`), func(r *Release, m []string) { r.MultiSub = true }},
	{regexp.MustCompile(`(?i)\bEng(?:lish)?[ -]?Subs?\b`), nil},
	{regexp.MustCompile(`(?i)\b(Director'?s[ ]Cut|Extended(?:[ ]Edition)?|Unrated|Uncut|Theatrical(?:[ ]Cut)?|Final[ ]Cut|IMAX|Remastered|Criterion|Uncropped|Open[ ]Matte)\b`), func(r *Release, m []string) {
		if r.Edition == "" {
			r.Edition = edition(m[1])
		}
	}},
	{regexp.MustCompile(`(?i)\b(REPACK\d?|PROPER|RERIP)\b`), func(r *Release, m []string) { r.Repack = true }},
	{regexp.MustCompile(`(?i)\b(?:AI[ ]?)?Upscaled?\b`), func(r *Release, m []string) { r.Upscaled = true }},
	{regexp.MustCompile(`(?i)\b(COMPLETE|INTERNAL|LIMITED|Batch|Collection)\b`), nil},
	{regexp.MustCompile(`(?i)\b(NCOP|NCED|Creditless)\d*\b`), func(r *Release, m []string) { setOnce(&r.Extra, "creditless") }},
	{regexp.MustCompile(`(?i)\bsample\b`), func(r *Release, m []string) { r.Sample = true }},
}

var hdrPlain = regexp.MustCompile(`(?i)\bHDR(?:10)?\b`)

// languages end a title only where a provider writes them: in capitals, or
// in brackets — so "The Good German" keeps its title.
var languages = regexp.MustCompile(`\b(GERMAN|ENGLISH|FRENCH|ITALIAN|SPANISH|TURKISH|JAPANESE|MULTI|DL|VOSTFR|SUBBED|DUBBED)\b`)

var extraSuffix = regexp.MustCompile(`(?i)-(trailer|featurette|behindthescenes|deleted|interview|scene|short|other)$`)

func setOnce(field *string, value string) {
	if *field == "" {
		*field = value
	}
}

func add(list *[]string, value string) {
	if !slices.Contains(*list, value) {
		*list = append(*list, value)
	}
}

func edition(raw string) string {
	switch k := strings.ToLower(strings.ReplaceAll(raw, "'", "")); {
	case strings.HasPrefix(k, "directors"):
		return "Director's Cut"
	case strings.HasPrefix(k, "extended"):
		return "Extended"
	case strings.HasPrefix(k, "theatrical"):
		return "Theatrical"
	case k == "imax":
		return "IMAX"
	case strings.HasPrefix(k, "final"):
		return "Final Cut"
	case strings.HasPrefix(k, "open"):
		return "Open Matte"
	default:
		return strings.ToUpper(k[:1]) + k[1:]
	}
}

// IsVideo reports whether a file name is a video's.
func IsVideo(name string) bool { return videoExt.MatchString(name) }

// StripCollision drops the ` (233)` Zurg adds to a file's name where two
// files of a torrent share it — every one of them gets it. A lone
// `Film (2019).mkv` keeps its year.
func StripCollision(name string, siblings []string) string {
	m := collision.FindStringSubmatch(name)
	if m == nil {
		return name
	}
	stem, number, ext := m[1], m[2], m[3]
	n, _ := strconv.Atoi(number)
	if len(number) != 4 || n < 1900 || n > time.Now().Year()+1 {
		return stem + ext
	}
	for _, sibling := range siblings {
		if s := collision.FindStringSubmatch(sibling); s != nil && sibling != name && s[1] == stem && s[3] == ext {
			return stem + ext
		}
	}
	return name
}

// Name reads one name: a file's, a folder's or an entry's.
func Name(raw string) Release {
	var r Release
	s := strings.TrimSpace(raw)
	if m := videoExt.FindStringSubmatchIndex(s); m != nil {
		r.Container = strings.ToLower(s[m[2]:m[3]])
		s = s[:m[0]]
	} else if m := otherExt.FindStringIndex(s); m != nil {
		s = s[:m[0]]
	}
	if m := extraSuffix.FindStringSubmatchIndex(s); m != nil {
		r.Extra = strings.ToLower(s[m[2]:m[3]])
		s = s[:m[0]]
	}
	if m := leading.FindStringSubmatchIndex(s); m != nil {
		r.Group = strings.TrimSpace(s[m[2]:m[3]])
		r.Anime++
		s = s[m[1]:]
	}
	if m := crc.FindStringSubmatch(s); m != nil {
		r.CRC = strings.ToUpper(m[1])
	}
	s = separate(s)

	end := len(s)
	cut := func(at int) {
		if at >= 0 && at < end {
			end = at
		}
	}

	// Episode markers, the first that matches.
	if m := seasonEp.FindStringSubmatchIndex(s); m != nil {
		season, _ := strconv.Atoi(s[m[2]:m[3]])
		first, _ := strconv.Atoi(s[m[4]:m[5]])
		r.Seasons = []int{season}
		r.Episodes = []int{first}
		at := m[1]
		for {
			c := more.FindStringSubmatchIndex(s[at:])
			if c == nil {
				break
			}
			var n int
			if c[2] >= 0 {
				n, _ = strconv.Atoi(s[at+c[2] : at+c[3]])
			} else {
				n, _ = strconv.Atoi(s[at+c[4] : at+c[5]])
			}
			last := r.Episodes[len(r.Episodes)-1]
			if n <= last || n > last+50 {
				break
			}
			if c[4] >= 0 || n > last+1 {
				for e := last + 1; e <= n; e++ {
					r.Episodes = append(r.Episodes, e)
				}
			} else {
				r.Episodes = append(r.Episodes, n)
			}
			at += c[1]
		}
		cut(m[0])
		r.EpisodeTitle = episodeTitle(s, m[0], at)
	} else if m := crossEp.FindStringSubmatchIndex(s); m != nil {
		season, _ := strconv.Atoi(s[m[2]:m[3]])
		episode, _ := strconv.Atoi(s[m[4]:m[5]])
		r.Seasons, r.Episodes = []int{season}, []int{episode}
		cut(m[0])
		r.EpisodeTitle = episodeTitle(s, m[0], m[1])
	} else {
		if m := seasonWord.FindStringSubmatchIndex(s); m != nil {
			r.Seasons = seasonRange(s, m)
			cut(m[0])
		} else if m := ordinal.FindStringSubmatchIndex(s); m != nil {
			n, _ := strconv.Atoi(s[m[2]:m[3]])
			r.Seasons = []int{n}
			cut(m[0])
		} else if m := seasonPack.FindStringSubmatchIndex(s); m != nil {
			r.Seasons = seasonRange(s, m)
			cut(m[0])
		}
		if m := episodeWord.FindStringSubmatchIndex(s); m != nil {
			numbers := numberRange(s, m)
			if len(r.Seasons) == 1 {
				r.Episodes = numbers
			} else {
				r.Absolute = numbers
			}
			cut(m[0])
			r.EpisodeTitle = episodeTitle(s, m[0], m[1])
		} else if m := bareEp.FindStringSubmatchIndex(s); m != nil && m[0] > 0 {
			n, _ := strconv.Atoi(s[m[2]:m[3]])
			if len(r.Seasons) == 1 {
				r.Episodes = []int{n}
			} else {
				r.Absolute = []int{n}
			}
			cut(m[0])
		}
	}

	// Anime numbering: a segment after " - " that is a number, or a range.
	if r.Episodes == nil && r.Absolute == nil {
		at := 0
		for i, segment := range strings.Split(s, " - ") {
			if i > 0 {
				trimmed := strings.TrimLeft(segment, " ")
				if m := recap.FindStringSubmatch(trimmed); m != nil {
					r.Special = "recap"
					cut(at - 3)
					break
				}
				if m := absolute.FindStringSubmatch(trimmed); m != nil {
					first, _ := strconv.Atoi(m[1])
					isYear := len(m[1]) == 4 && first >= 1900 && first <= 2099
					if !isYear || r.Group != "" {
						numbers := []int{first}
						if m[2] != "" {
							if last, _ := strconv.Atoi(m[2]); last > first && last <= first+50 {
								for n := first + 1; n <= last; n++ {
									numbers = append(numbers, n)
								}
							}
						}
						if len(r.Seasons) == 1 {
							r.Episodes = numbers
						} else {
							r.Absolute, r.Counted = numbers, true
							r.Anime++
						}
						if m[3] != "" {
							r.Repack = true
						}
						cut(at - 3)
						rest := strings.SplitN(s[at:], " - ", 3)
						if len(rest) > 1 {
							r.EpisodeTitle = trimTitle(cutTech(rest[1]))
						}
						break
					}
				}
			}
			at += len(segment) + 3
		}
	}

	// A pack's range — "(0001-1071+Movies+Specials)" — names what it holds,
	// not what this is.
	masked := s
	if m := packRange.FindStringSubmatchIndex(s); m != nil {
		from, _ := strconv.Atoi(s[m[2]:m[3]])
		to, _ := strconv.Atoi(s[m[4]:m[5]])
		if to > from {
			r.Pack = [2]int{from, to}
			r.Anime++
		}
		cut(m[0])
		masked = s[:m[0]] + strings.Repeat(" ", m[1]-m[0]) + s[m[1]:]
	} else if m := bareRange.FindStringSubmatchIndex(s); m != nil && len(r.Episodes) == 0 && len(r.Absolute) == 0 {
		from, _ := strconv.Atoi(s[m[2]:m[3]])
		to, _ := strconv.Atoi(s[m[4]:m[5]])
		if to > from && (from < 1900 || to < 1900) {
			r.Pack = [2]int{from, to}
			cut(m[0])
			masked = s[:m[0]] + strings.Repeat(" ", m[1]-m[0]) + s[m[1]:]
		}
	}
	if m := seasonSpecial.FindStringSubmatchIndex(masked); m != nil && len(r.Episodes) == 0 {
		r.Special = "special"
		cut(m[0])
		if r.EpisodeTitle == "" {
			r.EpisodeTitle = trimTitle(cutTech(strings.TrimLeft(s[m[1]:], " -")))
		}
	} else if m := special.FindStringSubmatchIndex(masked); m != nil {
		word := strings.ToLower(s[m[2]:m[3]])
		if strings.HasPrefix(word, "special") {
			r.Special = "special"
		} else {
			r.Special = "ova"
		}
		cut(m[0])
		if r.EpisodeTitle == "" {
			r.EpisodeTitle = trimTitle(cutTech(strings.TrimLeft(s[m[1]:], " -")))
		}
	}
	if m := movieNumber.FindStringSubmatchIndex(masked); m != nil && afterNumber.MatchString(strings.TrimLeft(s[m[1]:], " ._")) {
		r.MovieNumber, _ = strconv.Atoi(s[m[2]:m[3]])
		cut(m[0])
		// "Movie 01 (2000) - One Piece the Movie": its own title follows.
		if _, after, found := strings.Cut(s[m[1]:], " - "); found && r.EpisodeTitle == "" {
			r.EpisodeTitle = trimTitle(cutTech(after))
		}
	}
	airAt := -1
	if m := airDate.FindStringSubmatchIndex(s); m != nil {
		r.AirDate = s[m[2]:m[3]] + "-" + s[m[4]:m[5]] + "-" + s[m[6]:m[7]]
		airAt = m[0]
		cut(m[0])
	}

	// What the file is, from anywhere in the name; the first token ends the
	// title.
	firstTech := len(s)
	for _, t := range tech {
		for _, m := range t.re.FindAllStringSubmatchIndex(s, -1) {
			if m[0] == 0 {
				continue
			}
			groups := make([]string, len(m)/2)
			for i := range groups {
				if m[2*i] >= 0 {
					groups[i] = s[m[2*i]:m[2*i+1]]
				}
			}
			if t.read != nil {
				t.read(&r, groups)
			}
			if m[0] < firstTech {
				firstTech = m[0]
			}
		}
	}
	for _, m := range hdrPlain.FindAllStringIndex(s, -1) {
		next := strings.ToLower(s[m[1]:min(m[1]+4, len(s))])
		if m[0] > 0 && !strings.HasPrefix(next, "+") && !strings.HasPrefix(next, "plus") {
			add(&r.HDR, "hdr10")
		}
	}
	r.Audio = slices.DeleteFunc(r.Audio, func(a string) bool {
		switch a {
		case "dts-hd":
			return slices.Contains(r.Audio, "dts-hd ma")
		case "dts":
			return slices.ContainsFunc(r.Audio, func(b string) bool { return b == "dts-hd ma" || b == "dts-hd" || b == "dts:x" })
		case "ac3":
			return slices.Contains(r.Audio, "eac3")
		}
		return false
	})
	if m := languages.FindStringIndex(s); m != nil && m[0] > 0 && m[0] < firstTech {
		firstTech = m[0]
	}
	for _, m := range bracketTag.FindAllStringIndex(s, -1) {
		if m[0] > 0 && m[0] < firstTech {
			firstTech = m[0]
		}
	}
	cut(firstTech)
	if r.Channels == "" {
		r.Channels = channels(s[firstTech:])
	}
	if r.DualAudio || r.MultiSub {
		r.Anime++
	}

	// The year: one written in brackets, or after a dash; else the last bare
	// year before what ends the title, but never the title's first word.
	limit := min(end, firstTech)
	if m := yearMark.FindStringSubmatchIndex(s); m != nil && m[0] > 0 {
		r.Year, _ = strconv.Atoi(s[m[2]:m[3]])
		cut(m[0])
	} else if m := yearDash.FindStringSubmatchIndex(s); m != nil && m[0] > 0 {
		r.Year, _ = strconv.Atoi(s[m[2]:m[3]])
		cut(m[0])
	} else if m := yearSpan.FindStringSubmatchIndex(s); m != nil && m[0] > 0 {
		r.Year, _ = strconv.Atoi(s[m[2]:m[3]])
		cut(m[0])
	} else {
		year, at := 0, -1
		for _, m := range yearBare.FindAllStringSubmatchIndex(s[:min(limit+5, len(s))], -1) {
			if m[0] == 0 || m[0] > limit || m[0] == airAt {
				continue
			}
			n, _ := strconv.Atoi(s[m[2]:m[3]])
			if n > time.Now().Year()+1 {
				continue
			}
			year, at = n, m[0]
		}
		if at > 0 {
			r.Year = year
			title := trimTitle(s[:at])
			if strings.TrimSpace(s[at+4:min(limit, len(s))]) == "" {
				r.Alternate = title + " " + strconv.Itoa(year)
			}
			cut(at)
		}
	}

	r.Title = acronyms(trimTitle(s[:end]))
	if m := country.FindStringSubmatch(r.Title); m != nil && r.Alternate == "" {
		r.Alternate = m[1]
	}
	if r.Title == "" && end < len(s) && r.Group != "" && len(r.Absolute) > 0 {
		r.Title = r.Group
	}
	if m := groupTail.FindStringSubmatchIndex(s); m != nil && m[0] >= end && r.Group == "" {
		candidate := s[m[2]:m[3]]
		if !isTech(candidate) {
			r.Group = candidate
		}
	}
	return r
}

// country is a country a title ends in, for a remake: "The Office US".
var country = regexp.MustCompile(`^(.+) (US|UK|AU|NZ|CA)$`)

// acronyms joins letters a dotted name spelled one by one: "S W A T" was
// "S.W.A.T.".
func acronyms(title string) string {
	words := strings.Split(title, " ")
	var out []string
	for i := 0; i < len(words); {
		j := i
		for j < len(words) && len([]rune(words[j])) == 1 && isLetter(words[j]) {
			j++
		}
		if j-i >= 2 {
			out = append(out, strings.Join(words[i:j], ".")+".")
			i = j
			continue
		}
		out = append(out, words[i])
		i++
	}
	return strings.Join(out, " ")
}

func isLetter(s string) bool {
	r := []rune(s)[0]
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

// separate turns a dotted or underscored name into words, keeping `5.1` and
// `H.264` whole.
func separate(s string) string {
	if !strings.Contains(s, " ") && strings.Contains(s, "_") {
		s = strings.ReplaceAll(s, "_", " ")
	}
	if !strings.Contains(s, " ") && strings.Contains(s, ".") {
		s = codecDot.ReplaceAllString(s, "H$1")
		s = channelDot.ReplaceAllString(s, "${1}_$2")
		s = strings.ReplaceAll(s, ".", " ")
	}
	s = strings.NewReplacer("–", "-", "—", "-").Replace(s)
	return strings.TrimSpace(spaces.ReplaceAllString(s, " "))
}

var channelLayout = regexp.MustCompile(`([1-9])[._ ]([0-2])(?:\b|$|[^0-9])`)

func channels(s string) string {
	if m := channelLayout.FindStringSubmatch(s); m != nil {
		return m[1] + "." + m[2]
	}
	return ""
}

func seasonRange(s string, m []int) []int {
	first, _ := strconv.Atoi(s[m[2]:m[3]])
	seasons := []int{first}
	if m[4] >= 0 {
		if last, _ := strconv.Atoi(s[m[4]:m[5]]); last > first && last <= first+50 {
			for n := first + 1; n <= last; n++ {
				seasons = append(seasons, n)
			}
		}
	}
	return seasons
}

func numberRange(s string, m []int) []int {
	first, _ := strconv.Atoi(s[m[2]:m[3]])
	numbers := []int{first}
	if m[4] >= 0 {
		if last, _ := strconv.Atoi(s[m[4]:m[5]]); last > first && last <= first+50 {
			for n := first + 1; n <= last; n++ {
				numbers = append(numbers, n)
			}
		}
	}
	return numbers
}

// episodeTitle is what follows an episode's marker, up to what the file is:
// "S01E01 - Rose - 1080p" and "S01E01 Rose 1080p" alike.
func episodeTitle(s string, start, end int) string {
	rest := strings.TrimLeft(s[end:], " -")
	if before, _, found := strings.Cut(rest, " - "); found && strings.Contains(s[:start], " - ") {
		rest = before
	}
	return trimTitle(cutTech(rest))
}

// cutTech is s up to its first token that says what the file is.
func cutTech(s string) string {
	end := len(s)
	for _, t := range tech {
		if m := t.re.FindStringIndex(s); m != nil && m[0] < end {
			end = m[0]
		}
	}
	for _, re := range []*regexp.Regexp{bracketTag, yearMark, languages} {
		if m := re.FindStringIndex(s); m != nil && m[0] < end {
			end = m[0]
		}
	}
	return s[:end]
}

func isTech(token string) bool {
	for _, t := range tech {
		if t.re.MatchString(token) {
			return true
		}
	}
	return false
}

// trimTitle drops what a title's end trails: dashes, brackets, the tags a
// provider marks copies with.
func trimTitle(s string) string {
	s = strings.TrimSpace(s)
	for {
		before := s
		s = strings.TrimRight(s, " -_.,:;~+")
		s = strings.TrimSuffix(s, "()")
		s = strings.TrimSuffix(s, "[]")
		s = strings.TrimSpace(s)
		if s == before {
			break
		}
	}
	title, _ := titles.Bare(s)
	if title == "" {
		return s
	}
	return title
}
