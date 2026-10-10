package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/adapters/tmdb"
	"foyer/internal/adapters/webdav/webdavtest"
)

// A household's library in miniature: every name invented, shaped like the
// household's own.
var (
	monday  = time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	tuesday = monday.Add(24 * time.Hour)
	friday  = monday.Add(4 * 24 * time.Hour)
)

func zurgLibrary() *webdavtest.Fake {
	f := webdavtest.New()
	// As the real Zurg answers: bare relative hrefs, dates in UTC, every
	// file sent as a part with no length.
	f.AsZurg = true
	f.Dir("__all__", monday)
	// Films: one film in two torrents, an anime film named once, two films
	// of one name, and a film TMDB never heard of.
	f.File("movies/Some.Film.2022.1080p.BluRay.x264-GRP/Some.Film.2022.1080p.BluRay.x264-GRP.mkv", []byte("1080"), monday)
	f.Dir("movies/Some.Film.2022.1080p.BluRay.x264-GRP", monday)
	f.File("movies/Some.Film.2022.2160p.UHD.BluRay.x265.HDR-GRP/Some.Film.2022.2160p.UHD.BluRay.x265.HDR-GRP.mkv", []byte("2160p"), friday)
	f.Dir("movies/Some.Film.2022.2160p.UHD.BluRay.x265.HDR-GRP", friday)
	f.File("movies/[Grp] Lonely Picture [Eng Sub]/[Grp] Lonely Picture [Eng Sub].mkv", []byte("lonely"), tuesday)
	f.Dir("movies/[Grp] Lonely Picture [Eng Sub]", tuesday)
	f.File("movies/Twin Title/Twin.Title.mkv", []byte("twin"), monday)
	f.Dir("movies/Twin Title", monday)
	f.File("movies/Nothing.Known.1999.1080p/Nothing.Known.1999.1080p.mkv", []byte("nothing"), monday)
	f.Dir("movies/Nothing.Known.1999.1080p", monday)
	// Series: an anime pack numbered from its start, three copies of its first
	// episode, a special and a film in it; a season pack; one episode alone.
	pack := "shows/[Grp] Big Pack (0001-0030) [Dual Audio] [1080p]"
	for _, file := range []string{
		"/Episodes Uncropped (1080p)/[Grp] Big Pack - 0001 - The Start (11).mkv",
		"/Episodes Uncropped (480p)/[Grp] Big Pack - 0001 - The Start (12).mkv",
		"/Season 01 - First Arc/[Grp] Big Pack - 0001 - The Start (13).mkv",
		"/Season 01 - First Arc/[Grp] Big Pack - 0002 - The Next.mkv",
		"/Season 01 - First Arc/[Grp] Big Pack - 0013 - The Turn.mkv",
		"/[Grp] Big Pack Specials/[Grp] Big Pack Special - Episode of Somebody.mkv",
		"/[Grp] Big Pack Specials/[Grp] Big Pack OVA - Something Else Entirely.mkv",
		"/[Grp] Big Pack Movies/[Grp] Big Pack Movie 01 (2010) - The Film.mkv",
	} {
		f.File(pack+file, []byte(file), tuesday)
	}
	f.Dir(pack, tuesday)
	f.File("shows/Some.Show.S01.1080p.WEB-DL-GRP/Some.Show.S01E01.1080p.WEB-DL-GRP.mkv", []byte("s1e1"), monday)
	f.File("shows/Some.Show.S01.1080p.WEB-DL-GRP/Some.Show.S01E02.1080p.WEB-DL-GRP.mkv", []byte("s1e2"), monday)
	f.Dir("shows/Some.Show.S01.1080p.WEB-DL-GRP", monday)
	f.File("shows/Single Ep - S02E03 - Title - 1080p/Single Ep - S02E03 - Title - 1080p.mkv", []byte("s2e3"), friday)
	f.Dir("shows/Single Ep - S02E03 - Title - 1080p", friday)
	return f
}

