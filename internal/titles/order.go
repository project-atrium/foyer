package titles

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"golang.org/x/text/unicode/norm"
)

// Lower is JavaScript's toLowerCase, which Loge's compareItems sorts titles
// by: Go's unicode.ToLower but for the two mappings JavaScript makes and Go
// does not — İ to i and a combining dot above, and a capital sigma at a
// word's end to ς (Unicode's Final_Sigma).
func Lower(s string) string {
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range runes {
		switch {
		case r == 'İ':
			b.WriteString("i̇")
		case r == 'Σ' && finalSigma(runes, i):
			b.WriteRune('ς')
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// finalSigma is Unicode's condition for a capital sigma to lower to ς: a
// cased letter before it and none after it, case-ignorable characters
// between them aside.
func finalSigma(runes []rune, at int) bool {
	before := false
	for i := at - 1; i >= 0; i-- {
		if caseIgnorable(runes[i]) {
			continue
		}
		before = cased(runes[i])
		break
	}
	if !before {
		return false
	}
	for i := at + 1; i < len(runes); i++ {
		if caseIgnorable(runes[i]) {
			continue
		}
		return !cased(runes[i])
	}
	return true
}

func cased(r rune) bool {
	return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) ||
		unicode.Is(unicode.Other_Lowercase, r) || unicode.Is(unicode.Other_Uppercase, r)
}

func caseIgnorable(r rune) bool {
	switch r {
	// Word_Break MidLetter, MidNumLet and Single_Quote.
	case '\'', '.', ':', '·', '·', '՟', '״', '‘', '’', '․', '‧',
		'︓', '﹒', '﹕', '＇', '．', '：':
		return true
	}
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk)
}

// SortKey is the text compareItems orders a title by — its sort title,
// lower-cased as JavaScript lowers it — as text SQLite orders the same way.
// JavaScript compares strings by UTF-16 code units and SQLite by bytes, so
// each unit is four hex digits: 𝔄 (two units, D835 DD04) then sorts before
// ｚ (FF5A), as it does in the app, though its code point is the greater.
func SortKey(text string) string {
	const digits = "0123456789abcdef"
	units := utf16.Encode([]rune(Lower(text)))
	b := make([]byte, 0, len(units)*4)
	for _, u := range units {
		b = append(b, digits[u>>12], digits[u>>8&0xf], digits[u>>4&0xf], digits[u&0xf])
	}
	return string(b)
}

// ReleaseKey is compareItems' release date as text that sorts as it does:
// the date, else January 1 of the year, else nothing — which sorts first,
// as a missing value does.
func ReleaseKey(date string, year int) string {
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if t, err := time.Parse(layout, date); err == nil {
			return t.Format("2006-01-02")
		}
	}
	if year > 0 {
		return fmt.Sprintf("%04d-01-01", year)
	}
	return ""
}

// Rating is a rating as Foyer answers it: to the thousandth, which is what
// RatingKey keeps, so the order on the server is the app's.
func Rating(r float64) float64 {
	return math.Round(r*1000) / 1000
}

// RatingKey is a rating as text that sorts as the number does — nothing for
// none, which sorts first.
func RatingKey(r *float64) string {
	if r == nil {
		return ""
	}
	return fmt.Sprintf("%08.3f", Rating(*r))
}

// Fold is Loge's matching text: decomposed, its accents dropped, lowered —
// so "Pokémon" is found by "pokemon".
func Fold(text string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(text) {
		if r >= 0x300 && r <= 0x36f {
			continue
		}
		b.WriteRune(r)
	}
	return Lower(b.String())
}

// GenreKey is genreKey: one genre across sources, case, accents and spacing
// aside.
func GenreKey(name string) string {
	return strings.Join(strings.Fields(Fold(name)), " ")
}
