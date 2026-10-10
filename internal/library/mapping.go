package library

import (
	"slices"
	"strings"

	"foyer/internal/adapters/tmdb"
	"foyer/internal/parse"
	"foyer/internal/titles"
)

// catalog is a series' episodes as TMDB has them: by season, each by its
// own number, and the series' absolute ordering where it has one.
type catalog struct {
	seasons  map[int]map[int]tmdb.EpisodeInfo
	regular  []int
	absolute []tmdb.EpisodeRef
}

func newCatalog(seasons []tmdb.Season, absolute []tmdb.EpisodeRef) *catalog {
	c := &catalog{seasons: map[int]map[int]tmdb.EpisodeInfo{}, absolute: absolute}
	for _, s := range seasons {
		episodes := map[int]tmdb.EpisodeInfo{}
		for _, e := range s.Episodes {
			episodes[e.Number] = e
		}
		c.seasons[s.Number] = episodes
		if s.Number >= 1 && len(episodes) > 0 {
			c.regular = append(c.regular, s.Number)
		}
	}
	slices.Sort(c.regular)
	return c
}

func (c *catalog) episode(season, number int) (tmdb.EpisodeInfo, bool) {
	e, ok := c.seasons[season][number]
	return e, ok
}

func (c *catalog) bounds(season int) (first, last, count int) {
	episodes := c.seasons[season]
	first = -1
	for n := range episodes {
		if first < 0 || n < first {
			first = n
		}
		last = max(last, n)
	}
	return first, last, len(episodes)
}

// absoluteInSeason says TMDB numbers this series' episodes from its start,
// season after season — One Piece's season 21 is 892 to 1088.
func (c *catalog) absoluteInSeason() bool {
	runs := 0
	for i := 1; i < len(c.regular); i++ {
		_, last, _ := c.bounds(c.regular[i-1])
		first, _, _ := c.bounds(c.regular[i])
		if first == last+1 && first > 1 {
			runs++
		}
	}
	return runs >= 1
}

// byAbsolute is where an episode counted from the series' start lies: in
// the season TMDB numbers it in, else in TMDB's absolute ordering, else
// counted season by season.
func (c *catalog) byAbsolute(n int) (season, number int, by string, ok bool) {
	if n < 1 {
		return 0, 0, "", false
	}
	if c.absoluteInSeason() {
		for _, s := range c.regular {
			if _, found := c.seasons[s][n]; found {
				return s, n, "absolute", true
			}
		}
	}
	if n <= len(c.absolute) {
		ref := c.absolute[n-1]
		if _, found := c.episode(ref.Season, ref.Number); found {
			return ref.Season, ref.Number, "group", true
		}
	}
	sum := 0
	for _, s := range c.regular {
		first, _, count := c.bounds(s)
		if n <= sum+count {
			return s, first + n - sum - 1, "cumulative", true
		}
		sum += count
	}
	return 0, 0, "", false
}

// special is the season-0 episode a special's name begins: "Episode of Nami"
// is TMDB's "Episode of Nami: Tears of a Navigator…". A name may also be
// the head of TMDB's, before its colon or dash — "3D2Y", too short to trust
// as a beginning, is "3D2Y: Overcome Ace's Death!…" — or share its head,
// where the rest is spelt apart: "Episode of East Blue - …Crewmates's…".
// One answer, or none.
func (c *catalog) special(title string) (int, bool) {
	key, head := titles.Key(title), titles.Key(headOf(title))
	found, hits := 0, 0
	for number, e := range c.seasons[0] {
		name, nameHead := titles.Key(e.Name), titles.Key(headOf(e.Name))
		switch {
		case len(key) >= 6 && (name == key || strings.HasPrefix(name, key+" ") || strings.HasPrefix(key, name+" ")):
		case len(key) >= 4 && key == nameHead:
		case len(head) >= 6 && head != key && head == nameHead:
		default:
			continue
		}
		found = number
		hits++
	}
	return found, hits == 1
}

// headOf is a title before its first colon or spaced dash.
func headOf(title string) string {
	end := len(title)
	for _, sep := range []string{":", " - ", " – ", " — "} {
		if i := strings.Index(title, sep); i > 0 && i < end {
			end = i
		}
	}
	return strings.TrimSpace(title[:end])
}

// placement is where a file lies in its series: a season and an episode,
// the last episode a file of several holds, and how it was found.
type placement struct {
	season, episode, end int
	absolute             int
	by                   string
}

// place lays a file on its series' episodes. Without a catalog — a series
// TMDB did not name — its numbers are taken as they are: a number counted
// from the start is season 1's.
func place(c *catalog, r parse.Release) (placement, bool) {
	if c == nil {
		switch {
		case len(r.Seasons) == 1 && len(r.Episodes) > 0:
			return placement{season: r.Seasons[0], episode: r.Episodes[0], end: endOf(r.Episodes), by: "unknown"}, true
		case len(r.Absolute) > 0:
			return placement{season: 1, episode: r.Absolute[0], end: endOf(r.Absolute), absolute: r.Absolute[0], by: "unknown"}, true
		}
		return placement{}, false
	}
	if len(r.Seasons) == 1 && len(r.Episodes) > 0 {
		s, e := r.Seasons[0], r.Episodes[0]
		if _, ok := c.episode(s, e); ok {
			return placement{season: s, episode: e, end: c.endWithin(s, r.Episodes), by: "release"}, true
		}
		if s >= 1 && c.absoluteInSeason() {
			first, _, count := c.bounds(s)
			if first > 0 && e <= count {
				if _, ok := c.episode(s, first+e-1); ok {
					return placement{season: s, episode: first + e - 1, by: "offset"}, true
				}
			}
		}
		// A number past its season's end is counted from the series' start, as
		// some releases number a long series. A season TMDB does not have yet
		// is left for TMDB to catch up with.
		if _, _, count := c.bounds(s); s >= 1 && count > 0 && e > count {
			if season, number, by, ok := c.byAbsolute(e); ok {
				return placement{season: season, episode: number, absolute: e, by: by}, true
			}
		}
		return placement{}, false
	}
	if len(r.Absolute) > 0 {
		if season, number, by, ok := c.byAbsolute(r.Absolute[0]); ok {
			p := placement{season: season, episode: number, absolute: r.Absolute[0], by: by}
			if len(r.Absolute) > 1 {
				if s2, n2, _, ok := c.byAbsolute(r.Absolute[len(r.Absolute)-1]); ok && s2 == season && n2 > number {
					p.end = n2
				}
			}
			return p, true
		}
		return placement{}, false
	}
	if r.Special != "" && r.EpisodeTitle != "" {
		if number, ok := c.special(r.EpisodeTitle); ok {
			return placement{season: 0, episode: number, by: "special"}, true
		}
	}
	return placement{}, false
}

// endWithin is the last episode a file of several holds, where it lies in
// the same season.
func (c *catalog) endWithin(season int, episodes []int) int {
	if len(episodes) < 2 {
		return 0
	}
	last := episodes[len(episodes)-1]
	if _, ok := c.episode(season, last); ok {
		return last
	}
	return 0
}

func endOf(numbers []int) int {
	if len(numbers) < 2 {
		return 0
	}
	return numbers[len(numbers)-1]
}