func picture() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 4, 6))
	img.Set(0, 0, color.RGBA{200, 30, 30, 255})
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// fakeTMDB answers as TMDB does, for the titles above.
func fakeTMDB(t *testing.T) *httptest.Server {
	movies := map[int]map[string]any{
		1001: {"id": 1001, "title": "Some Film", "original_title": "Some Film", "release_date": "2022-03-01", "runtime": 120,
			"genres": []any{map[string]any{"id": 18, "name": "Drama"}}, "external_ids": map[string]any{"imdb_id": "tt1001"},
			"images": map[string]any{"posters": []any{map[string]any{"file_path": "/film.png", "iso_639_1": "en", "vote_average": 5}}}},
		1002: {"id": 1002, "title": "Lonely Picture", "original_title": "Lonely Picture", "original_language": "ja", "release_date": "2023-02-17",
			"genres": []any{map[string]any{"id": 16, "name": "Animation"}}},
		1003: {"id": 1003, "title": "Twin Title", "original_title": "Twin Title", "release_date": "2010-01-01"},
		1004: {"id": 1004, "title": "Twin Title", "original_title": "Twin Title", "release_date": "2020-01-01"},
		1009: {"id": 1009, "title": "Known After All", "original_title": "Known After All", "release_date": "1999-05-05"},
	}
	type episode struct {
		number int
		name   string
	}
	shows := map[int]map[string]any{
		2001: {"id": 2001, "name": "Big Pack", "original_name": "ビッグ", "original_language": "ja", "origin_country": []any{"JP"},
			"first_air_date": "2005-04-01", "status": "Ended", "number_of_seasons": 2, "number_of_episodes": 30,
			"genres": []any{map[string]any{"id": 16, "name": "Animation"}},
			"seasons": []any{
				map[string]any{"season_number": 0, "episode_count": 1, "name": "Specials"},
				map[string]any{"season_number": 1, "episode_count": 12, "name": "First Arc"},
				map[string]any{"season_number": 2, "episode_count": 18, "name": "Second Arc"},
			}},
		2009: {"id": 2009, "name": "Big Pack", "original_name": "Big Pack", "original_language": "en", "first_air_date": "2021-01-01",
			"status": "Ended", "number_of_seasons": 1, "number_of_episodes": 5,
			"seasons": []any{map[string]any{"season_number": 1, "episode_count": 5}}},
		2002: {"id": 2002, "name": "Some Show", "original_name": "Some Show", "first_air_date": "2019-01-01", "status": "Returning Series",
			"number_of_seasons": 1, "number_of_episodes": 8, "seasons": []any{map[string]any{"season_number": 1, "episode_count": 8}}},
		2003: {"id": 2003, "name": "Single Ep", "original_name": "Single Ep", "first_air_date": "2018-01-01", "status": "Ended",
			"number_of_seasons": 2, "number_of_episodes": 20,
			"seasons": []any{map[string]any{"season_number": 1, "episode_count": 10}, map[string]any{"season_number": 2, "episode_count": 10}}},
	}
	// Big Pack's seasons run on from each other, as TMDB numbers One Piece.
	seasons := map[int]map[int][]episode{
		2001: {0: {{7, "Episode of Somebody: A Long Title"}}, 1: {}, 2: {}},
		2002: {1: {}},
		2003: {1: {}, 2: {}},
	}
	for n := 1; n <= 12; n++ {
		seasons[2001][1] = append(seasons[2001][1], episode{n, fmt.Sprintf("Big %d", n)})
	}
	for n := 13; n <= 30; n++ {
		seasons[2001][2] = append(seasons[2001][2], episode{n, fmt.Sprintf("Big %d", n)})
	}
	for n := 1; n <= 8; n++ {
		seasons[2002][1] = append(seasons[2002][1], episode{n, fmt.Sprintf("Some %d", n)})
	}
	for s := 1; s <= 2; s++ {
		for n := 1; n <= 10; n++ {
			seasons[2003][s] = append(seasons[2003][s], episode{n, fmt.Sprintf("Single %d.%d", s, n)})
		}
	}
	search := map[string][]map[string]any{
		"movie/Some Film":      {{"id": 1001, "title": "Some Film", "original_title": "Some Film", "release_date": "2022-03-01"}},
		"movie/Lonely Picture": {{"id": 1002, "title": "Lonely Picture", "original_title": "Lonely Picture", "release_date": "2023-02-17"}},
		"movie/Twin Title": {
			{"id": 1003, "title": "Twin Title", "original_title": "Twin Title", "release_date": "2010-01-01"},
			{"id": 1004, "title": "Twin Title", "original_title": "Twin Title", "release_date": "2020-01-01"},
		},
		"tv/Big Pack": {
			{"id": 2009, "name": "Big Pack", "original_name": "Big Pack", "first_air_date": "2021-01-01"},
			{"id": 2001, "name": "Big Pack", "original_name": "ビッグ", "first_air_date": "2005-04-01"},
		},
		"tv/Some Show": {{"id": 2002, "name": "Some Show", "original_name": "Some Show", "first_air_date": "2019-01-01"}},
		"tv/Single Ep": {{"id": 2003, "name": "Single Ep", "original_name": "Single Ep", "first_air_date": "2018-01-01"}},
	}
	art := picture()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		write := func(body any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(body)
		}
		switch {
		case strings.HasPrefix(path, "/t/p/"):
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(art)
		case strings.HasPrefix(path, "/3/search/"):
			kind := strings.TrimPrefix(path, "/3/search/")
			year := r.URL.Query().Get("year") + r.URL.Query().Get("first_air_date_year")
			var results []map[string]any
			for _, result := range search[kind+"/"+r.URL.Query().Get("query")] {
				date, _ := result["release_date"].(string)
				if date == "" {
					date, _ = result["first_air_date"].(string)
				}
				if year == "" || strings.HasPrefix(date, year) {
					results = append(results, result)
				}
			}
			write(map[string]any{"results": results})
		case strings.HasPrefix(path, "/3/movie/"):
			id, _ := strconv.Atoi(strings.TrimPrefix(path, "/3/movie/"))
			if m, ok := movies[id]; ok {
				write(m)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(path, "/episode_groups"):
			write(map[string]any{"results": []any{}})
		case strings.HasPrefix(path, "/3/tv/"):
			id, _ := strconv.Atoi(strings.TrimPrefix(path, "/3/tv/"))
			show, ok := shows[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			body := map[string]any{}
			for k, v := range show {
				body[k] = v
			}
			for _, part := range strings.Split(r.URL.Query().Get("append_to_response"), ",") {
				number, err := strconv.Atoi(strings.TrimPrefix(part, "season/"))
				if !strings.HasPrefix(part, "season/") || err != nil {
					continue
				}
				var list []any
				for _, e := range seasons[id][number] {
					list = append(list, map[string]any{"id": id*1000 + e.number, "season_number": number, "episode_number": e.number, "name": e.name, "still_path": "/still.png"})
				}
				body[part] = map[string]any{"season_number": number, "episodes": list}
			}
			write(body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// scanned is a household's Foyer after a first scan of both libraries.
type scanned struct {
	*harness
	admin       string
	zurg        *webdavtest.Fake
	movies, tvs string
}

func scanLibrary(t *testing.T) *scanned {
	t.Helper()
	h := start(t, options{})
	admin := h.superuser()
	fake := zurgLibrary()
	zurgServer := httptest.NewServer(fake)
	t.Cleanup(zurgServer.Close)
	tmdbServer := fakeTMDB(t)
	h.server.Library.UseTMDB(tmdb.WithBase(tmdbServer.URL+"/3", tmdbServer.URL+"/t/p"), tmdb.WithBackoff(time.Millisecond))
	h.server.Library.SetClock(func() time.Time { return friday.Add(time.Hour) })
	for key, values := range map[string]map[string]any{
		"webdav": {"url": zurgServer.URL + "/dav"},
		"tmdb":   {"key": "a-key.for.tests", "language": "en-US"},
	} {
		if got := h.call(http.MethodPut, "/api/foyer/admin/adapters/"+key, admin, map[string]any{"enabled": true, "values": values}); got.status != http.StatusOK {
			t.Fatalf("%s: %d %v", key, got.status, got.body)
		}
	}
	movies := h.addLibrary(admin, "movies", "movies", "movies")["id"].(string)
	tvs := h.addLibrary(admin, "shows", "shows", "shows")["id"].(string)
	for _, id := range []string{movies, tvs} {
		if err := h.server.Library.Scan(context.Background(), id, true); err != nil {
			t.Fatalf("scanning: %v", err)
		}
	}
	return &scanned{harness: h, admin: admin, zurg: fake, movies: movies, tvs: tvs}
}

// An entry that will not be kept is told and passed over: the library's
// other titles are placed all the same, and the next scan tries it again.
func TestAnEntryThatWillNotBeKeptHoldsNothingBack(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	fake := zurgLibrary()
	fake.File("movies/Broken.Film.2020.1080p/Broken.Film.2020.1080p.mkv", []byte("broken"), monday)
	fake.Dir("movies/Broken.Film.2020.1080p", monday)
	zurgServer := httptest.NewServer(fake)
	t.Cleanup(zurgServer.Close)
	tmdbServer := fakeTMDB(t)
	h.server.Library.UseTMDB(tmdb.WithBase(tmdbServer.URL+"/3", tmdbServer.URL+"/t/p"), tmdb.WithBackoff(time.Millisecond))
	for key, values := range map[string]map[string]any{
		"webdav": {"url": zurgServer.URL + "/dav"},
		"tmdb":   {"key": "a-key.for.tests", "language": "en-US"},
	} {
		if got := h.call(http.MethodPut, "/api/foyer/admin/adapters/"+key, admin, map[string]any{"enabled": true, "values": values}); got.status != http.StatusOK {
			t.Fatalf("%s: %d %v", key, got.status, got.body)
		}
	}
	refuse := true
	h.app.OnRecordCreate("media_files").BindFunc(func(e *core.RecordEvent) error {
		if refuse && strings.Contains(e.Record.GetString("path"), "Broken") {
			return fmt.Errorf("refused for the test")
		}
		return e.Next()
	})
	movies := h.addLibrary(admin, "movies", "movies", "movies")["id"].(string)
	if err := h.server.Library.Scan(context.Background(), movies, true); err != nil {
		t.Fatalf("the scan gave up: %v", err)
	}
	if _, err := h.app.FindFirstRecordByFilter("movies", "title = 'Some Film'"); err != nil {
		t.Fatalf("the other films were not placed: %v", err)
	}
	status := h.call(http.MethodGet, "/api/foyer/admin/status", admin, nil)
	notes := fmt.Sprint(status.body["scans"].([]any)[0].(map[string]any)["notes"])
	if !strings.Contains(notes, "Broken.Film.2020.1080p was not kept") {
		t.Errorf("notes %s", notes)
	}
	refuse = false
	if err := h.server.Library.Scan(context.Background(), movies, false); err != nil {
		t.Fatal(err)
	}
	if n, _ := h.app.CountRecords("media_files", dbx.NewExp("path LIKE '%Broken%'")); n != 1 {
		t.Errorf("the next scan kept %d of its files", n)
	}
}

func (s *scanned) title(collection, title string) *core.Record {
	s.t.Helper()
	r, err := s.app.FindFirstRecordByFilter(collection, "title = {:title}", dbx.Params{"title": title})
	if err != nil {
		s.t.Fatalf("no %q in %s", title, collection)
	}
	return r
}

func TestAScanNamesEveryFilm(t *testing.T) {
	s := scanLibrary(t)
	film := s.title("movies", "Some Film")
	if film.GetString("match_state") != "matched" || film.GetString("identity") != "tmdb:movie:1001" || film.GetInt("version_count") != 2 ||
		film.GetString("imdb_id") != "tt1001" || film.GetInt("runtime_ms") != 120*60_000 {
		t.Fatalf("some film: %v", film.PublicExport())
	}
	if q := film.GetStringSlice("quality"); !slices.Equal(q, []string{"fhd", "4k"}) {
		t.Errorf("quality %v", q)
	}
	if enc := film.GetStringSlice("encoding"); !slices.Equal(enc, []string{"h264", "hevc"}) {
		t.Errorf("encoding %v", enc)
	}
	// Added when its first file was, as Zurg dated its folder; last added when
	// its newest was.
	if added := film.GetDateTime("added").Time(); !added.Equal(monday) {
		t.Errorf("added %v", added)
	}
	if last := film.GetDateTime("last_added").Time(); !last.Equal(friday) {
		t.Errorf("last added %v", last)
	}
	var versions []map[string]any
	_ = film.UnmarshalJSONField("versions", &versions)
	if len(versions) != 2 || versions[0]["quality"] != "4k" || !strings.HasPrefix(versions[0]["path"].(string), "Some.Film.2022.2160p") {
		t.Errorf("versions %v", versions)
	}
	if film.GetString("poster") == "" || film.GetString("poster_path") != "/film.png" {
		t.Errorf("poster %q from %q", film.GetString("poster"), film.GetString("poster_path"))
	}
	if lonely := s.title("movies", "Lonely Picture"); lonely.GetString("match_state") != "matched" {
		t.Errorf("lonely picture: %s", lonely.GetString("match_state"))
	}
	twin := s.title("movies", "Twin Title")
	if twin.GetString("match_state") != "uncertain" || twin.GetString("identity") != "" || twin.GetInt("tmdb_id") != 1003 {
		t.Errorf("twin title: %s %q %d", twin.GetString("match_state"), twin.GetString("identity"), twin.GetInt("tmdb_id"))
	}
	nothing := s.title("movies", "Nothing Known")
	if nothing.GetString("match_state") != "unmatched" || nothing.GetString("identity") != "title:movie:nothing known:1999" {
		t.Errorf("nothing known: %s %q", nothing.GetString("match_state"), nothing.GetString("identity"))
	}
}

func TestAScanLaysAPackOnItsSeriesEpisodes(t *testing.T) {
	s := scanLibrary(t)
	pack := s.title("shows", "Big Pack")
	// Two series are called Big Pack; only one holds 13 episodes.
	if pack.GetInt("tmdb_id") != 2001 || pack.GetString("match_state") != "matched" || pack.GetString("numbering") != "absolute" {
		t.Fatalf("big pack: %d %s %s", pack.GetInt("tmdb_id"), pack.GetString("match_state"), pack.GetString("numbering"))
	}
	episodes, err := s.app.FindRecordsByFilter("shows_episodes", "show = {:show}", "season,episode", 0, 0, dbx.Params{"show": pack.Id})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ep := range episodes {
		got = append(got, fmt.Sprintf("S%dE%d×%d %s %s", ep.GetInt("season"), ep.GetInt("episode"), ep.GetInt("version_count"), ep.GetString("title"), ep.GetString("identity")))
	}
	want := []string{
		"S0E7×1 Episode of Somebody: A Long Title tmdb:tv:2001/s0e7",
		"S1E1×3 Big 1 tmdb:tv:2001/s1e1",
		"S1E2×1 Big 2 tmdb:tv:2001/s1e2",
		// In a "Season 01" folder, and still the second season's: counted
		// from the start.
		"S2E13×1 Big 13 tmdb:tv:2001/s2e13",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("episodes:\n%s", strings.Join(got, "\n"))
	}
	// The film is an extra; so is an OVA TMDB has no episode for.
	var extras []map[string]any
	_ = pack.UnmarshalJSONField("extras", &extras)
	kinds := map[string]string{}
	for _, x := range extras {
		kinds[x["kind"].(string)] = x["title"].(string)
	}
	if len(extras) != 2 || kinds["movie"] != "The Film" || kinds["ova"] != "Something Else Entirely" {
		t.Errorf("extras %v", extras)
	}
	if pack.GetInt("episodes_owned") != 4 || pack.GetInt("version_count") != 6 {
		t.Errorf("owned %d, versions %d", pack.GetInt("episodes_owned"), pack.GetInt("version_count"))
	}
	show := s.title("shows", "Some Show")
	if n, _ := s.app.CountRecords("shows_episodes", dbx.HashExp{"show": show.Id}); n != 2 {
		t.Errorf("some show has %d episodes", n)
	}
	single := s.title("shows", "Single Ep")
	if ep, err := s.app.FindFirstRecordByFilter("shows_episodes", "show = {:show}", dbx.Params{"show": single.Id}); err != nil ||
		ep.GetInt("season") != 2 || ep.GetInt("episode") != 3 || ep.GetString("still") == "" {
		t.Errorf("single ep: %v %v", ep, err)
	}
}

func TestTheGridIsTheNewestAddedFirst(t *testing.T) {
	s := scanLibrary(t)
	got := s.call(http.MethodGet, "/api/foyer/admin/library?perPage=100", s.admin, nil)
	if got.status != http.StatusOK {
		t.Fatalf("%d %v", got.status, got.body)
	}
	var titles []string
	for _, raw := range got.body["items"].([]any) {
		titles = append(titles, raw.(map[string]any)["title"].(string))
	}
	if len(titles) != 7 || titles[0] != "Single Ep" || titles[1] != "Some Film" {
		t.Fatalf("grid: %v", titles)
	}
	counts := got.body["counts"].(map[string]any)
	if counts["uncertain"] != float64(1) || counts["unmatched"] != float64(1) || counts["all"] != float64(7) {
		t.Fatalf("counts %v", counts)
	}
	uncertain := s.call(http.MethodGet, "/api/foyer/admin/library?state=uncertain", s.admin, nil)
	if items := uncertain.body["items"].([]any); len(items) != 1 || items[0].(map[string]any)["title"] != "Twin Title" {
		t.Fatalf("uncertain: %v", uncertain.body)
	}
}

func TestASecondScanWalksOnlyWhatChanged(t *testing.T) {
	s := scanLibrary(t)
	before := s.zurg.Propfinds("movies/Twin Title")
	if err := s.server.Library.Scan(context.Background(), s.movies, false); err != nil {
		t.Fatal(err)
	}
	if after := s.zurg.Propfinds("movies/Twin Title"); after != before {
		t.Fatalf("an unchanged torrent was walked again: %d → %d", before, after)
	}
	// A torrent Zurg no longer lists is let go after the grace period, and its
	// film with it — the other film's files stay.
	s.zurg.Remove("movies/Nothing.Known.1999.1080p")
	if err := s.server.Library.Scan(context.Background(), s.movies, false); err != nil {
		t.Fatal(err)
	}
	s.title("movies", "Nothing Known")
	s.server.Library.SetClock(func() time.Time { return friday.Add(80 * time.Hour) })
	if err := s.server.Library.Scan(context.Background(), s.movies, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.app.FindFirstRecordByFilter("movies", "title = 'Nothing Known'"); err == nil {
		t.Fatal("a film with no file left is still there")
	}
	if n, _ := s.app.CountRecords("movies"); n != 3 {
		t.Fatalf("%d films", n)
	}
}

func TestASuperusersCorrectionSticks(t *testing.T) {
	s := scanLibrary(t)
	twin := s.title("movies", "Twin Title")
	if got := s.call(http.MethodPost, "/api/foyer/admin/library/movies/"+twin.Id+"/confirm", s.admin, nil); got.status != http.StatusNoContent {
		t.Fatalf("confirm: %d %v", got.status, got.body)
	}
	twin = s.title("movies", "Twin Title")
	if twin.GetString("match_state") != "manual" || twin.GetString("identity") != "tmdb:movie:1003" {
		t.Fatalf("twin: %s %q", twin.GetString("match_state"), twin.GetString("identity"))
	}
	alex := s.makeUser("alex", nil)
	if got := s.call(http.MethodGet, "/api/collections/movies/records/"+twin.Id, alex.token, nil); got.status != http.StatusOK {
		t.Fatalf("alex reading the confirmed twin: %d", got.status)
	}
	nothing := s.title("movies", "Nothing Known")
	if got := s.call(http.MethodPost, "/api/foyer/admin/library/movies/"+nothing.Id+"/match", s.admin, map[string]any{"tmdbId": 1009}); got.status != http.StatusNoContent {
		t.Fatalf("match: %d %v", got.status, got.body)
	}
	known := s.title("movies", "Known After All")
	if known.GetString("match_state") != "manual" || known.GetString("identity") != "tmdb:movie:1009" || known.GetInt("version_count") != 1 {
		t.Fatalf("known: %v", known.PublicExport())
	}
	// A later scan never asks TMDB about it again, nor undoes it.
	if err := s.server.Library.Scan(context.Background(), s.movies, true); err != nil {
		t.Fatal(err)
	}
	if again := s.title("movies", "Known After All"); again.GetString("match_state") != "manual" {
		t.Fatalf("after a scan: %s", again.GetString("match_state"))
	}
	// Naming a film as another that is in the library merges the two.
	if got := s.call(http.MethodPost, "/api/foyer/admin/library/movies/"+known.Id+"/match", s.admin, map[string]any{"tmdbId": 1001}); got.status != http.StatusNoContent {
		t.Fatalf("merge: %d %v", got.status, got.body)
	}
	if film := s.title("movies", "Some Film"); film.GetInt("version_count") != 3 {
		t.Fatalf("merged: %d versions", film.GetInt("version_count"))
	}
	if _, err := s.app.FindRecordById("movies", known.Id); err == nil {
		t.Fatal("the merged film is still there")
	}
}

func TestTheWebhookNeedsItsToken(t *testing.T) {
	s := scanLibrary(t)
	hook := s.call(http.MethodGet, "/api/foyer/admin/webdav/hook", s.admin, nil)
	token, _ := hook.body["token"].(string)
	if hook.status != http.StatusOK || len(token) < 20 || !strings.Contains(hook.body["command"].(string), token) {
		t.Fatalf("hook: %d %v", hook.status, hook.body)
	}
	if got := s.call(http.MethodPost, "/api/foyer/webdav/updated", "Bearer wrong", nil); got.status != http.StatusForbidden {
		t.Fatalf("a wrong token: %d", got.status)
	}
	if got := s.call(http.MethodPost, "/api/foyer/webdav/updated", "Bearer "+token, nil); got.status != http.StatusAccepted {
		t.Fatalf("the token: %d %v", got.status, got.body)
	}
	// Asked again, the same token: Zurg's config keeps working.
	if again := s.call(http.MethodGet, "/api/foyer/admin/webdav/hook", s.admin, nil); again.body["token"] != token {
		t.Fatal("a new token every time")
	}
}
