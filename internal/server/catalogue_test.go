package server_test

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/fixtures"
	"foyer/internal/watch"
)

func (h *harness) get(path, token string) answer {
	h.t.Helper()
	got := h.call(http.MethodGet, path, token, nil)
	if got.status != http.StatusOK {
		h.t.Fatalf("GET %s: %d %v", path, got.status, got.body)
	}
	return got
}

// fits says that what Foyer sent is what the app reads: every key in got is
// one the app's fixture has, of the same JSON kind, objects compared all the
// way down and each element of a list against the fixture's elements — so
// Foyer never sends a key the app does not know, or text where it reads a
// number. External ids are keyed by catalogue, any catalogue.
func fits(t *testing.T, where string, got, want any) {
	t.Helper()
	switch g := got.(type) {
	case map[string]any:
		w, ok := want.(map[string]any)
		if !ok {
			t.Errorf("%s: an object where the app reads %T", where, want)
			return
		}
		for key, value := range g {
			if strings.HasSuffix(where, ".externalIds") {
				if _, ok := value.(string); !ok {
					t.Errorf("%s.%s: %T", where, key, value)
				}
				continue
			}
			expected, ok := w[key]
			if !ok {
				t.Errorf("%s.%s: the app's fixture has no such key", where, key)
				continue
			}
			fits(t, where+"."+key, value, expected)
		}
	case []any:
		w, ok := want.([]any)
		if !ok {
			t.Errorf("%s: a list where the app reads %T", where, want)
			return
		}
		if len(w) == 0 {
			if len(g) > 0 {
				t.Errorf("%s: the app's fixture has nothing to hold %v to", where, g[0])
			}
			return
		}
		// Every element against what any of the fixture's elements holds.
		merged := w[0]
		for _, e := range w[1:] {
			merged = union(merged, e)
		}
		for i, e := range g {
			fits(t, fmt.Sprintf("%s[%d]", where, i), e, merged)
		}
	default:
		if fmt.Sprintf("%T", got) != fmt.Sprintf("%T", want) {
			t.Errorf("%s: %T, where the app reads %T", where, got, want)
		}
	}
}

// union is two fixture values as one: every key either has, all the way
// down, and the elements of both lists.
func union(a, b any) any {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		out := map[string]any{}
		for k, v := range am {
			out[k] = v
		}
		for k, v := range bm {
			if mine, ok := out[k]; ok {
				out[k] = union(mine, v)
			} else {
				out[k] = v
			}
		}
		return out
	}
	al, aok := a.([]any)
	bl, bok := b.([]any)
	if aok && bok {
		return append(slices.Clone(al), bl...)
	}
	return a
}

// has says an answer holds every key the app needs.
func has(t *testing.T, where string, got map[string]any, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if _, ok := got[key]; !ok {
			t.Errorf("%s has no %s: %v", where, key, got)
		}
	}
}

func fixtureAnswer(t *testing.T, name string) map[string]any {
	t.Helper()
	var f struct {
		Answer map[string]any `json:"answer"`
	}
	fixtures.Load(t, name, &f)
	return f.Answer
}

func items(a answer, key string) []map[string]any {
	list, _ := a.body[key].([]any)
	out := make([]map[string]any, len(list))
	for i, e := range list {
		out[i] = e.(map[string]any)
	}
	return out
}

func titleNamed(list []map[string]any, title string) map[string]any {
	for _, item := range list {
		if item["title"] == title {
			return item
		}
	}
	return nil
}

