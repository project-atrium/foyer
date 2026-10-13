package stalker

import (
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"

	"foyer/internal/titles"
)

// The portal's answers in Foyer's terms. A portal's own ids hold colons
// (`18390:18390`), so every part of a key made of them is encoded as the
// app's adapter encodes it — the same channel has the same key on Foyer and
// on a device that reaches the portal itself. A `cmd` never goes into a key:
// on some portals it is the stream's address, sign-in and all.

// VodType is where a portal keeps a film or a series: its films' section, or
// a series section of its own, which not every portal has — an older one
// mixes its series into the films — and the two answer different calls for
// the same thing, so what a series came from travels with it.
type VodType string

const (
	Films  VodType = "vod"
	Series VodType = "series"
)

func (v VodType) letter() string {
	if v == Series {
		return "s"
	}
	return "v"
}

// Keys a portal's things go by.
func ChannelKey(id string) string { return "ch:" + EncodeURIComponent(id) }
func MovieKey(id string) string   { return "vod:" + EncodeURIComponent(id) }
func ShowKey(id string, from VodType) string {
	return "show:" + from.letter() + ":" + EncodeURIComponent(id)
}
func SeasonKey(show, season string, from VodType) string {
	return "season:" + from.letter() + ":" + EncodeURIComponent(show) + ":" + EncodeURIComponent(season)
}
func EpisodeKey(show, season, episode string, from VodType) string {
	return "episode:" + from.letter() + ":" + EncodeURIComponent(show) + ":" + EncodeURIComponent(season) + ":" + EncodeURIComponent(episode)
}

// Parsed is what a key names.
type Parsed struct {
	// channel, movie, show, season or episode.
	Kind                      string
	From                      VodType
	ID, Show, Season, Episode string
}

// ParseKey reads a key back; false for one no portal's thing goes by.
func ParseKey(key string) (Parsed, bool) {
	parts := strings.Split(key, ":")
	kind, rest := parts[0], parts[1:]
	part := func(i int) (string, bool) {
		if i >= len(rest) {
			return "", false
		}
		v, err := url.PathUnescape(strings.ReplaceAll(rest[i], "+", "%2B"))
		return v, err == nil && v != ""
	}
	if kind == "ch" || kind == "vod" {
		id, ok := part(0)
		if !ok {
			return Parsed{}, false
		}
		if kind == "ch" {
			return Parsed{Kind: "channel", ID: id}, true
		}
		return Parsed{Kind: "movie", ID: id}, true
	}
	var from VodType
	switch {
	case len(rest) > 0 && rest[0] == "s":
		from = Series
	case len(rest) > 0 && rest[0] == "v":
		from = Films
	default:
		return Parsed{}, false
	}
	a, okA := part(1)
	b, okB := part(2)
	c, okC := part(3)
	switch {
	case kind == "show" && okA:
		return Parsed{Kind: "show", From: from, ID: a}, true
	case kind == "season" && okA && okB:
		return Parsed{Kind: "season", From: from, Show: a, Season: b}, true
	case kind == "episode" && okA && okB && okC:
		return Parsed{Kind: "episode", From: from, Show: a, Season: b, Episode: c}, true
	}
	return Parsed{}, false
}

