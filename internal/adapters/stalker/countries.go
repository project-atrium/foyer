// Package stalker reaches a Stalker (Ministra) portal as the one MAG box its
// MAC address is: Loge's iptv/stalker adapter ported, so Foyer can be the
// portal's only client. Data in, Go out; everything a portal says is read
// loosely, as the app reads it.
package stalker

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const flagA, flagZ = 0x1f1e6, 0x1f1ff

// isLetter is a letter of a name: Latin, as the app's LETTER is.
func isLetter(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= 'À' && r <= 'Ö' || r >= 'Ø' && r <= 'ö' || r >= 'ø' && r <= 'ÿ' || r >= 'Ā' && r <= 'ſ'
}

// flagOf is a flag at the start of a name, as two regional-indicator
// letters (🇹🇷 is T R): only before any word.
func flagOf(name string) string {
	var letters []byte
	for _, r := range name {
		switch {
		case r >= flagA && r <= flagZ:
			letters = append(letters, byte('A'+r-flagA))
			if len(letters) == 2 {
				return string(letters)
			}
		case isLetter(r), r >= '0' && r <= '9', len(letters) > 0:
			return ""
		}
	}
	return ""
}

// wordsOf is a name's words: whatever is not a letter separates them.
func wordsOf(name string) []string {
	var words []string
	var word strings.Builder
	for _, r := range name {
		if isLetter(r) {
			word.WriteRune(r)
			continue
		}
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	if word.Len() > 0 {
		words = append(words, word.String())
	}
	return words
}

// countryInName is a name's country: a flag, a code standing first, or a
// country's name among its first words.
func countryInName(name string) string {
	if flag := flagOf(name); flag != "" && countryZones[flag] != "" {
		return flag
	}
	words := wordsOf(name)
	if len(words) == 0 {
		return ""
	}
	first := words[0]
	// Only a code written in capitals: "De Wereld" is Dutch, not German.
	if first == strings.ToUpper(first) && !notCountries[first] {
		code := countryAliases[first]
		if code == "" && utf8.RuneCountInString(first) == 2 {
			code = first
		}
		if code != "" && countryZones[code] != "" {
			return code
		}
	}
	upper := words[:min(len(words), 4)]
	for i := range upper {
		upper[i] = strings.ToUpper(upper[i])
	}
	for start := range upper {
		for end := len(upper); end > start; end-- {
			if found := countryNames[strings.Join(upper[start:end], " ")]; found != "" {
				return found
			}
		}
	}
	return ""
}

var guideSuffix = regexp.MustCompile(`(?i)\.([a-z]{2})(?:@[\w.-]+)?$`)

// CountryOf is the country a channel is from, where something about it says
// so: its guide id's ending, the convention XMLTV sources follow (`trt1.tr`,
// `DasErste.de@HD`); else a name — its group's, then its own — that starts
// with a code standing alone (`TR ✨ ULUSAL`, `|DE| SPORT`), a flag, or a
// country's name. Nothing where none of them does.
func CountryOf(guideID string, names ...string) string {
	if m := guideSuffix.FindStringSubmatch(strings.TrimSpace(guideID)); m != nil {
		code := strings.ToUpper(m[1])
		if alias := countryAliases[code]; alias != "" {
			code = alias
		}
		if countryZones[code] != "" {
			return code
		}
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		if found := countryInName(name); found != "" {
			return found
		}
	}
	return ""
}

// CountryZone is a country's one zone, by its code: "" where none is known.
func CountryZone(country string) string { return countryZones[country] }