// Every answer of the catalogue is one the app's household reads: key for
// key as its fixtures, and every key a title needs.
func TestTheCatalogueAnswersAsTheAppReadsThem(t *testing.T) {
	s := scanLibrary(t)
	alex := s.makeUser("alex", nil)
	film, pack := s.title("movies", "Some Film"), s.title("shows", "Big Pack")
	s.report(alex, "movies", film.Id, "progress", 600_000, 7_200_000)
	first := s.episode(pack, 1, 1)
	s.report(alex, "shows_episodes", first.Id, "progress", 300_000, 1_500_000)
	extras := s.extras(pack)
	if len(extras) == 0 {
		t.Fatal("Big Pack has no extras")
	}
	if got := s.call(http.MethodPost, "/api/foyer/watch/progress", alex.token, map[string]any{
		"collection": "shows", "id": pack.Id, "file": extras[0], "kind": "progress", "positionMs": 90_000, "durationMs": 5_400_000, "session": "x1",
	}); got.status != http.StatusNoContent {
		t.Fatalf("an extra's progress: %d %v", got.status, got.body)
	}

	movies := s.get("/api/foyer/catalogue?kind=movies", alex.token)
	fits(t, "catalogue", movies.body, fixtureAnswer(t, "routes/catalogue.json"))
	for _, item := range items(movies, "items") {
		has(t, "a film", item, "collection", "id", "kind", "title", "genres", "externalIds", "images")
	}
	someFilm := titleNamed(items(movies, "items"), "Some Film")
	if someFilm == nil || someFilm["watch"] == nil || someFilm["identity"] != "tmdb:movie:1001" || someFilm["externalIds"].(map[string]any)["tmdb"] != "1001" {
		t.Errorf("Some Film: %v", someFilm)
	}

	shows := s.get("/api/foyer/catalogue?kind=shows&sort=addedAt&order=desc", alex.token)
	fits(t, "catalogue of shows", shows.body, fixtureAnswer(t, "routes/catalogue-shows.json"))
	bigPack := titleNamed(items(shows, "items"), "Big Pack")
	if bigPack == nil || bigPack["status"] != "ended" || bigPack["seasonCount"] != float64(2) || bigPack["episodeCount"] != float64(4) {
		t.Errorf("Big Pack: %v", bigPack)
	}
	if w, _ := bigPack["watch"].(map[string]any); w == nil || w["unwatched"] != float64(4) || w["watched"] != false {
		t.Errorf("Big Pack's watch: %v", bigPack["watch"])
	}

	detail := s.get("/api/foyer/catalogue/movies/"+film.Id, alex.token)
	fits(t, "a film's page", detail.body, fixtureAnswer(t, "routes/title-movie.json"))
	versions := items(detail, "versions")
	if len(versions) != 2 {
		t.Fatalf("Some Film's versions: %v", versions)
	}
	for _, v := range versions {
		if _, ok := v["path"]; ok {
			t.Errorf("a version tells where on the share it lies: %v", v)
		}
	}

	page := s.get("/api/foyer/catalogue/shows/"+pack.Id, alex.token)
	fits(t, "a series' page", page.body, fixtureAnswer(t, "routes/title-show.json"))
	seasons := s.get("/api/foyer/catalogue/shows/"+pack.Id+"/seasons", alex.token)
	fits(t, "seasons", seasons.body, fixtureAnswer(t, "routes/seasons.json"))
	keys := []string{}
	for _, season := range items(seasons, "seasons") {
		keys = append(keys, season["key"].(string))
	}
	if !slices.Equal(keys, []string{"0", "1", "2", "extras"}) {
		t.Errorf("Big Pack's seasons %v", keys)
	}
	one := s.get("/api/foyer/catalogue/shows/"+pack.Id+"/seasons/1", alex.token)
	fits(t, "a season", one.body, fixtureAnswer(t, "routes/season.json"))
	episodePage := s.get("/api/foyer/catalogue/shows_episodes/"+first.Id, alex.token)
	fits(t, "an episode's page", episodePage.body, fixtureAnswer(t, "routes/episode.json"))
	if ep := episodePage.body["episode"].(map[string]any); ep["watch"] == nil || ep["identity"] != "tmdb:tv:2001/s1e1" {
		t.Errorf("the episode %v", ep)
	}
	if show := episodePage.body["show"].(map[string]any); show["title"] != "Big Pack" || show["watch"] == nil {
		t.Errorf("its series %v", show)
	}
	for _, v := range items(episodePage, "versions") {
		if _, ok := v["path"]; ok {
			t.Errorf("an episode's version tells its path: %v", v)
		}
	}
	if got := s.call(http.MethodGet, "/api/foyer/catalogue/shows_episodes/nothing", alex.token, nil); got.status != http.StatusNotFound {
		t.Errorf("no such episode: %d", got.status)
	}
	extrasSeason := s.get("/api/foyer/catalogue/shows/"+pack.Id+"/seasons/extras", alex.token)
	fits(t, "the extras", extrasSeason.body, fixtureAnswer(t, "routes/season-extras.json"))
	genres := s.get("/api/foyer/catalogue/genres?kind=movies", alex.token)
	fits(t, "genres", genres.body, fixtureAnswer(t, "routes/genres.json"))
	resume := s.get("/api/foyer/catalogue/resume", alex.token)
	fits(t, "resume", resume.body, fixtureAnswer(t, "routes/resume.json"))
	if n := len(items(resume, "items")); n != 3 {
		t.Errorf("resume holds %d", n)
	}

	household := s.get("/api/foyer/household", alex.token)
	fits(t, "household", household.body, fixtureAnswer(t, "routes/household.json"))
	if served := items(household, "served"); len(served) != 1 || served[0]["key"] != "library" || served[0]["type"] != "library" {
		t.Errorf("served %v", served)
	}
}

