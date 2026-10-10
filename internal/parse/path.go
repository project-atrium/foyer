package parse

import (
	"regexp"
	"strconv"
	"strings"
)

// Folder is what a folder inside a torrent holds, by its name.
type Folder string

const (
	Plain    Folder = ""
	Seasonal Folder = "season"
	Specials Folder = "specials"
	Movies   Folder = "movies"
	Extras   Folder = "extras"
	Samples  Folder = "sample"
)

var (
	seasonFolder   = regexp.MustCompile(`(?i)^(?:Season|Series|Staffel|Saison|S)[ ._-]*(\d{1,3})\b`)
	specialsFolder = regexp.MustCompile(`(?i)\b(?:Specials|SPs)\b|^(?:Special|SP|Season[ ._-]*0+|S0+)$`)
	moviesFolder   = regexp.MustCompile(`(?i)\b(?:Movies?|Films?)\b`)
	extrasFolder   = regexp.MustCompile(`(?i)^(?:Extras?|Featurettes?|Behind[ ._-]?The[ ._-]?Scenes|Deleted[ ._-]?Scenes|Interviews?|Trailers?|Shorts?|Bonus|NCOP|NCED|Menus?)\b`)
	samplesFolder  = regexp.MustCompile(`(?i)^Samples?$`)
	// numberedStem is a file named by its number first: "05 - Pilot".
	numberedStem = regexp.MustCompile(`^(\d{1,3})(?:\s*-\s*|\.\s+|\s+)(.*)$`)
)

// Placed is a file read where it lies: its own name filled in from the
// folders it is in and its torrent's name.
type Placed struct {
	Release
	// Torrent is the torrent's name, read: a pack's title and year are there.
	Torrent Release
	// Folder is what the folder nearest the file holds.
	Folder Folder
}

// classify reads a folder's name.
func classify(name string) (Folder, int) {
	switch {
	case specialsFolder.MatchString(name):
		return Specials, 0
	case samplesFolder.MatchString(name):
		return Samples, 0
	case extrasFolder.MatchString(name):
		return Extras, 0
	}
	if m := seasonFolder.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[1])
		return Seasonal, n
	}
	if moviesFolder.MatchString(name) {
		return Movies, 0
	}
	return Plain, 0
}

// Path reads a file under a library folder: path is "<torrent>/<folders…>/
// <file>", as Zurg lists it, or a loose file's name alone. siblings are the
// names beside the file, for Zurg's collision suffix.
func Path(path string, siblings []string) Placed {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	file := StripCollision(segments[len(segments)-1], siblings)
	var torrent string
	var folders []string
	if len(segments) > 1 {
		torrent = segments[0]
		folders = segments[1 : len(segments)-1]
	}

	p := Placed{Release: Name(file)}
	if torrent != "" {
		p.Torrent = Name(torrent)
	}
	season := -1
	var hints []Release
	for _, name := range folders {
		kind, n := classify(name)
		if kind != Plain {
			p.Folder = kind
		}
		if kind == Seasonal {
			season = n
		}
		hints = append(hints, Name(name))
	}

	r := &p.Release
	switch p.Folder {
	case Specials:
		if r.Special == "" {
			r.Special = "special"
		}
	case Samples:
		r.Sample = true
	case Extras:
		if r.Extra == "" {
			r.Extra = "extra"
		}
	}

	// A season folder holds the numbers a file's name gives without a
	// season — "Episode 5", "05 - Pilot" — but never numbers counted from
	// the series' start: a pack's "Season 01" may hold episode 62.
	numbered := len(r.Seasons) == 0 && len(r.Episodes) == 0 && len(r.Absolute) == 0
	if season >= 0 && len(r.Seasons) == 0 {
		switch {
		case len(r.Absolute) > 0 && !r.Counted:
			r.Seasons, r.Episodes, r.Absolute = []int{season}, r.Absolute, nil
		case numbered:
			if m := numberedStem.FindStringSubmatch(strings.TrimSuffix(file, extensionOf(file))); m != nil {
				n, _ := strconv.Atoi(m[1])
				r.Seasons, r.Episodes = []int{season}, []int{n}
				r.EpisodeTitle = trimTitle(cutTech(m[2]))
				r.Title = ""
			}
		}
	}
	// A season pack's one season, where the file names none.
	if len(r.Seasons) == 0 && len(p.Torrent.Seasons) == 1 && len(r.Absolute) > 0 && !r.Counted {
		r.Seasons, r.Episodes, r.Absolute = p.Torrent.Seasons, r.Absolute, nil
	}

	// What the file is: its own name first, then its folders', then the
	// torrent's.
	for _, h := range append(reverse(hints), p.Torrent) {
		if r.Year == 0 && h.Year != 0 && len(h.Seasons) == 0 {
			r.Year = h.Year
		}
		if r.Resolution == 0 {
			r.Resolution = h.Resolution
		}
		if r.Source == "" {
			r.Source = h.Source
		}
		if r.VideoCodec == "" {
			r.VideoCodec = h.VideoCodec
		}
		if r.Edition == "" {
			r.Edition = h.Edition
		}
		if r.BitDepth == 0 {
			r.BitDepth = h.BitDepth
		}
		r.Upscaled = r.Upscaled || h.Upscaled
		r.DualAudio = r.DualAudio || h.DualAudio
		r.MultiSub = r.MultiSub || h.MultiSub
		r.Remux = r.Remux || h.Remux
		for _, hdr := range h.HDR {
			add(&r.HDR, hdr)
		}
		for _, a := range h.Audio {
			add(&r.Audio, a)
		}
	}
	if r.Group == "" {
		r.Group = p.Torrent.Group
	}
	r.Anime = max(r.Anime, p.Torrent.Anime)
	if r.Title == "" {
		r.Title = p.Torrent.Title
	}
	return p
}

func extensionOf(name string) string {
	if m := videoExt.FindStringIndex(name); m != nil {
		return name[m[0]:]
	}
	return ""
}

func reverse(list []Release) []Release {
	out := make([]Release, len(list))
	for i, r := range list {
		out[len(list)-1-i] = r
	}
	return out
}
