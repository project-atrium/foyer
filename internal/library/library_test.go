package library

import (
	"testing"

	"foyer/internal/adapters/tmdb"
	"foyer/internal/parse"
)

func season(number int, episodes ...int) tmdb.Season {
	s := tmdb.Season{Number: number}
	for _, n := range episodes {
		s.Episodes = append(s.Episodes, tmdb.EpisodeInfo{Season: number, Number: n})
	}
	return s
}

func span(from, to int) []int {
	var n []int
	for i := from; i <= to; i++ {
		n = append(n, i)
	}
	return n
}

func TestPlacesEpisodesWhereTMDBHasThem(t *testing.T) {
	// One Piece's way: each season's episodes run on from the last's.
	absolute := newCatalog([]tmdb.Season{season(1, span(1, 61)...), season(2, span(62, 77)...), season(0, 1, 2)}, nil)
	absolute.seasons[0][7] = tmdb.EpisodeInfo{Season: 0, Number: 7, Name: "Episode of Nami: Tears of a Navigator"}
	absolute.seasons[0][11] = tmdb.EpisodeInfo{Season: 0, Number: 11, Name: "3D2Y: Overcome Ace's Death! Luffy's Vow to his Friends"}
	absolute.seasons[0][15] = tmdb.EpisodeInfo{Season: 0, Number: 15, Name: "Episode of East Blue - Luffy and His 4 Crewmates' Big Adventure"}
	absolute.seasons[0][22] = tmdb.EpisodeInfo{Season: 0, Number: 22, Name: "Recapping Fierce Fights! Straw Hats vs. Tobi Roppo"}
	absolute.seasons[0][23] = tmdb.EpisodeInfo{Season: 0, Number: 23, Name: "Recapping Fierce Fights! Zoro vs. a Lead Performer!"}
	// Most series' way: each season from one.
	perSeason := newCatalog([]tmdb.Season{season(1, span(1, 10)...), season(2, span(1, 10)...)}, nil)
	// A series whose absolute ordering TMDB keeps apart.
	grouped := newCatalog([]tmdb.Season{season(1, span(1, 10)...), season(2, span(1, 10)...)},
		[]tmdb.EpisodeRef{{Season: 1, Number: 1}, {Season: 2, Number: 5}})

	for _, c := range []struct {
		name    string
		cat     *catalog
		release parse.Release
		want    placement
		ok      bool
	}{
		{"absolute in its season", absolute, parse.Release{Absolute: []int{70}}, placement{season: 2, episode: 70, absolute: 70, by: "absolute"}, true},
		{"a release's own numbers", perSeason, parse.Release{Seasons: []int{2}, Episodes: []int{3}}, placement{season: 2, episode: 3, by: "release"}, true},
		{"a season's own number, TMDB counting on", absolute, parse.Release{Seasons: []int{2}, Episodes: []int{3}}, placement{season: 2, episode: 64, by: "offset"}, true},
		{"counted season by season", perSeason, parse.Release{Absolute: []int{13}}, placement{season: 2, episode: 3, absolute: 13, by: "cumulative"}, true},
		{"TMDB's own ordering", grouped, parse.Release{Absolute: []int{2}}, placement{season: 2, episode: 5, absolute: 2, by: "group"}, true},
		{"past its season's end, from the start", perSeason, parse.Release{Seasons: []int{1}, Episodes: []int{13}}, placement{season: 2, episode: 3, absolute: 13, by: "cumulative"}, true},
		{"a season TMDB has not got", perSeason, parse.Release{Seasons: []int{3}, Episodes: []int{1}}, placement{}, false},
		{"a special by its name", absolute, parse.Release{Special: "special", EpisodeTitle: "Episode of Nami"}, placement{season: 0, episode: 7, by: "special"}, true},
		{"a special by too short a name", absolute, parse.Release{Special: "special", EpisodeTitle: "Nami"}, placement{}, false},
		{"a special by the head of TMDB's name", absolute, parse.Release{Special: "special", EpisodeTitle: "3D2Y"}, placement{season: 0, episode: 11, by: "special"}, true},
		{"a special sharing TMDB's head", absolute, parse.Release{Special: "special", EpisodeTitle: "Episode of East Blue - Luffy and His 4 Crewmates's Big Adventure"}, placement{season: 0, episode: 15, by: "special"}, true},
		{"a special's name two of TMDB's begin", absolute, parse.Release{Special: "special", EpisodeTitle: "Recapping Fierce Fights"}, placement{}, false},
		{"several episodes in one file", perSeason, parse.Release{Seasons: []int{1}, Episodes: []int{1, 2}}, placement{season: 1, episode: 1, end: 2, by: "release"}, true},
		{"no series to look in", nil, parse.Release{Absolute: []int{5}}, placement{season: 1, episode: 5, absolute: 5, by: "unknown"}, true},
	} {
		got, ok := place(c.cat, c.release)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: %+v %v, want %+v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestDecidesAmongSeveralOfOneName(t *testing.T) {
	anime := candidateInfo{ID: 37854, Episodes: 1180, Seasons: map[int]int{1: 61, 2: 16}, Anime: true}
	live := candidateInfo{ID: 111110, Episodes: 17, Seasons: map[int]int{1: 8, 2: 9}}
	for _, c := range []struct {
		name  string
		kind  tmdb.Kind
		infos []candidateInfo
		ev    evidence
		want  decision
	}{
		{"one has room for the episodes", tmdb.Show, []candidateInfo{live, anime}, evidence{maxAbsolute: 1071},
			decision{id: 37854, state: sure, reason: "episodes"}},
		{"an anime release, one anime", tmdb.Show, []candidateInfo{live, anime}, evidence{anime: 2},
			decision{id: 37854, state: sure, reason: "anime"}},
		{"a weak anime mark is a guess", tmdb.Show, []candidateInfo{live, anime}, evidence{anime: 1},
			decision{id: 37854, state: uncertain, reason: "anime"}},
		{"nothing tells them apart", tmdb.Show, []candidateInfo{live, anime}, evidence{perSeason: map[int]int{1: 3}},
			decision{id: 111110, state: uncertain, reason: "best guess: shows of one name"}},
		{"films of one name", tmdb.Movie, []candidateInfo{{ID: 1}, {ID: 2}}, evidence{},
			decision{id: 1, state: uncertain, reason: "best guess: movies of one name"}},
	} {
		if got := decide(c.kind, c.infos, c.ev); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestAYearBesideALaterSeasonIsNotTheSeries(t *testing.T) {
	files := []*placedFile{{torrent: "Show.2021.S03", placed: parse.Placed{Release: parse.Release{Title: "Show", Year: 2021, Seasons: []int{3}, Episodes: []int{1}}}}}
	files[0].placed.Torrent = parse.Release{Title: "Show", Year: 2021, Seasons: []int{3}}
	got := plan(Shows, files)
	if len(got) != 1 || got[0].year != 0 || got[0].key != "show|show|0" {
		t.Fatalf("%+v", got[0])
	}
}

func TestTwoTorrentsOfOneFilmAreOneTitle(t *testing.T) {
	film := func(torrent string) *placedFile {
		return &placedFile{torrent: torrent, placed: parse.Placed{Release: parse.Release{Title: "Some Film", Year: 2022}}}
	}
	got := plan(Movies, []*placedFile{film("a"), film("b")})
	if len(got) != 1 || len(got[0].files) != 2 {
		t.Fatalf("%d candidates", len(got))
	}
}