func (s *scanned) episode(show *core.Record, season, number int) *core.Record {
	s.t.Helper()
	r, err := s.app.FindFirstRecordByFilter("shows_episodes", "show = {:show} && season = {:season} && episode = {:episode}",
		map[string]any{"show": show.Id, "season": season, "episode": number})
	if err != nil {
		s.t.Fatalf("no s%de%d", season, number)
	}
	return r
}

// extras are a title's extras' files.
func (s *scanned) extras(r *core.Record) []string {
	s.t.Helper()
	var list []struct {
		File string `json:"file"`
	}
	_ = r.UnmarshalJSONField("extras", &list)
	files := []string{}
	for _, x := range list {
		files = append(files, x.File)
	}
	return files
}

// orderItem is one of the app's order.json titles.
type orderItem struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	SortTitle   *string  `json:"sortTitle"`
	Year        int      `json:"year"`
	ReleaseDate string   `json:"releaseDate"`
	AddedAt     string   `json:"addedAt"`
	Rating      *float64 `json:"rating"`
}

// The catalogue's pages, read one after the other, are the app's own order
// for every sort, across two libraries — the titles of order.json, the hard
// cases among them.
func TestTheCatalogueIsInTheAppsOrder(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	h.addLibrary(admin, "movies", "movies", "movies")
	h.addLibrary(admin, "films", "films", "movies")
	alex := h.makeUser("alex", nil)
	var fixture struct {
		Items  []orderItem `json:"items"`
		Orders []struct {
			By    string   `json:"by"`
			Order string   `json:"order"`
			IDs   []string `json:"ids"`
		} `json:"orders"`
	}
	fixtures.Load(t, "order.json", &fixture)

	// Ids that sort as the fixture's do: the last tie-break is Loge's id,
	// `collection/id`, so two titles that tie share a collection.
	names := []string{}
	for _, item := range fixture.Items {
		names = append(names, item.ID)
	}
	sort.Strings(names)
	recordID := map[string]string{}
	fixtureID := map[string]string{}
	for i, name := range names {
		recordID[name] = fmt.Sprintf("o%014d", i)
		fixtureID[recordID[name]] = name
	}
	// An empty sort title is one Foyer never keeps, and a rating of 0 is
	// nobody's: the app sorts both as the fixture says, Foyer as the
	// catalogue sends them, which the app sorts the same way.
	skipAlways, skipRating := map[string]bool{"m-empty-sort": true}, map[string]bool{}
	for i, item := range fixture.Items {
		if skipAlways[item.ID] {
			continue
		}
		collection := []string{"movies", "films"}[i%2]
		if item.ID == "m-thing-b" || item.ID == "m-the-thing" {
			collection = "movies"
		}
		fields := map[string]any{"id": recordID[item.ID], "title": item.Title, "year": item.Year, "release_date": item.ReleaseDate,
			"match_state": "matched", "available": true}
		if item.SortTitle != nil {
			fields["sort_title"] = *item.SortTitle
		}
		if item.AddedAt != "" {
			fields["added"] = item.AddedAt
		}
		if item.Rating != nil {
			fields["rating"] = *item.Rating
			if *item.Rating == 0 {
				skipRating[item.ID] = true
			}
		}
		h.save(collection, fields)
	}

	for _, o := range fixture.Orders {
		skip := func(id string) bool { return skipAlways[id] || (o.By == "rating" && skipRating[id]) }
		want := slices.DeleteFunc(slices.Clone(o.IDs), skip)
		got := []string{}
		next := ""
		for pages := 0; pages < 20; pages++ {
			path := "/api/foyer/catalogue?kind=movies&limit=3&sort=" + o.By + "&order=" + o.Order
			if next != "" {
				path += "&cursor=" + url.QueryEscape(next)
			}
			page := h.get(path, alex.token)
			for _, item := range items(page, "items") {
				got = append(got, fixtureID[item["id"].(string)])
			}
			next, _ = page.body["nextCursor"].(string)
			if next == "" {
				break
			}
		}
		got = slices.DeleteFunc(got, skip)
		if !slices.Equal(got, want) {
			t.Errorf("%s %s:\n got %v\nwant %v", o.By, o.Order, got, want)
		}
	}
}

