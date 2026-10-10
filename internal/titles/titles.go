// Package titles is Loge's way of comparing names, ported: what two
// spellings of one title share, and a title freed of the marks a provider
// adds to its copies. Loge keys watch status by the same answers
// (`@loge/api`'s identity.ts), so these must agree with it to the byte.
package titles

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Key is titleKey: decomposed, its accents dropped, lower case, and every run
// of anything but a–z and 0–9 one space. A title in another script keys to
// nothing, and nothing is asked for it.
func Key(title string) string {
	var b strings.Builder
	space := false
	for _, r := range norm.NFD.String(title) {
		if r >= 0x300 && r <= 0x36f {
			continue
		}
		r = unicode.ToLower(r)
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
			continue
		}
		space = true
	}
	return b.String()
}

// tags are what a provider writes after a title to tell its copies apart.
var tags = map[string]bool{
	"HQ": true, "HD": true, "FHD": true, "UHD": true, "SD": true, "4K": true, "8K": true, "HDR": true, "HDR10": true,
	"DV": true, "3D": true, "NEU": true, "NEW": true, "MULTI": true, "DUAL": true, "SUB": true, "OMU": true, "OV": true,
	"DE": true, "GER": true, "EN": true, "ENG": true, "TR": true, "TUR": true, "FR": true, "ES": true, "IT": true,
	"NL": true, "PL": true, "RU": true, "AR": true, "PT": true, "EXYU": true,
}

// mark is a year written after a title: `(1999)`, `[1999]` or ` - 1999`. The
// last must be followed by a space or the end, which Go's regexp cannot say,
// so Bare checks it.
var mark = regexp.MustCompile(`\((1[89]\d\d|20\d\d)\)|\[(1[89]\d\d|20\d\d)\]|[\s\p{Zs}][-–][\s\p{Zs}](1[89]\d\d|20\d\d)`)

// Bare is bareTitle: the title as written, without the year marked after it
// or the tags trailing it, and the year — 0 when none was marked. A year at
// the very start is part of the title.
func Bare(name string) (string, int) {
	trimmed := strings.TrimFunc(name, unicode.IsSpace)
	at, year := -1, 0
	for from := 0; from < len(trimmed); {
		m := mark.FindStringSubmatchIndex(trimmed[from:])
		if m == nil {
			break
		}
		start, end := from+m[0], from+m[1]
		if m[6] >= 0 {
			if end < len(trimmed) {
				next := []rune(trimmed[end:])[0]
				if !unicode.IsSpace(next) {
					from = start + 1
					continue
				}
			}
		}
		if start > 0 {
			at = start
			for g := 2; g <= 6; g += 2 {
				if m[g] >= 0 {
					year, _ = strconv.Atoi(trimmed[from+m[g] : from+m[g+1]])
					break
				}
			}
		}
		break
	}
	head := trimmed
	if at >= 0 {
		head = trimmed[:at]
	}
	words := strings.Fields(head)
	for len(words) > 1 && tags[words[len(words)-1]] {
		words = words[:len(words)-1]
	}
	return strings.Join(words, " "), year
}

// Plain is plainTitle: Bare's title, keyed.
func Plain(name string) (string, int) {
	title, year := Bare(name)
	return Key(title), year
}
