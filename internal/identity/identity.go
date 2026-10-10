// Package identity is what a thing watched is, apart from any copy of it:
// Loge's watchIdentity (`@loge/api`'s identity.ts), ported. Foyer keys watch
// status by it, so a film's 1080p and 4K files, in any library, are one, and
// Loge and Foyer name it alike.
package identity

import (
	"fmt"
	"strconv"

	"foyer/internal/titles"
)

// Movie is a film by its TMDB id: `tmdb:movie:603`.
func Movie(tmdb int) string { return "tmdb:movie:" + strconv.Itoa(tmdb) }

// Show is a series by its TMDB id: `tmdb:tv:1396`.
func Show(tmdb int) string { return "tmdb:tv:" + strconv.Itoa(tmdb) }

// Episode is an episode by its series' identity, its season and number:
// `tmdb:tv:1396/s1e3`.
func Episode(show string, season, number int) string {
	return fmt.Sprintf("%s/s%de%d", show, season, number)
}

// ByTitle is a title TMDB did not name, by its name and year:
// `title:movie:matrix:1999`, `title:show:breaking bad`. kind is "movie" or
// "show". A name with no letters to key it by is nothing.
func ByTitle(kind, name string, year int) string {
	title, marked := titles.Plain(name)
	if title == "" {
		return ""
	}
	if year == 0 {
		year = marked
	}
	if year == 0 {
		return "title:" + kind + ":" + title
	}
	return "title:" + kind + ":" + title + ":" + strconv.Itoa(year)
}

// Hash is identityHash: sixteen hex digits standing for an identity —
// FNV-1a over its UTF-8, twice, with different offsets. Never a secret, only
// a name.
func Hash(identity string) string {
	pass := func(offset uint32) uint32 {
		hash := offset
		for _, b := range []byte(identity) {
			hash ^= uint32(b)
			hash *= 0x01000193
		}
		return hash
	}
	return fmt.Sprintf("%08x%08x", pass(0x811c9dc5), pass(0x050c5d1f))
}