// A page after a cursor starts after the title it ended on, whatever came or
// went meanwhile: the cursor is the title's keys, never an offset.
func TestAPageStaysPutWhileTheLibraryChanges(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	h.addLibrary(admin, "movies", "movies", "movies")
	alex := h.makeUser("alex", nil)
	for _, title := range []string{"Bravo", "Charlie", "Delta", "Echo", "Foxtrot"} {
		h.save("movies", map[string]any{"title": title, "match_state": "matched", "available": true})
	}
	first := h.get("/api/foyer/catalogue?kind=movies&limit=2", alex.token)
	if got := titlesOf(first); !slices.Equal(got, []string{"Bravo", "Charlie"}) {
		t.Fatalf("the first page %v", got)
	}
	// One sorts before the page, the page's last goes.
	h.save("movies", map[string]any{"title": "Alpha", "match_state": "matched", "available": true})
	charlie, err := h.app.FindFirstRecordByFilter("movies", "title = 'Charlie'")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.app.Delete(charlie); err != nil {
		t.Fatal(err)
	}
	next := first.body["nextCursor"].(string)
	second := h.get("/api/foyer/catalogue?kind=movies&limit=2&cursor="+url.QueryEscape(next), alex.token)
	if got := titlesOf(second); !slices.Equal(got, []string{"Delta", "Echo"}) {
		t.Errorf("the second page %v", got)
	}
	if got := h.call(http.MethodGet, "/api/foyer/catalogue?kind=movies&cursor=not-a-cursor", alex.token, nil); got.status != http.StatusBadRequest {
		t.Errorf("a cursor of nothing: %d", got.status)
	}
	if got := h.call(http.MethodGet, "/api/foyer/catalogue?kind=movies", "", nil); got.status != http.StatusUnauthorized {
		t.Errorf("a guest: %d", got.status)
	}
}

func titlesOf(a answer) []string {
	out := []string{}
	for _, item := range items(a, "items") {
		out = append(out, item["title"].(string))
	}
	return out
}

// A search finds every word in a title's names, case and accents aside; a
// genre is matched as the app keys it; and what a user may not see is not
// listed: a guess, a title whose files are gone, a library switched off.
func TestTheCatalogueFindsAndHides(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	lib := h.addLibrary(admin, "movies", "movies", "movies")["id"].(string)
	alex := h.makeUser("alex", nil)
	for _, fields := range []map[string]any{
		{"title": "Pokémon: The Movie", "original_title": "劇場版ポケットモンスター", "year": 1998, "genres": []string{"Animation", "Science  Fiction"}},
		{"title": "İstanbul Hatırası", "year": 2010, "genres": []string{"Drama"}},
		{"title": "Istanbul", "year": 2011, "genres": []string{"science fiction"}},
	} {
		fields["match_state"], fields["available"] = "matched", true
		h.save("movies", fields)
	}
	h.save("movies", map[string]any{"title": "A Guess", "match_state": "uncertain", "available": true})
	h.save("movies", map[string]any{"title": "All Gone", "match_state": "matched", "available": false})

	for term, want := range map[string][]string{
		"pokemon":        {"Pokémon: The Movie"},
		"POKÉMON 1998":   {"Pokémon: The Movie"},
		"istanbul":       {"Istanbul", "İstanbul Hatırası"},
		"hatırası":       {"İstanbul Hatırası"},
		"movie istanbul": {},
		"ポケットモンスター":      {"Pokémon: The Movie"},
	} {
		got := titlesOf(h.get("/api/foyer/catalogue?kind=movies&term="+url.QueryEscape(term), alex.token))
		if !slices.Equal(got, want) {
			t.Errorf("%q found %v, want %v", term, got, want)
		}
	}
	if got := titlesOf(h.get("/api/foyer/catalogue?kind=movies&genre="+url.QueryEscape("Science Fiction"), alex.token)); !slices.Equal(got, []string{"Istanbul", "Pokémon: The Movie"}) {
		t.Errorf("science fiction: %v", got)
	}
	all := titlesOf(h.get("/api/foyer/catalogue?kind=movies", alex.token))
	if slices.Contains(all, "A Guess") || slices.Contains(all, "All Gone") || len(all) != 3 {
		t.Errorf("listed %v", all)
	}
	genres := h.get("/api/foyer/catalogue/genres?kind=movies", alex.token).body["genres"]
	if fmt.Sprint(genres) != "[Animation Drama Science  Fiction]" {
		t.Errorf("genres %v", genres)
	}
	guess, err := h.app.FindFirstRecordByFilter("movies", "title = 'A Guess'")
	if err != nil {
		t.Fatal(err)
	}
	if got := h.call(http.MethodGet, "/api/foyer/catalogue/movies/"+guess.Id, alex.token, nil); got.status != http.StatusNotFound {
		t.Errorf("a guess's page: %d", got.status)
	}

	// Switched off, the library lists nothing — and is still served, empty,
	// so no device lets go of what it kept of it.
	if got := h.call(http.MethodPatch, "/api/foyer/admin/libraries/"+lib, admin, map[string]any{"enabled": false}); got.status != http.StatusOK {
		t.Fatalf("switching off: %d %v", got.status, got.body)
	}
	if got := titlesOf(h.get("/api/foyer/catalogue?kind=movies", alex.token)); len(got) != 0 {
		t.Errorf("a library switched off lists %v", got)
	}
	if served := items(h.get("/api/foyer/household", alex.token), "served"); len(served) != 1 {
		t.Errorf("served with its library switched off: %v", served)
	}
	// Gone, it is no longer served.
	if got := h.call(http.MethodDelete, "/api/foyer/admin/libraries/"+lib, admin, nil); got.status != http.StatusNoContent {
		t.Fatalf("removing: %d %v", got.status, got.body)
	}
	if served := items(h.get("/api/foyer/household", alex.token), "served"); len(served) != 0 {
		t.Errorf("served with no library: %v", served)
	}
	if got := h.call(http.MethodGet, "/api/foyer/catalogue?kind=films", alex.token, nil); got.status != http.StatusBadRequest {
		t.Errorf("a kind of nothing: %d", got.status)
	}
}

