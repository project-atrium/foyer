package library

import (
	"fmt"
	"slices"
	"strings"

	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/adapters/tmdb"
	"foyer/internal/parse"
	"foyer/internal/titles"
)

// placedFile is a file waiting for its title: its record, and what its name
// and place say.
type placedFile struct {
	record *core.Record
	entry  string
	placed parse.Placed
}

// role is what a file is to its title.
func (f *placedFile) role(kind string) string {
	p := f.placed
	switch {
	case p.Extra != "" || p.Folder == parse.Extras:
		return "extra"
	case kind == Shows && (p.MovieNumber > 0 || p.Folder == parse.Movies):
		return "extra"
	}
	return "version"
}

// evidence is what a title's files say that a name does not: how far its
// episodes run, and whether it is anime.
type evidence struct {
	maxAbsolute int
	perSeason   map[int]int
	anime       int
}

// candidate is one title a library read, and the files that are it.
type candidate struct {
	kind      tmdb.Kind
	title     string
	year      int
	alternate string
	key       string
	files     []*placedFile
	evidence  evidence
}

// matchKey is what a title is asked of TMDB by, and remembered by.
func matchKey(kind tmdb.Kind, title string, year int) string {
	return fmt.Sprintf("%s|%s|%d", kind, titles.Key(title), year)
}

// plan groups a library's waiting files into titles: a movies library's
// files by their title and year — two entries of one film are one title —
// and a shows library's by their entry, each file an episode, a special
// or an extra of it.
func plan(kind string, files []*placedFile) []*candidate {
	byKey := map[string]*candidate{}
	var order []string
	add := func(c *candidate, f *placedFile) {
		existing, ok := byKey[c.key]
		if !ok {
			byKey[c.key] = c
			order = append(order, c.key)
			existing = c
		}
		existing.files = append(existing.files, f)
		if existing.title == "" {
			existing.title = c.title
		}
		existing.evidence.anime = max(existing.evidence.anime, f.placed.Anime)
	}

	byEntry := map[string][]*placedFile{}
	var entries []string
	for _, f := range files {
		if _, ok := byEntry[f.entry]; !ok {
			entries = append(entries, f.entry)
		}
		byEntry[f.entry] = append(byEntry[f.entry], f)
	}

	for _, entry := range entries {
		group := byEntry[entry]
		if kind == Movies {
			planMovies(group, add)
		} else {
			planShows(group, add)
		}
	}
	candidates := make([]*candidate, 0, len(order))
	for _, key := range order {
		c := byKey[key]
		c.evidence.perSeason = map[int]int{}
		for _, f := range c.files {
			r := f.placed.Release
			for _, n := range r.Absolute {
				c.evidence.maxAbsolute = max(c.evidence.maxAbsolute, n)
			}
			c.evidence.maxAbsolute = max(c.evidence.maxAbsolute, f.placed.Entry.Pack[1])
			if len(r.Seasons) == 1 && r.Seasons[0] >= 1 {
				for _, n := range r.Episodes {
					c.evidence.perSeason[r.Seasons[0]] = max(c.evidence.perSeason[r.Seasons[0]], n)
				}
			}
		}
		candidates = append(candidates, c)
	}
	return candidates
}

// planMovies: an entry's main files are one film, or several where their
// names say several; what came with them is theirs.
func planMovies(group []*placedFile, add func(*candidate, *placedFile)) {
	var main, extras []*placedFile
	for _, f := range group {
		if f.role(Movies) == "extra" {
			extras = append(extras, f)
		} else {
			main = append(main, f)
		}
	}
	var first *candidate
	for _, f := range main {
		title, year, alternate := f.placed.Title, f.placed.Year, f.placed.Alternate
		if len(main) == 1 || title == "" {
			// An entry's own name is the film's, where the file's says less.
			if t := f.placed.Entry; t.Title != "" && (title == "" || len(main) == 1 && year == 0 && t.Year != 0) {
				title, year, alternate = t.Title, t.Year, t.Alternate
			}
		}
		c := &candidate{kind: tmdb.Movie, title: title, year: year, alternate: alternate, key: matchKey(tmdb.Movie, title, year)}
		add(c, f)
		if first == nil {
			first = c
		}
	}
	for _, f := range extras {
		if first == nil {
			// Extras alone: their entry's film, as its name says.
			t := f.placed.Entry
			first = &candidate{kind: tmdb.Movie, title: t.Title, year: t.Year, key: matchKey(tmdb.Movie, t.Title, t.Year)}
		}
		add(first, f)
	}
}

// planShows: one series per entry — the pack's name where it says one,
// else its files' — unless its files name several series of their own.
func planShows(group []*placedFile, add func(*candidate, *placedFile)) {
	entry := group[0].placed.Entry
	byTitle := map[string][]*placedFile{}
	var keys []string
	for _, f := range group {
		key := titles.Key(f.placed.Title)
		if _, ok := byTitle[key]; !ok {
			keys = append(keys, key)
		}
		byTitle[key] = append(byTitle[key], f)
	}
	entryKey := titles.Key(entry.Title)
	related := func(key string) bool {
		return entryKey != "" && (key == "" || strings.Contains(key, entryKey) || strings.Contains(entryKey, key))
	}
	// Several series in one entry only where each holds a fair share of it
	// — numbered episodes, two at least: a pack's "Start Credit (Part 1)" and
	// "(Part 2)" are its own, not a series.
	var own []string
	for _, key := range keys {
		numbered := 0
		for _, f := range byTitle[key] {
			if len(f.placed.Episodes) > 0 || len(f.placed.Absolute) > 0 {
				numbered++
			}
		}
		if !related(key) && key != "" && numbered >= 2 {
			own = append(own, key)
		}
	}
	if len(own) == 0 || entryKey == "" && len(keys) == 1 {
		title, year := entry.Title, entry.Year
		if title == "" {
			title, year = largest(byTitle, keys)
		}
		c := showCandidate(title, year, group)
		for _, f := range group {
			add(c, f)
		}
		return
	}
	for _, key := range keys {
		files := byTitle[key]
		if !slices.Contains(own, key) {
			continue
		}
		c := showCandidate(files[0].placed.Title, files[0].placed.Year, files)
		for _, f := range files {
			add(c, f)
		}
		delete(byTitle, key)
	}
	// What is left belongs to the entry's own series.
	var rest []*placedFile
	for _, key := range keys {
		rest = append(rest, byTitle[key]...)
	}
	if len(rest) > 0 {
		title, year := entry.Title, entry.Year
		if title == "" {
			title, year = rest[0].placed.Title, rest[0].placed.Year
		}
		c := showCandidate(title, year, rest)
		for _, f := range rest {
			add(c, f)
		}
	}
}

// showCandidate is a series to ask TMDB for. A year beside a season past the
// first is the season's, not the series' — and a wrong year finds nothing.
func showCandidate(title string, year int, files []*placedFile) *candidate {
	for _, f := range files {
		for _, s := range f.placed.Seasons {
			if s >= 2 {
				year = 0
			}
		}
		for _, s := range f.placed.Entry.Seasons {
			if s >= 2 {
				year = 0
			}
		}
	}
	return &candidate{kind: tmdb.Show, title: title, year: year, key: matchKey(tmdb.Show, title, year)}
}

func largest(byTitle map[string][]*placedFile, keys []string) (string, int) {
	best := keys[0]
	for _, key := range keys {
		if len(byTitle[key]) > len(byTitle[best]) {
			best = key
		}
	}
	f := byTitle[best][0]
	return f.placed.Title, f.placed.Year
}
