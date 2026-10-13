package stalker

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"foyer/internal/adapters/stalker/stalkertest"
)

// answer is a portal's answer as it arrives, read back.
func answer(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	return record(decoded(t, v))
}

func decoded(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return parse(b)
}

func at(iso string) time.Time {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		panic(err)
	}
	return t
}

func TestListsTheGenresAsGroupsLeavingOutAll(t *testing.T) {
	got := Groups(answer(t, stalkertest.Genres)["js"])
	if want := []Group{{ID: "10", Name: "News"}, {ID: "11", Name: "Sports"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
}

func TestReadsChannelsWithTheirNumbersLogosAndCatchUp(t *testing.T) {
	page := Channels(record(answer(t, stalkertest.ChannelPage(1)))["js"], 1, "http://portal.test", nil)
	if page.Total != 3 || page.PerPage != 2 || page.Page != 1 || !page.More() || len(page.Rows) != 2 {
		t.Fatalf("%+v", page)
	}
	want := Channel{ID: "101", Name: "Morning Wire", Number: 1, HasNumber: true, GroupID: "10", Logo: "http://portal.test/stalker_portal/misc/logos/320/101.png", CatchupDays: 7, Cmd: "ffmpeg http://localhost/ch/101_"}
	if page.Rows[0] != want {
		t.Errorf("%+v", page.Rows[0])
	}
	if second := page.Rows[1]; second.Logo != "http://cdn.test/102.png" || second.CatchupDays != 0 {
		t.Errorf("%+v", second)
	}
	if last := Channels(record(answer(t, stalkertest.ChannelPage(2)))["js"], 2, "", nil); last.More() || last.Rows[0].Name != "Late Show" {
		t.Errorf("%+v", last)
	}
}

func TestSaysEachChannelsCountryByItsGuideIDOrItsGroup(t *testing.T) {
	titles := map[string]string{}
	for _, g := range Groups(answer(t, stalkertest.CountryGenres)["js"]) {
		titles[g.ID] = g.Name
	}
	page := Channels(map[string]any{"data": decoded(t, stalkertest.CountryChannels)}, 1, "", func(id string) string { return titles[id] })
	var got [][2]string
	for _, c := range page.Rows {
		got = append(got, [2]string{c.ID, c.Country})
	}
	if want := [][2]string{{"12", "TR"}, {"534", "DE"}, {"24", "TR"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
}

func TestPutsTheGuideOnTheClockOfTheZoneThePortalKeepsItIn(t *testing.T) {
	// 20:15 in Berlin, in summer, stamped as 20:15Z: it airs at 18:15Z.
	stamped := at("2026-07-01T20:15:00Z").Unix()
	entry := decoded(t, map[string]any{"name": "Tagesschau", "start_timestamp": stamped, "stop_timestamp": stamped + 900})
	p, ok := ProgrammeOf(entry, Reading{Zone: "Europe/Berlin"})
	if !ok || !p.Start.Equal(at("2026-07-01T18:15:00Z")) || !p.End.Equal(at("2026-07-01T18:30:00Z")) {
		t.Errorf("%+v", p)
	}
	// As the portal says: its times as sent.
	if p, _ := ProgrammeOf(entry, Reading{}); !p.Start.Equal(at("2026-07-01T20:15:00Z")) {
		t.Errorf("%+v", p)
	}
}

func TestReadsAnotherCountrysGuideWrittenInUTCAndTakenForThePortalsClockAsUTC(t *testing.T) {
	guide := decoded(t, map[string]any{"data": stalkertest.CountryGuide})
	news, tagesschau := GuideFor(guide, "12")[0], GuideFor(guide, "534")[0]
	// Istanbul keeps another offset than the portal's clock (`time` against
	// the stamp): 18:25 there is 15:25Z.
	if p, _ := ProgrammeOf(news, Reading{CountryZone: "Europe/Istanbul"}); !p.Start.Equal(at("2026-10-04T15:25:00Z")) || !p.End.Equal(at("2026-10-04T16:45:00Z")) {
		t.Errorf("news %+v", p)
	}
	// Berlin keeps the portal's own: as sent, 20:00 there.
	if p, _ := ProgrammeOf(tagesschau, Reading{CountryZone: "Europe/Berlin"}); !p.Start.Equal(at("2026-10-04T18:00:00Z")) {
		t.Errorf("Tagesschau %+v", p)
	}
	// No country known, or no `time` to read: as sent.
	if p, _ := ProgrammeOf(news, Reading{}); !p.Start.Equal(at("2026-10-04T13:25:00Z")) {
		t.Errorf("news as sent %+v", p)
	}
	stampsOnly := map[string]any{}
	for key, value := range record(news) {
		if key != "time" && key != "time_to" {
			stampsOnly[key] = value
		}
	}
	if p, _ := ProgrammeOf(stampsOnly, Reading{CountryZone: "Europe/Istanbul"}); !p.Start.Equal(at("2026-10-04T13:25:00Z")) {
		t.Errorf("stamps only %+v", p)
	}
}

func TestKeepsTheChosenZoneForThePortalsOwnCountryAndReadsAnothersStampsAsTheyAre(t *testing.T) {
	// A portal that stamps its own Berlin wall clock as UTC: its home news,
	// sent as 20:00Z, airs at 18:00Z.
	home := decoded(t, map[string]any{"name": "Tagesschau", "start_timestamp": stalkertest.Stamp("2026-10-04T20:00:00Z"), "stop_timestamp": stalkertest.Stamp("2026-10-04T20:15:00Z"), "time": "2026-10-04 20:00:00"})
	if p, _ := ProgrammeOf(home, Reading{Zone: "Europe/Berlin", CountryZone: "Europe/Berlin"}); !p.Start.Equal(at("2026-10-04T18:00:00Z")) {
		t.Errorf("home %+v", p)
	}
	// A guide written in UTC there was stamped as written: 15:25Z is 18:25 in Istanbul.
	abroad := decoded(t, map[string]any{"name": "Show Ana Haber", "start_timestamp": stalkertest.Stamp("2026-10-04T15:25:00Z"), "stop_timestamp": stalkertest.Stamp("2026-10-04T16:45:00Z"), "time": "2026-10-04 15:25:00"})
	if p, _ := ProgrammeOf(abroad, Reading{Zone: "Europe/Berlin", CountryZone: "Europe/Istanbul"}); !p.Start.Equal(at("2026-10-04T15:25:00Z")) {
		t.Errorf("abroad %+v", p)
	}
}

func TestReadsAGuideTimeWithoutSecondsAsTheMinute(t *testing.T) {
	news := decoded(t, map[string]any{"name": "Show Ana Haber", "start_timestamp": stalkertest.Stamp("2026-10-04T13:25:00Z"), "stop_timestamp": stalkertest.Stamp("2026-10-04T14:45:00Z"), "time": "2026-10-04 15:25"})
	if p, ok := ProgrammeOf(news, Reading{CountryZone: "Europe/Istanbul"}); !ok || !p.Start.Equal(at("2026-10-04T15:25:00Z")) || !p.End.Equal(at("2026-10-04T16:45:00Z")) {
		t.Errorf("%+v", p)
	}
}

func TestLeavesOutAProgrammeWithoutATitleOrThatEndsBeforeItStarts(t *testing.T) {
	for _, row := range []map[string]any{
		{"name": "", "start_timestamp": 10, "stop_timestamp": 20},
		{"name": "A", "start_timestamp": 20, "stop_timestamp": 20},
		{"name": "A", "start_timestamp": "x", "stop_timestamp": 20},
	} {
		if p, ok := ProgrammeOf(decoded(t, row), Reading{}); ok {
			t.Errorf("%v: %+v", row, p)
		}
	}
}

func TestMakesAChannelsLinkWithoutTheMagHintRawMPEGTSSaidAsSuch(t *testing.T) {
	link := LinkOf(decoded(t, map[string]any{"id": "1", "cmd": "ffmpeg http://stream.test/live/abc123/101.ts?play_token=t0k3n", "error": ""}))
	if link.URL != "http://stream.test/live/abc123/101.ts?play_token=t0k3n" || link.Error != "" {
		t.Fatalf("%+v", link)
	}
	if s := SourceFor(link.URL, true); s != (Source{URI: link.URL, Protocol: "mpegts", Container: "ts", Live: true}) {
		t.Errorf("%+v", s)
	}
	if s := SourceFor("http://stream.test/live/103/index.m3u8", true); s.Protocol != "hls" || !s.Live {
		t.Errorf("%+v", s)
	}
	if s := SourceFor("http://stream.test/vod/501.mkv?token=x", false); s.Protocol != "progressive" || s.Container != "mkv" {
		t.Errorf("%+v", s)
	}
	if s := SourceFor("http://stream.test/live/12", true); s.Protocol != "mpegts" {
		t.Errorf("%+v", s)
	}
	if l := LinkOf(decoded(t, map[string]any{"cmd": "", "error": "limit"})); l.URL != "" || l.Error != "limit" {
		t.Errorf("%+v", l)
	}
	if l := LinkOf(decoded(t, map[string]any{"cmd": "ffmpeg /not/an/address"})); l.URL != "" {
		t.Errorf("%+v", l)
	}
}

func TestReadsFilmsAndSeriesFromAnOlderPortalsSharedPages(t *testing.T) {
	page := Titles(record(answer(t, stalkertest.VodPage))["js"], 1, "http://portal.test", Films)
	if len(page.Rows) != 3 || page.More() {
		t.Fatalf("%+v", page)
	}
	film := page.Rows[0]
	want := Title{Kind: "movie", ID: "501", From: Films, Name: "Harbor Lights", Overview: "A film.", Year: 2019, Runtime: 112 * time.Minute, Rating: 7.4, Genres: []string{"Drama", "Thriller"}, Poster: "http://cdn.test/501.jpg", Cmd: "L21lZGlhLzUwMS5tcGc="}
	if !reflect.DeepEqual(film, want) {
		t.Errorf("%+v", film)
	}
	if film.Key() != "vod:501" {
		t.Errorf("%s", film.Key())
	}
	relay, old := page.Rows[1], page.Rows[2]
	if relay.Kind != "show" || relay.Key() != "show:v:601" || old.Key() != "show:v:701" || !reflect.DeepEqual(old.Episodes, []float64{1, 2, 3}) {
		t.Errorf("%+v %+v", relay, old)
	}
	seasons := Seasons(record(answer(t, stalkertest.Seasons))["js"])
	if len(seasons) != 2 || seasons[0].ID != "61" || seasons[0].Number != 1 || seasons[1].Title != "Season 2" {
		t.Errorf("%+v", seasons)
	}
	episodes := Episodes(record(answer(t, stalkertest.Episodes))["js"])
	if len(episodes) != 2 || episodes[0] != (EpisodeRow{ID: "611", Number: 1, Title: "Pilot", Cmd: "L3Nlcmllcy82MTEubWt2"}) {
		t.Errorf("%+v", episodes)
	}
	if key := EpisodeKey("601", "61", "611", Films); key != "episode:v:601:61:611" {
		t.Errorf("%s", key)
	}
}

func TestTakesAPortalsOwnSeriesSectionIDsWithAColonAndAll(t *testing.T) {
	page := Titles(record(answer(t, stalkertest.SeriesPage))["js"], 1, "", Series)
	show := page.Rows[0]
	if show.Kind != "show" || show.Key() != "show:s:18390%3A18390" || show.Name != "Harbour Nights" {
		t.Fatalf("%+v", show)
	}
	seasons := Seasons(record(answer(t, stalkertest.SeriesSeasons))["js"])
	if len(seasons) != 1 || !reflect.DeepEqual(seasons[0].Episodes, []float64{1, 2, 3}) || SeasonKey(show.ID, seasons[0].ID, Series) != "season:s:18390%3A18390:18390%3A1" {
		t.Fatalf("%+v", seasons)
	}
	key := EpisodeKey(show.ID, seasons[0].ID, "2", Series)
	if key != "episode:s:18390%3A18390:18390%3A1:2" {
		t.Fatalf("%s", key)
	}
	parsed, ok := ParseKey(key)
	if !ok || parsed != (Parsed{Kind: "episode", From: Series, Show: "18390:18390", Season: "18390:1", Episode: "2"}) {
		t.Errorf("%+v", parsed)
	}
	for _, bad := range []string{"ch:", "show:x:1", "season:v:1", "episode:s:%E0%A4%A:1:2", "film:1"} {
		if p, ok := ParseKey(bad); ok {
			t.Errorf("%s: %+v", bad, p)
		}
	}
	if p, ok := ParseKey("ch:12"); !ok || p != (Parsed{Kind: "channel", ID: "12"}) {
		t.Errorf("%+v", p)
	}
}

func TestKnowsAFilmByTheCatalogueThePortalMatchedAndItsYearByItsDateOrName(t *testing.T) {
	// As a real portal sends them: a date for a year, TMDB's id twice, as a
	// number or a string, and 1 minute for "not known".
	rows := decoded(t, []any{
		map[string]any{"id": "11", "name": "Matrix (1999) DE 4K HDR", "o_name": "Matrix (1999) DE 4K HDR", "year": "1999-03-31", "tmdb_id": "603", "tmdb": "603", "time": 136, "is_series": 0},
		map[string]any{"id": "12", "name": "Matrix HQ", "o_name": "Matrix HQ", "year": "1999-03-31", "tmdb_id": 603, "tmdb": 603, "time": 136, "is_series": 0},
		map[string]any{"id": "13", "name": "Matrix Revolutions (2003) DE", "o_name": "The Matrix Revolutions", "year": "N/A", "tmdb_id": "", "tmdb": "", "time": 1, "is_series": 0},
	})
	page := Titles(map[string]any{"data": rows}, 1, "", Films)
	german, high, unknown := page.Rows[0], page.Rows[1], page.Rows[2]
	if german.Year != 1999 || german.ReleaseDate != "1999-03-31" || german.ExternalIDs.TMDB != "603" || german.Runtime != 136*time.Minute || german.OriginalTitle != "" {
		t.Errorf("german %+v", german)
	}
	if high.ExternalIDs != (ExternalIDs{TMDB: "603"}) {
		t.Errorf("high %+v", high)
	}
	// No catalogue, no date: the year is the one the name carries, and a
	// minute is no runtime.
	if unknown.Year != 2003 || unknown.OriginalTitle != "The Matrix Revolutions" || unknown.ExternalIDs != (ExternalIDs{}) || unknown.Runtime != 0 {
		t.Errorf("unknown %+v", unknown)
	}
}

func TestReadsTheChannelsAndTheGuideItAsksTheRealPortalFor(t *testing.T) {
	r := open(t, stalkertest.Options{Countries: true}, setup{})
	ctx := context.Background()
	js, err := r.portal.Call(ctx, "itv", "get_epg_info", Params{{"period", "25"}}, CallOptions{Lane: LaneGuide, Timeout: WholePortal})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(GuideFor(js, "12")); got != 1 {
		t.Errorf("%d programmes", got)
	}
	if asked := r.fake.Asked("get_epg_info"); len(asked) != 1 || asked[0].Query["period"] != "25" {
		t.Errorf("asked %v", asked)
	}
}