// A series' seasons count what it holds and what its user watched; its
// extras are a season of their own, each with its own watch status, which
// moves with the series.
func TestASeriesSeasonsAndExtras(t *testing.T) {
	s := scanLibrary(t)
	alex := s.makeUser("alex", nil)
	pack := s.title("shows", "Big Pack")
	first, second := s.episode(pack, 1, 1), s.episode(pack, 1, 2)
	s.report(alex, "shows_episodes", first.Id, "end", 0, 1_500_000)
	s.report(alex, "shows_episodes", second.Id, "progress", 400_000, 1_500_000)

	seasons := items(s.get("/api/foyer/catalogue/shows/"+pack.Id+"/seasons", alex.token), "seasons")
	byKey := map[string]map[string]any{}
	for _, season := range seasons {
		byKey[season["key"].(string)] = season
	}
	if one := byKey["1"]; one["name"] != "First Arc" || one["episodeCount"] != float64(2) || one["unwatched"] != float64(1) {
		t.Errorf("season 1: %v", one)
	}
	if specials := byKey["0"]; specials["name"] != "Specials" || specials["episodeCount"] != float64(1) {
		t.Errorf("the specials: %v", specials)
	}
	if extras := byKey["extras"]; extras["name"] != "Extras" || extras["episodeCount"] != float64(len(s.extras(pack))) {
		t.Errorf("the extras: %v", extras)
	}

	episodes := items(s.get("/api/foyer/catalogue/shows/"+pack.Id+"/seasons/1", alex.token), "episodes")
	if len(episodes) != 2 || episodes[0]["episode"] != float64(1) || episodes[1]["episode"] != float64(2) {
		t.Fatalf("season 1's episodes %v", episodes)
	}
	if w := episodes[0]["watch"].(map[string]any); w["watched"] != true {
		t.Errorf("the first episode's watch %v", w)
	}
	if w := episodes[1]["watch"].(map[string]any); w["positionMs"] != float64(400_000) {
		t.Errorf("the second episode's watch %v", w)
	}

	extras := items(s.get("/api/foyer/catalogue/shows/"+pack.Id+"/seasons/extras", alex.token), "episodes")
	if len(extras) == 0 {
		t.Fatal("no extras")
	}
	extra := extras[0]
	if !strings.HasPrefix(extra["id"].(string), "extra:") || extra["file"] == nil || !strings.HasPrefix(extra["identity"].(string), "tmdb:tv:2001/extra:") {
		t.Fatalf("an extra %v", extra)
	}
	if got := s.call(http.MethodPost, "/api/foyer/watch/played", alex.token, map[string]any{"collection": "shows", "id": pack.Id, "file": extra["file"], "played": true}); got.status != http.StatusNoContent {
		t.Fatalf("an extra played: %d %v", got.status, got.body)
	}
	if got := s.call(http.MethodPost, "/api/foyer/watch/played", alex.token, map[string]any{"collection": "shows", "id": pack.Id, "file": first.Id, "played": true}); got.status != http.StatusNotFound {
		t.Errorf("an episode's file as an extra: %d", got.status)
	}
	extras = items(s.get("/api/foyer/catalogue/shows/"+pack.Id+"/seasons/extras", alex.token), "episodes")
	if w, _ := extras[0]["watch"].(map[string]any); w == nil || w["watched"] != true {
		t.Errorf("the extra's watch %v", extras[0])
	}
	// What was watched of the series' extras follows the series to another
	// identity.
	known := extra["identity"].(string)
	if err := watch.MoveExtras(s.app, "tmdb:tv:2001", "tmdb:tv:9"); err != nil {
		t.Fatal(err)
	}
	moved := strings.Replace(known, "tmdb:tv:2001/", "tmdb:tv:9/", 1)
	if s.state(alex, known) != nil || s.state(alex, moved) == nil || !s.state(alex, moved).GetBool("watched") {
		t.Errorf("the extra's state did not move to %s", moved)
	}
	if s.state(alex, "tmdb:tv:2001/s1e1") == nil {
		t.Error("an episode's state moved with the extras")
	}
}

