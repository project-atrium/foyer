package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	apiKey = "0123456789abcdef0123456789abcdef"
	token  = "eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiJ0ZXN0In0.c2lnbmF0dXJl"
)

// fake is TMDB's routes as a test writes them: "GET /3/search/movie" → a
// handler. It records every request.
type fake struct {
	t        *testing.T
	mu       sync.Mutex
	routes   map[string]func(r *http.Request) (int, any)
	requests []*http.Request
}

func newFake(t *testing.T, routes map[string]func(r *http.Request) (int, any)) (*fake, *Client, func(key string) *Client) {
	f := &fake{t: t, routes: routes}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r)
		f.mu.Unlock()
		route, ok := f.routes[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status_code":34}`))
			return
		}
		status, body := route(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)
	client := func(key string) *Client {
		return New(key, WithBase(server.URL+"/3", server.URL+"/t/p"), WithBackoff(time.Millisecond))
	}
	return f, client(apiKey), client
}

func (f *fake) to(route string) []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	var matched []*http.Request
	for _, r := range f.requests {
		if r.Method+" "+r.URL.Path == route {
			matched = append(matched, r)
		}
	}
	return matched
}

func (f *fake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func answer(body any) func(*http.Request) (int, any) {
	return func(*http.Request) (int, any) { return http.StatusOK, body }
}

func status(code int) func(*http.Request) (int, any) {
	return func(*http.Request) (int, any) { return code, map[string]any{"status_code": 7} }
}

func film(id int, title, date string, original ...string) map[string]any {
	o := title
	if len(original) > 0 {
		o = original[0]
	}
	return map[string]any{"id": id, "title": title, "original_title": o, "release_date": date}
}

func series(id int, name, date string) map[string]any {
	return map[string]any{"id": id, "name": name, "original_name": name, "first_air_date": date}
}

// searching answers a search by the term asked for, and by year where one is
// asked, as TMDB does.
func searching(table map[string][]map[string]any) func(*http.Request) (int, any) {
	return func(r *http.Request) (int, any) {
		year := r.URL.Query().Get("year") + r.URL.Query().Get("first_air_date_year")
		var results []map[string]any
		for _, result := range table[r.URL.Query().Get("query")] {
			date, _ := result["release_date"].(string)
			if date == "" {
				date, _ = result["first_air_date"].(string)
			}
			if year == "" || strings.HasPrefix(date, year) {
				results = append(results, result)
			}
		}
		return http.StatusOK, map[string]any{"page": 1, "results": results}
	}
}

func find(t *testing.T, c *Client, q Query) FindResult {
	t.Helper()
	result, err := c.Find(context.Background(), q)
	if err != nil {
		t.Fatalf("Find(%+v): %v", q, err)
	}
	return result
}

// --- The key ------------------------------------------------------------------

func TestAnAPIKeyGoesInTheQueryAndNothingInAHeader(t *testing.T) {
	f, c, _ := newFake(t, map[string]func(*http.Request) (int, any){"GET /3/authentication": answer(map[string]any{"success": true})})
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := f.to("GET /3/authentication")[0]
	if r.URL.Query().Get("api_key") != apiKey || r.Header.Get("Authorization") != "" {
		t.Fatalf("query %q, authorization %q", r.URL.RawQuery, r.Header.Get("Authorization"))
	}
}

func TestATokenGoesAsABearerHeaderAndNeverInTheQuery(t *testing.T) {
	f, _, client := newFake(t, map[string]func(*http.Request) (int, any){"GET /3/authentication": answer(map[string]any{"success": true})})
	if err := client("  " + token + "\n").Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := f.to("GET /3/authentication")[0]
	if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Query().Has("api_key") {
		t.Fatalf("query %q, authorization %q", r.URL.RawQuery, r.Header.Get("Authorization"))
	}
}

func TestARefusedKeyStaysRefusedAndAsksNothingMore(t *testing.T) {
	f, c, _ := newFake(t, map[string]func(*http.Request) (int, any){"GET /3/authentication": status(http.StatusUnauthorized)})
	if err := c.Check(context.Background()); !errors.Is(err, ErrRefused) {
		t.Fatalf("check: %v", err)
	}
	if _, err := c.Find(context.Background(), Query{Kind: Movie, Title: "The Matrix", Year: 1999}); !errors.Is(err, ErrRefused) {
		t.Fatalf("find after a refusal: %v", err)
	}
	if f.count() != 1 {
		t.Fatalf("%d requests", f.count())
	}
}

func TestNoKeyAsksNothing(t *testing.T) {
	f, _, client := newFake(t, nil)
	if err := client("").Check(context.Background()); !errors.Is(err, ErrNoKey) {
		t.Fatalf("check: %v", err)
	}
	if f.count() != 0 {
		t.Fatalf("%d requests", f.count())
	}
}

// --- What a film is: Loge's cases, ported ---------------------------------------

func TestFindsAFilmByItsTitleAndYearWithoutAdultTitles(t *testing.T) {
	f, c, _ := newFake(t, map[string]func(*http.Request) (int, any){
		"GET /3/search/movie": searching(map[string][]map[string]any{"The Matrix": {film(603, "The Matrix", "1999-03-30"), film(604, "The Matrix Reloaded", "2003-05-15")}}),
	})
	if got := find(t, c, Query{Kind: Movie, Title: "The Matrix", Year: 1999}); got.ID != 603 {
		t.Fatalf("got %+v", got)
	}
	q := f.to("GET /3/search/movie")[0].URL.Query()
	if q.Get("query") != "The Matrix" || q.Get("include_adult") != "false" || q.Get("year") != "1999" {
		t.Fatalf("query %v", q)
	}
}

func TestAsksByTheOriginalTitleFirst(t *testing.T) {
	f, c, _ := newFake(t, map[string]func(*http.Request) (int, any){
		"GET /3/search/movie": searching(map[string][]map[string]any{"Matrix": {film(603, "The Matrix", "1999-03-30", "Matrix")}}),
	})
	if got := find(t, c, Query{Kind: Movie, Title: "Matrix DE", OriginalTitle: "Matrix", Year: 1999}); got.ID != 603 {
		t.Fatalf("got %+v", got)
	}
	if asked := f.to("GET /3/search/movie"); len(asked) != 1 || asked[0].URL.Query().Get("query") != "Matrix" {
		t.Fatalf("%d searches", len(asked))
	}
}

func TestMatchesWithoutCaseAccentsOrPunctuation(t *testing.T) {
	_, c, _ := newFake(t, map[string]func(*http.Request) (int, any){
		"GET /3/search/movie": searching(map[string][]map[string]any{"amelie": {film(194, "Amélie", "2001-04-25", "Le Fabuleux Destin d’Amélie Poulain")}}),
	})
	if got := find(t, c, Query{Kind: Movie, Title: "amelie", Year: 2001}); got.ID != 194 {
		t.Fatalf("got %+v", got)
	}
}

// A release's name drops what TMDB's keeps: found all the same, once the
// exact names find nothing — and never another year's film.
func TestForgivesWhatAReleasesNameDrops(t *testing.T) {
	for _, c := range []struct {
		release, tmdb string
		year          int
	}{
		{"Dont Be a Menace to South Central While Drinking Your Juice in the Hood", "Don't Be a Menace to South Central While Drinking Your Juice in the Hood", 1996},
		{"Pirates of the Caribbean Dead Mans Chest", "Pirates of the Caribbean: Dead Man's Chest", 2006},
		{"Gordon Gino and Fred Road Trip", "Gordon, Gino and Fred's Road Trip", 2018},
		{"Accountant 2", "The Accountant²", 2025},
		{"Fast and Furious", "Fast & Furious", 2009},
	} {
		_, client, _ := newFake(t, map[string]func(*http.Request) (int, any){
			"GET /3/search/movie": searching(map[string][]map[string]any{c.release: {film(7, c.tmdb, strconv.Itoa(c.year)+"-06-01")}}),
		})
		if got := find(t, client, Query{Kind: Movie, Title: c.release, Year: c.year}); got.ID != 7 {
			t.Errorf("%q is not %q: %+v", c.release, c.tmdb, got)
		}
		if got := find(t, client, Query{Kind: Movie, Title: c.release, Year: c.year + 5}); got.ID != 0 {
			t.Errorf("%q took another year's film", c.release)
		}
	}
}

func TestTakesAFilmAYearEitherSideAskingForAnyYearWhenTheYearFindsNone(t *testing.T) {
	f, c, _ := newFake(t, map[string]func(*http.Request) (int, any){
		"GET /3/search/movie": searching(map[string][]map[string]any{"Parasite": {film(496243, "Parasite", "2019-05-30")}}),
	})
	if got := find(t, c, Query{Kind: Movie, Title: "Parasite", Year: 2020}); got.ID != 496243 {
		t.Fatalf("got %+v", got)
	}
	asked := f.to("GET /3/search/movie")
	if len(asked) != 2 || asked[0].URL.Query().Get("year") != "2020" || asked[1].URL.Query().Has("year") {
		t.Fatalf("%d searches", len(asked))
	}
}

func TestRefusesAFilmOfAnotherYearAndNeverAsksItsNames(t *testing.T) {
	f, c, _ := newFake(t, map[string]func(*http.Request) (int, any){
		"GET /3/search/movie": searching(map[string][]map[string]any{"The Thing": {film(1091, "The Thing", "1982-06-25")}}),
	})
	if got := find(t, c, Query{Kind: Movie, Title: "The Thing", Year: 2011}); got.ID != 0 {
		t.Fatalf("got %+v", got)
	}
	if len(f.to("GET /3/movie/1091")) != 0 {
		t.Fatal("asked its names")
	}
}

func TestKnowsAFilmByItsNameInAnotherLanguageOnceItsTranslationsSaySo(t *testing.T) {
	f, c, _ := newFake(t, map[string]func(*http.Request) (int, any){
		"GET /3/search/movie": searching(map[string][]map[string]any{"Der Pate": {film(238, "The Godfather", "1972-03-14")}}),
		"GET /3/movie/238": answer(map[string]any{
			"id": 238, "title": "The Godfather", "original_title": "The Godfather",
			"translations":       map[string]any{"translations": []any{map[string]any{"iso_639_1": "de", "data": map[string]any{"title": "Der Pate"}}}},
			"alternative_titles": map[string]any{"titles": []any{map[string]any{"iso_3166_1": "US", "title": "Mario Puzo’s The Godfather"}}},
		}),
	})
	if got := find(t, c, Query{Kind: Movie, Title: "Der Pate", Year: 1972}); got.ID != 238 {
		t.Fatalf("got %+v", got)
	}
	if q := f.to("GET /3/movie/238")[0].URL.Query(); q.Get("append_to_response") != "translations,alternative_titles" {
		t.Fatalf("query %v", q)
	}
}

func TestTakesNothingASearchFoundByANameTheFilmDoesNotHave(t *testing.T) {
	f, c, _ := newFake(t, map[string]func(*http.Request) (int, any){
		"GET /3/search/movie": searching(map[string][]map[string]any{"Das Boot": {film(1, "The Ship", "1981-09-17"), film(2, "Boats", "1981-01-01"), film(3, "Sea", "1981-02-02")}}),
		"GET /3/movie/1":      answer(map[string]any{"title": "The Ship", "translations": map[string]any{"translations": []any{}}}),
		"GET /3/movie/2":      answer(map[string]any{"title": "Boats"}),
	})
	if got := find(t, c, Query{Kind: Movie, Title: "Das Boot", Year: 1981}); got.ID != 0 {
		t.Fatalf("got %+v", got)
	}
	// The best two of the year, and no more.
	if len(f.to("GET /3/movie/1")) != 1 || len(f.to("GET /3/movie/2")) != 1 || len(f.to("GET /3/movie/3")) != 0 {
		t.Fatal("asked the wrong names")
	}
}

// Loge answers nothing for several films of one name and no year; Foyer's
// library is told which, and decides.
func TestHandsOverSeveralOfOneNameWhenNoYearTellsThemApart(t *testing.T) {
	_, c, _ := newFake(t, map[string]func(*http.Request) (int, any){
		"GET /3/search/movie": searching(map[string][]map[string]any{"Dune": {film(438631, "Dune", "2021-09-15"), film(841, "Dune", "1984-12-14"), film(693134, "Dune: Part Two", "2024-02-27")}}),
	})
	got := find(t, c, Query{Kind: Movie, Title: "Dune"})
	if got.ID != 0 || len(got.Named) != 2 || got.Named[0].ID != 438631 || got.Named[1].ID != 841 {
		t.Fatalf("got %+v", got)
	}
	if got := find(t, c, Query{Kind: Movie, Title: "Dune", Year: 1984}); got.ID != 841 {
		t.Fatalf("with a year: %+v", got)
	}
}

func TestAsksNothingForATitleWithNoLettersItCanCompare(t *testing.T) {
	f, c, _ := newFake(t, nil)
	if got := find(t, c, Query{Kind: Movie, Title: "Брат", Year: 1997}); got.ID != 0 || got.Named != nil {
		t.Fatalf("got %+v", got)
	}
	if f.count() != 0 {
		t.Fatalf("%d requests", f.count())
	}
}

// --- What a series is -------------------------------------------------------------

func TestSearchesSeriesByTheirFirstAirDate(t *testing.T) {
	f, c, _ := newFake(t, map[string]func(*http.Request) (int, any){
		"GET /3/search/tv": searching(map[string][]map[string]any{"Breaking Bad": {series(1396, "Breaking Bad", "2008-01-20")}}),
	})
	if got := find(t, c, Query{Kind: Show, Title: "Breaking Bad", Year: 2008}); got.ID != 1396 {
		t.Fatalf("got %+v", got)
	}
	if q := f.to("GET /3/search/tv")[0].URL.Query(); q.Get("first_air_date_year") != "2008" {
		t.Fatalf("query %v", q)
	}
}

func TestReadsASeriesNamesInOtherLanguagesFromItsOwnFields(t *testing.T) {
	_, c, _ := newFake(t, map[string]func(*http.Request) (int, any){
		"GET /3/search/tv": searching(map[string][]map[string]any{"Haus des Geldes": {series(71446, "Money Heist", "2017-05-02")}}),
		"GET /3/tv/71446": answer(map[string]any{
			"name": "Money Heist", "original_name": "La casa de papel",
			"translations":       map[string]any{"translations": []any{map[string]any{"iso_639_1": "en", "data": map[string]any{"name": "Money Heist"}}}},
			"alternative_titles": map[string]any{"results": []any{map[string]any{"iso_3166_1": "DE", "title": "Haus des Geldes"}}},
		}),
	})
	if got := find(t, c, Query{Kind: Show, Title: "Haus des Geldes", Year: 2017}); got.ID != 71446 {
		t.Fatalf("got %+v", got)
	}
}

// --- Errors -------------------------------------------------------------------------

func TestWaitsWhenTMDBCountsTooManyRequests(t *testing.T) {
	calls := 0
	_, c, _ := newFake(t, map[string]func(*http.Request) (int, any){
		"GET /3/search/movie": func(r *http.Request) (int, any) {
			calls++
			if calls < 3 {
				return http.StatusTooManyRequests, map[string]any{"status_code": 25}
			}
			return http.StatusOK, map[string]any{"results": []any{film(949, "Heat", "1995-12-15")}}
		},
	})
	if got := find(t, c, Query{Kind: Movie, Title: "Heat", Year: 1995}); got.ID != 949 {
		t.Fatalf("got %+v", got)
	}
}

func TestGivesUpOnASearchTMDBKeepsRefusingAsBusy(t *testing.T) {
	_, c, _ := newFake(t, map[string]func(*http.Request) (int, any){"GET /3/search/movie": status(http.StatusTooManyRequests)})
	if _, err := c.Find(context.Background(), Query{Kind: Movie, Title: "Heat", Year: 1995}); !errors.Is(err, ErrBusy) {
		t.Fatalf("got %v", err)
	}
}

func TestAsksAgainAfterAFailureOnTMDBsSide(t *testing.T) {
	calls := 0
	_, c, _ := newFake(t, map[string]func(*http.Request) (int, any){
		"GET /3/search/movie": func(r *http.Request) (int, any) {
			calls++
			if calls == 1 {
				return http.StatusBadGateway, nil
			}
			return http.StatusOK, map[string]any{"results": []any{film(949, "Heat", "1995-12-15")}}
		},
	})
	if got := find(t, c, Query{Kind: Movie, Title: "Heat", Year: 1995}); got.ID != 949 {
		t.Fatalf("got %+v", got)
	}
}

func TestReadsAnAnswerItCannotUseAsNoResult(t *testing.T) {
	_, c, _ := newFake(t, map[string]func(*http.Request) (int, any){
		"GET /3/search/movie": answer(map[string]any{"results": []any{map[string]any{"id": "x", "title": "Heat"}, nil, map[string]any{"title": "Heat"}}}),
	})
	if got := find(t, c, Query{Kind: Movie, Title: "Heat", Year: 1995}); got.ID != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestTakesAFilmGoneBetweenTheSearchAndItsNamesAsUnnamed(t *testing.T) {
	_, c, _ := newFake(t, map[string]func(*http.Request) (int, any){
		"GET /3/search/movie": searching(map[string][]map[string]any{"Das Boot": {film(387, "The Boat", "1981-09-17")}}),
		"GET /3/movie/387":    status(http.StatusNotFound),
	})
	if got := find(t, c, Query{Kind: Movie, Title: "Das Boot", Year: 1981}); got.ID != 0 {
		t.Fatalf("got %+v", got)
	}
}

// An API key travels in the address: no error may carry it.
func TestNoErrorHoldsTheAddress(t *testing.T) {
	c := New(apiKey, WithBase("http://127.0.0.1:1/3", ""), WithBackoff(time.Millisecond))
	err := c.Check(context.Background())
	if err == nil || strings.Contains(err.Error(), apiKey) || strings.Contains(err.Error(), "api_key") {
		t.Fatalf("got %v", err)
	}
}

// --- Details ------------------------------------------------------------------------

func TestAFilmsDetailsInTheLanguageAskedFor(t *testing.T) {
	f, c, _ := newFake(t, map[string]func(*http.Request) (int, any){
		"GET /3/movie/937278": answer(map[string]any{
			"id": 937278, "title": "Ein Mann namens Otto", "original_title": "A Man Called Otto", "original_language": "en",
			"release_date": "2022-12-28", "runtime": 126, "vote_average": 7.7, "overview": "…",
			"genres":       []any{map[string]any{"id": 35, "name": "Komödie"}},
			"poster_path":  "/fallback.jpg",
			"external_ids": map[string]any{"imdb_id": "tt7405458", "wikidata_id": "Q1"},
			"images": map[string]any{
				"posters":   []any{map[string]any{"file_path": "/en.jpg", "iso_639_1": "en", "vote_average": 9}, map[string]any{"file_path": "/de.jpg", "iso_639_1": "de", "vote_average": 5}},
				"backdrops": []any{map[string]any{"file_path": "/words.jpg", "iso_639_1": "en", "vote_average": 9}, map[string]any{"file_path": "/plain.jpg", "iso_639_1": nil, "vote_average": 1}},
				"logos":     []any{map[string]any{"file_path": "/logo.svg", "iso_639_1": "de", "vote_average": 9}, map[string]any{"file_path": "/logo.png", "iso_639_1": "en", "vote_average": 1}},
			},
			"release_dates": map[string]any{"results": []any{
				map[string]any{"iso_3166_1": "US", "release_dates": []any{map[string]any{"certification": "PG-13"}}},
				map[string]any{"iso_3166_1": "DE", "release_dates": []any{map[string]any{"certification": ""}, map[string]any{"certification": "12"}}},
			}},
		}),
	})
	info, err := c.Movie(context.Background(), 937278, "de-DE")
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "Ein Mann namens Otto" || info.Year != 2022 || info.RuntimeMinutes != 126 || info.ContentRating != "12" ||
		info.ExternalIDs["imdb"] != "tt7405458" || info.ExternalIDs["tmdb"] != "937278" {
		t.Fatalf("info %+v", info)
	}
	if info.Artwork != (Artwork{Poster: "/de.jpg", Backdrop: "/plain.jpg", Logo: "/logo.png"}) {
		t.Fatalf("artwork %+v", info.Artwork)
	}
	q := f.to("GET /3/movie/937278")[0].URL.Query()
	if q.Get("language") != "de-DE" || q.Get("include_image_language") != "de,en,null" || q.Get("append_to_response") != "external_ids,images,release_dates" {
		t.Fatalf("query %v", q)
	}
	if got := c.ImageURL("w780", info.Artwork.Poster); !strings.HasSuffix(got, "/t/p/w780/de.jpg") {
		t.Fatalf("image %q", got)
	}
}

func TestASeriesSeasonsComeTwentyToACall(t *testing.T) {
	f, c, _ := newFake(t, map[string]func(*http.Request) (int, any){
		"GET /3/tv/37854": func(r *http.Request) (int, any) {
			body := map[string]any{"id": 37854}
			for _, part := range strings.Split(r.URL.Query().Get("append_to_response"), ",") {
				var n int
				_, _ = fmt.Sscanf(part, "season/%d", &n)
				body[part] = map[string]any{
					"season_number": n, "name": fmt.Sprintf("Arc %d", n),
					"episodes": []any{map[string]any{"id": 1000 + n, "season_number": n, "episode_number": 100 + n, "name": "x"}},
				}
			}
			return http.StatusOK, body
		},
	})
	numbers := make([]int, 24)
	for i := range numbers {
		numbers[i] = i
	}
	seasons, err := c.Seasons(context.Background(), 37854, "en-US", numbers)
	if err != nil {
		t.Fatal(err)
	}
	if len(seasons) != 24 || seasons[21].Number != 21 || seasons[21].Episodes[0].Number != 121 {
		t.Fatalf("%d seasons: %+v", len(seasons), seasons[21])
	}
	if calls := f.to("GET /3/tv/37854"); len(calls) != 2 || strings.Count(calls[0].URL.Query().Get("append_to_response"), "season/") != 20 {
		t.Fatalf("%d calls", len(calls))
	}
}

func TestAnEpisodeGroupFlattensInItsOrder(t *testing.T) {
	_, c, _ := newFake(t, map[string]func(*http.Request) (int, any){
		"GET /3/tv/episode_group/abs": answer(map[string]any{"groups": []any{
			map[string]any{"order": 2, "episodes": []any{map[string]any{"order": 0, "season_number": 2, "episode_number": 62}}},
			map[string]any{"order": 1, "episodes": []any{
				map[string]any{"order": 1, "season_number": 1, "episode_number": 2},
				map[string]any{"order": 0, "season_number": 1, "episode_number": 1},
			}},
		}}),
	})
	ordered, err := c.Ordered(context.Background(), "abs")
	if err != nil {
		t.Fatal(err)
	}
	if len(ordered) != 3 || ordered[0] != (EpisodeRef{1, 1}) || ordered[1] != (EpisodeRef{1, 2}) || ordered[2] != (EpisodeRef{2, 62}) {
		t.Fatalf("ordered %+v", ordered)
	}
}

func TestTheAdaptersTestSaysWhetherTMDBTakesTheKey(t *testing.T) {
	adapter := Adapter()
	if adapter.Notice == "" || len(adapter.Fields) != 2 {
		t.Fatalf("adapter %+v", adapter)
	}
}

// --- Live, opt-in -----------------------------------------------------------------

// TestLive asks the real TMDB, with a key from FOYER_TMDB_LIVE_KEY — never
// committed, never printed. The household's own samples, as names only.
func TestLive(t *testing.T) {
	key := os.Getenv("FOYER_TMDB_LIVE_KEY")
	if key == "" {
		t.Skip("FOYER_TMDB_LIVE_KEY is not set")
	}
	c := New(key)
	ctx := context.Background()
	if err := c.Check(ctx); err != nil {
		t.Fatalf("check: %v", err)
	}
	for _, want := range []struct {
		q  Query
		id int
	}{
		{Query{Kind: Movie, Title: "A Man Called Otto", Year: 2022}, 937278},
		{Query{Kind: Movie, Title: "Blue Giant"}, 887870},
		{Query{Kind: Show, Title: "86 Eighty Six"}, 100565},
		{Query{Kind: Show, Title: "One Punch Man"}, 63926},
	} {
		got, err := c.Find(ctx, want.q)
		if err != nil || got.ID != want.id {
			t.Errorf("%+v: %+v %v", want.q, got, err)
		}
	}
	// Two series are called One Piece, and nothing in the name tells them
	// apart: the library decides, by what the files hold.
	got, err := c.Find(ctx, Query{Kind: Show, Title: "One Piece"})
	if err != nil || got.ID != 0 || len(got.Named) < 2 {
		t.Errorf("One Piece: %+v %v", got, err)
	}
	show, err := c.Show(ctx, 37854, "en-US")
	if err != nil || show.EpisodeCount < 1000 || !show.IsAnime() {
		t.Errorf("One Piece's details: %+v %v", show.EpisodeCount, err)
	}
}