var (
	yearPattern = regexp.MustCompile(`^(1[89]\d\d|20\d\d)\b`)
	idPattern   = regexp.MustCompile(`^[A-Za-z0-9]+$`)
	imdbPattern = regexp.MustCompile(`^tt\d+$`)
	httpPattern = regexp.MustCompile(`(?i)^https?://`)
	dayPattern  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	wallPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2})(?::(\d{2}))?`)
	playerHint  = regexp.MustCompile(`(?i)^(ffmpeg|ffrt\d?|auto)\s+`)
	containerOf = regexp.MustCompile(`\.(mp4|mkv|avi|m4v|mov|webm|ts)$`)
)

// yearOf is a year a portal writes as a year, a date (`1999-03-31`) or "N/A".
func yearOf(v any) int {
	digits := text(v)
	if m := yearPattern.FindStringSubmatch(strings.TrimSpace(digits)); m != nil {
		return int(jsNumber(m[1]))
	}
	return 0
}

// idOf is an id a portal sends as a number or a string — and "", 0 or "N/A"
// for none.
func idOf(v any) string {
	id := text(v)
	if _, ok := v.(string); ok {
		id = strings.TrimSpace(id)
	}
	if id == "" || id == "0" || !idPattern.MatchString(id) {
		return ""
	}
	return id
}

// ExternalIDs are the catalogues a portal matched a film or series to — TMDB
// above all, which many fill in — so its copies in other languages and
// qualities, each a title of its own on the portal, are known as one.
type ExternalIDs struct {
	TMDB      string `json:"tmdb,omitempty"`
	IMDB      string `json:"imdb,omitempty"`
	Kinopoisk string `json:"kinopoisk,omitempty"`
}

func externalIDsOf(row map[string]any) ExternalIDs {
	tmdb := idOf(row["tmdb_id"])
	if tmdb == "" {
		tmdb = idOf(row["tmdb"])
	}
	imdb := idOf(row["imdb_id"])
	if !imdbPattern.MatchString(imdb) {
		imdb = ""
	}
	return ExternalIDs{TMDB: tmdb, IMDB: imdb, Kinopoisk: idOf(row["kinopoisk_id"])}
}

// image is artwork: an address, or a path on the portal.
func image(v any, root string) string {
	path := text(v)
	switch {
	case path == "":
		return ""
	case httpPattern.MatchString(path):
		return path
	case root == "":
		return ""
	case strings.HasPrefix(path, "/"):
		return root + path
	}
	return root + "/" + path
}

// Group is one of the portal's channel groups.
type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Groups are the portal's genres, its "All" left out: "*" is no group of its own.
func Groups(js any) []Group {
	out := []Group{}
	for _, entry := range list(js) {
		genre := record(entry)
		id, title := text(genre["id"]), text(genre["title"])
		if id != "" && title != "" && id != "*" {
			out = append(out, Group{ID: id, Name: title})
		}
	}
	return out
}

// Page is a page of rows as the portal pages them, with what it says of its
// pages where it says it.
type Page[T any] struct {
	Rows             []T
	Page             int
	Total, PerPage   int
	HasTotal, HasPer bool
}

// More says whether the portal has pages after this one.
func (p Page[T]) More() bool {
	return p.HasTotal && p.HasPer && len(p.Rows) > 0 && p.Page*p.PerPage < p.Total
}

func pageOf[T any](js any, requested int, read func(row map[string]any) (T, bool)) Page[T] {
	body := record(js)
	page := Page[T]{Rows: []T{}}
	for _, entry := range list(body["data"]) {
		if row := record(entry); row != nil {
			if mapped, ok := read(row); ok {
				page.Rows = append(page.Rows, mapped)
			}
		}
	}
	if total, ok := number(body["total_items"]); ok {
		page.Total, page.HasTotal = int(total), true
	}
	if per, ok := number(body["max_page_items"]); ok {
		page.PerPage, page.HasPer = int(per), true
	}
	page.Page = requested
	if at, ok := number(body["cur_page"]); ok && at != 0 {
		page.Page = int(at)
	}
	return page
}

// Channel is one channel as the portal lists it.
type Channel struct {
	// ID is the portal's own.
	ID        string
	Name      string
	Number    float64
	HasNumber bool
	GroupID   string
	Logo      string
	// CatchupDays is how far back it can be played, where it can be.
	CatchupDays int
	// Country is where it is from, where something about it says so.
	Country string
	// Cmd is what create_link takes. It is never sent to a device.
	Cmd string
}

// Channels is a page of channels; titleOf gives a group's title by its id,
// where the groups are known, since a group's name can say the country.
func Channels(js any, requested int, root string, titleOf func(groupID string) string) Page[Channel] {
	return pageOf(js, requested, func(row map[string]any) (Channel, bool) { return ChannelOf(row, root, titleOf) })
}

// ChannelOf is one channel's row.
func ChannelOf(row map[string]any, root string, titleOf func(groupID string) string) (Channel, bool) {
	id, name := text(row["id"]), text(row["name"])
	if id == "" || name == "" {
		return Channel{}, false
	}
	c := Channel{ID: id, Name: name, GroupID: text(row["tv_genre_id"]), Logo: image(row["logo"], root), Cmd: text(row["cmd"])}
	c.Number, c.HasNumber = number(row["number"])
	if hours, ok := number(row["tv_archive_duration"]); ok && hours != 0 && yes(row["archive"]) {
		c.CatchupDays = max(1, int(math.Floor(hours/24+0.5)))
	}
	// Where it is from: its guide id's ending (`trt1.tr`), else its group's
	// or its own name.
	group := ""
	if c.GroupID != "" && titleOf != nil {
		group = titleOf(c.GroupID)
	}
	c.Country = CountryOf(text(row["xmltv_id"]), group, name)
	return c, true
}

// Reading is how one channel's guide is read. Zone is the zone the
// connection says the portal keeps its guide in: a portal that stamps its
// own wall clock as UTC — told the box is on UTC, as Foyer always says — puts
// 20:15 in Berlin at 20:15Z, two hours late in summer. CountryZone is the
// zone of the channel's own country, where something about it says which,
// and the connection reads other countries' guides as UTC.
type Reading struct {
	Zone, CountryZone string
}

// Programme is one programme on a channel.
type Programme struct {
	Title       string
	Description string
	Start, End  time.Time
}

// entry is a programme as the portal sent it, as small as it can be kept:
// the whole guide of thousands of channels is held in memory.
type entry struct {
	title, description string
	start, stop        float64
	wall, wallTo       int64
	hasWall, hasWallTo bool
	hasStart, hasStop  bool
}

func entryOf(v any) (entry, bool) {
	row := record(v)
	if row == nil {
		return entry{}, false
	}
	e := entry{title: text(row["name"]), description: text(row["descr"])}
	e.start, e.hasStart = number(row["start_timestamp"])
	e.stop, e.hasStop = number(row["stop_timestamp"])
	e.wall, e.hasWall = wallTime(row["time"])
	e.wallTo, e.hasWallTo = wallTime(row["time_to"])
	return e, true
}

// wallTime is the portal's own wall-clock time — `time`,
// `2026-10-04 15:25:00` — read as if it were UTC, in milliseconds.
func wallTime(v any) (int64, bool) {
	m := wallPattern.FindStringSubmatch(text(v))
	if m == nil {
		return 0, false
	}
	n := func(s string) int {
		if s == "" {
			return 0
		}
		return int(jsNumber(s))
	}
	return time.Date(n(m[1]), time.Month(n(m[2])), n(m[3]), n(m[4]), n(m[5]), n(m[6]), 0, time.UTC).UnixMilli(), true
}

// ProgrammeOf is one programme. Without a zone, its times are as sent.
//
// Another country's guide can be written in UTC and taken by the portal for
// its own clock: on a German portal, Show TV's weekend news at 18:25 in
// Istanbul (15:25Z) came stamped 13:25Z, its `time` 15:25 — two hours early in
// summer. A channel whose country's zone keeps another offset than the
// portal's clock — `time` against the stamp, at that programme — is that
// case: its `time` is read as UTC. One of the portal's own country, or of none
// known, stays as sent.
func ProgrammeOf(v any, r Reading) (Programme, bool) {
	e, ok := entryOf(v)
	if !ok {
		return Programme{}, false
	}
	return e.programme(r)
}

func (e entry) programme(r Reading) (Programme, bool) {
	if !e.hasStart || !e.hasStop || e.title == "" || e.stop <= e.start {
		return Programme{}, false
	}
	sent := func(seconds float64) int64 {
		ms := int64(seconds * 1000)
		if r.Zone == "" {
			return ms
		}
		return FromZoneWallClock(time.UnixMilli(ms), r.Zone).UnixMilli()
	}
	// The portal's wall clock for it: its `time`, else the stamp, where the
	// connection says stamps are one.
	wall, hasWall := e.wall, e.hasWall
	if !hasWall && r.Zone != "" {
		wall, hasWall = int64(e.start*1000), true
	}
	utc := false
	if r.CountryZone != "" && hasWall {
		if own, ok := ZoneOffset(r.CountryZone, time.UnixMilli(sent(e.start))); ok {
			utc = own.Milliseconds() != wall-sent(e.start)
		}
	}
	start, end := sent(e.start), sent(e.stop)
	if utc {
		start = wall
		end = wall + int64((e.stop-e.start)*1000)
		if e.hasWallTo {
			end = e.wallTo
		}
	}
	return Programme{Title: e.title, Description: e.description, Start: time.UnixMilli(start).UTC(), End: time.UnixMilli(end).UTC()}, true
}

// GuideFor is what `get_epg_info` sends for one channel, by the portal's id.
func GuideFor(js any, channelID string) []any {
	return list(record(record(js)["data"])[channelID])
}

// Link is what `create_link` answered: an address to play, or why there is
// none.
type Link struct {
	URL, Error string
}

// LinkOf is `create_link`'s address, without the player hint a MAG box
// reads first.
func LinkOf(js any) Link {
	body := record(js)
	l := Link{Error: text(body["error"])}
	cmd := strings.TrimSpace(text(body["cmd"]))
	if u := strings.TrimSpace(playerHint.ReplaceAllString(cmd, "")); u != "" && schemePattern.MatchString(u) {
		l.URL = u
	}
	return l
}

// Source is how an address plays: an HLS playlist, raw MPEG-TS, or a file.
type Source struct {
	URI       string
	Protocol  string
	Container string
	Live      bool
}

// SourceFor is what an address is, by its path. A live link with no
// extension is a raw MPEG-TS stream on almost every portal.
func SourceFor(address string, live bool) Source {
	path, _, _ := strings.Cut(address, "?")
	path = strings.ToLower(path)
	if strings.HasSuffix(path, ".m3u8") {
		return Source{URI: address, Protocol: "hls", Live: live}
	}
	container := ""
	if m := containerOf.FindStringSubmatch(path); m != nil {
		container = m[1]
	}
	if live || container == "ts" {
		return Source{URI: address, Protocol: "mpegts", Container: "ts", Live: live}
	}
	return Source{URI: address, Protocol: "progressive", Container: container, Live: live}
}

// Title is a film or a series as the portal lists it.
type Title struct {
	// movie or show.
	Kind string
	// ID is the portal's own; From, where it came from.
	ID            string
	From          VodType
	Name          string
	OriginalTitle string
	ExternalIDs   ExternalIDs
	Overview      string
	Year          int
	ReleaseDate   string
	Runtime       time.Duration
	Rating        float64
	Genres        []string
	Poster        string
	// Cmd is what create_link takes. It is never sent to a device.
	Cmd string
	// Episodes are the episodes an old portal numbers on a series itself.
	Episodes []float64
}

// Key is the key the title goes by.
func (t Title) Key() string {
	if t.Kind == "show" {
		return ShowKey(t.ID, t.From)
	}
	return MovieKey(t.ID)
}

// Titles is a page of films or series from a section: everything in a
// portal's series section is a series, whatever it says.
func Titles(js any, requested int, root string, from VodType) Page[Title] {
	return pageOf(js, requested, func(row map[string]any) (Title, bool) { return titleOf(row, root, from) })
}

func titleOf(row map[string]any, root string, from VodType) (Title, bool) {
	id, name := text(row["id"]), text(row["name"])
	if id == "" || name == "" {
		return Title{}, false
	}
	t := Title{Kind: "movie", ID: id, From: from, Name: name, ExternalIDs: externalIDsOf(row), Overview: text(row["description"]), Poster: image(row["screenshot_uri"], root), Cmd: text(row["cmd"]), Genres: []string{}}
	// A date more often than a year, else "N/A" — and then the year the name
	// carries, as "Matrix (1999) DE" does.
	if t.Year = yearOf(row["year"]); t.Year == 0 {
		_, t.Year = titles.Plain(name)
	}
	if day := text(row["year"]); dayPattern.MatchString(day) {
		t.ReleaseDate = day
	}
	if original := text(row["o_name"]); original != name {
		t.OriginalTitle = original
	}
	// A portal says 1 minute where it does not know.
	if minutes, ok := number(row["time"]); ok && minutes > 1 {
		t.Runtime = time.Duration(minutes * float64(time.Minute))
	}
	if rating, ok := number(row["rating_imdb"]); ok && rating > 0 {
		t.Rating = math.Floor(rating*10+0.5) / 10
	}
	for _, genre := range strings.Split(text(row["genres_str"]), ",") {
		if genre = strings.TrimSpace(genre); genre != "" && genre != "N/A" {
			t.Genres = append(t.Genres, genre)
		}
	}
	if from == Series || yes(row["is_series"]) {
		t.Kind = "show"
		t.Episodes = numbers(row["series"])
	}
	return t, true
}

func numbers(v any) []float64 {
	var out []float64
	for _, each := range list(v) {
		if n, ok := number(each); ok {
			out = append(out, n)
		}
	}
	return out
}

// SeasonRow is a series' season as the portal lists it.
type SeasonRow struct {
	ID     string
	Number float64
	Title  string
	Cmd    string
	// Episodes are the episodes a season lists by number rather than as rows
	// of their own.
	Episodes []float64
}

// Seasons are a series' seasons, numbered in order where the portal does not.
func Seasons(js any) []SeasonRow {
	out := []SeasonRow{}
	for i, each := range list(record(js)["data"]) {
		row := record(each)
		id := text(row["id"])
		if row == nil || id == "" {
			continue
		}
		s := SeasonRow{ID: id, Cmd: text(row["cmd"]), Episodes: numbers(row["series"])}
		var ok bool
		if s.Number, ok = number(row["season_number"]); !ok {
			s.Number = float64(i + 1)
		}
		if s.Title = text(row["name"]); s.Title == "" {
			s.Title = "Season " + jsNumberString(s.Number)
		}
		out = append(out, s)
	}
	return out
}

// EpisodeRow is a season's episode as the portal lists it; its number is
// what old portals put in `create_link`'s `series`.
type EpisodeRow struct {
	ID     string
	Number float64
	Title  string
	Cmd    string
}

// Episodes are a season's episodes, numbered in order where the portal does not.
func Episodes(js any) []EpisodeRow {
	out := []EpisodeRow{}
	for i, each := range list(record(js)["data"]) {
		row := record(each)
		id := text(row["id"])
		if row == nil || id == "" {
			continue
		}
		e := EpisodeRow{ID: id, Cmd: text(row["cmd"])}
		var ok bool
		if e.Number, ok = number(row["series_number"]); !ok {
			e.Number = float64(i + 1)
		}
		if e.Title = text(row["name"]); e.Title == "" {
			e.Title = "Episode " + jsNumberString(e.Number)
		}
		out = append(out, e)
	}
	return out
}