// What was begun comes back the latest first — an episode with its series,
// an extra as one of its episodes — and what is finished, hidden or a look
// does not.
func TestWhatWasBegunComesBack(t *testing.T) {
	s := scanLibrary(t)
	alex, sam := s.makeUser("alex", nil), s.makeUser("sam", nil)
	film, pack := s.title("movies", "Some Film"), s.title("shows", "Big Pack")
	first := s.episode(pack, 1, 1)
	s.report(alex, "movies", film.Id, "progress", 600_000, 7_200_000)
	s.report(alex, "shows_episodes", first.Id, "progress", 300_000, 1_500_000)
	s.call(http.MethodPost, "/api/foyer/watch/progress", alex.token, map[string]any{
		"collection": "shows", "id": pack.Id, "file": s.extras(pack)[0], "kind": "progress", "positionMs": 120_000, "durationMs": 5_400_000,
	})
	// A look is not begun.
	lonely := s.title("movies", "Lonely Picture")
	s.report(alex, "movies", lonely.Id, "progress", 10_000, 5_400_000)
	// The order they were played in, a second apart.
	for i, identity := range []string{"tmdb:movie:1001", "tmdb:tv:2001/s1e1"} {
		state := s.state(alex, identity)
		state.Set("last_played_at", fmt.Sprintf("2026-10-10 20:0%d:00.000Z", i))
		if err := s.app.Save(state); err != nil {
			t.Fatal(err)
		}
	}
	for _, state := range s.statesOf(alex) {
		if strings.Contains(state.GetString("identity"), "/extra:") {
			state.Set("last_played_at", "2026-10-10 20:05:00.000Z")
			if err := s.app.Save(state); err != nil {
				t.Fatal(err)
			}
		}
	}
	resume := items(s.get("/api/foyer/catalogue/resume", alex.token), "items")
	kinds := []string{}
	for _, item := range resume {
		switch item["kind"] {
		case "movie":
			kinds = append(kinds, "movie:"+item["title"].(map[string]any)["title"].(string))
		case "episode":
			kinds = append(kinds, "episode:"+item["episode"].(map[string]any)["id"].(string)[:6]+":"+item["show"].(map[string]any)["title"].(string))
		}
	}
	if !slices.Equal(kinds, []string{"episode:extra::Big Pack", "episode:" + first.Id[:6] + ":Big Pack", "movie:Some Film"}) {
		t.Errorf("resume %v", kinds)
	}
	if got := items(s.get("/api/foyer/catalogue/resume?limit=1", alex.token), "items"); len(got) != 1 {
		t.Errorf("a limit of one: %d", len(got))
	}
	if got := items(s.get("/api/foyer/catalogue/resume", sam.token), "items"); len(got) != 0 {
		t.Errorf("sam resumes alex's: %v", got)
	}
}

func (s *scanned) statesOf(u user) []*core.Record {
	s.t.Helper()
	list, err := s.app.FindAllRecords("watch_states", dbx.HashExp{"user": u.id})
	if err != nil {
		s.t.Fatal(err)
	}
	return list
}
