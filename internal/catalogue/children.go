package catalogue

import (
	"cmp"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/identity"
	"foyer/internal/library"
	"foyer/internal/media"
	"foyer/internal/titles"
	"foyer/internal/watch"
)

func (s *Service) detail(re *core.RequestEvent) error {
	collection := re.Request.PathValue("collection")
	// A library's name never ends in _episodes: these are a series' episodes.
	if shows, ok := strings.CutSuffix(collection, "_episodes"); ok {
		return s.episodePage(re, shows, collection, re.Request.PathValue("id"))
	}
	r, kind, ok := s.titleRecord(collection, re.Request.PathValue("id"))
	if !ok {
		return re.NotFoundError("", nil)
	}
	items := []Title{titleOf(r, kind)}
	if err := s.annotate(re.Auth.Id, items); err != nil {
		return re.InternalServerError("", err)
	}
	versions, err := s.versionsOf(r)
	if err != nil {
		return re.InternalServerError("", err)
	}
	extras, err := s.extrasOf(r)
	if err != nil {
		return re.InternalServerError("", err)
	}
	d := Detail{Title: items[0], Tagline: r.GetString("tagline"), Versions: versions, Extras: make([]extraView, 0, len(extras))}
	for _, x := range extras {
		d.Extras = append(d.Extras, extraView{File: x.File, Kind: x.Kind, Title: x.Title, Size: x.Size})
	}
	if kind == library.Shows {
		if d.Seasons, err = s.seasonsOf(re.Auth.Id, r, extras); err != nil {
			return re.InternalServerError("", err)
		}
	}
	return re.JSON(http.StatusOK, d)
}

// EpisodePage is an episode's page: the episode, its series as a page
// lists it, and its files.
type EpisodePage struct {
	Episode  Episode        `json:"episode"`
	Show     Title          `json:"show"`
	Versions []media.Public `json:"versions"`
}

func (s *Service) episodePage(re *core.RequestEvent, shows, collection, id string) error {
	if kind, ok := s.shelfOf(shows); !ok || kind != library.Shows {
		return re.NotFoundError("", nil)
	}
	record, err := s.app.FindRecordById(collection, id)
	if err != nil {
		return re.NotFoundError("", nil)
	}
	show, _, ok := s.titleRecord(shows, record.GetString("show"))
	if !ok {
		return re.NotFoundError("", nil)
	}
	page := EpisodePage{Episode: episodeOf(record, show.Id)}
	states, err := s.states(re.Auth.Id, []string{page.Episode.Identity})
	if err != nil {
		return re.InternalServerError("", err)
	}
	if state, ok := states[page.Episode.Identity]; ok && page.Episode.Identity != "" {
		page.Episode.Watch = watchOf(state)
	}
	titles := []Title{titleOf(show, library.Shows)}
	if err := s.annotate(re.Auth.Id, titles); err != nil {
		return re.InternalServerError("", err)
	}
	page.Show = titles[0]
	if page.Versions, err = s.versionsOf(record); err != nil {
		return re.InternalServerError("", err)
	}
	return re.JSON(http.StatusOK, page)
}

func (s *Service) seasons(re *core.RequestEvent) error {
	show, kind, ok := s.titleRecord(re.Request.PathValue("collection"), re.Request.PathValue("id"))
	if !ok || kind != library.Shows {
		return re.NotFoundError("", nil)
	}
	extras, err := s.extrasOf(show)
	if err != nil {
		return re.InternalServerError("", err)
	}
	seasons, err := s.seasonsOf(re.Auth.Id, show, extras)
	if err != nil {
		return re.InternalServerError("", err)
	}
	return re.JSON(http.StatusOK, map[string]any{"seasons": seasons})
}

func (s *Service) season(re *core.RequestEvent) error {
	collection := re.Request.PathValue("collection")
	show, kind, ok := s.titleRecord(collection, re.Request.PathValue("id"))
	if !ok || kind != library.Shows {
		return re.NotFoundError("", nil)
	}
	var episodes []Episode
	key := re.Request.PathValue("season")
	if key == "extras" {
		extras, err := s.extrasOf(show)
		if err != nil {
			return re.InternalServerError("", err)
		}
		for _, x := range extras {
			episodes = append(episodes, extraEpisode(show, x))
		}
	} else {
		number, err := strconv.Atoi(key)
		if err != nil || number < 0 {
			return re.NotFoundError("", nil)
		}
		records, err := s.app.FindRecordsByFilter(library.Episodes(collection), "show = {:show} && season = {:season} && available = true",
			"episode,episode_end", 0, 0, dbx.Params{"show": show.Id, "season": number})
		if err != nil {
			return re.InternalServerError("", err)
		}
		for _, r := range records {
			episodes = append(episodes, episodeOf(r, show.Id))
		}
	}
	identities := make([]string, len(episodes))
	for i, e := range episodes {
		identities[i] = e.Identity
	}
	states, err := s.states(re.Auth.Id, identities)
	if err != nil {
		return re.InternalServerError("", err)
	}
	for i := range episodes {
		if state, ok := states[episodes[i].Identity]; ok && episodes[i].Identity != "" {
			episodes[i].Watch = watchOf(state)
		}
	}
	return re.JSON(http.StatusOK, map[string]any{"episodes": orEmpty(episodes)})
}

// versionsOf are a title's files still on the share, the one to play first
// first. A series' are its episodes'.
func (s *Service) versionsOf(r *core.Record) ([]media.Public, error) {
	var all []media.Version
	_ = r.UnmarshalJSONField("versions", &all)
	ids := make([]string, len(all))
	for i, v := range all {
		ids[i] = v.File
	}
	here, err := s.here(r, ids)
	if err != nil {
		return nil, err
	}
	out := []media.Public{}
	for _, v := range all {
		if here[v.File] {
			out = append(out, v.Public())
		}
	}
	return out, nil
}

// extra is one of a title's extras as the library keeps it.
type extra struct {
	File  string `json:"file"`
	Path  string `json:"path"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Size  int64  `json:"size"`
}

// name is what an extra is called: its title, else its file's name.
func (x extra) name() string {
	if x.Title != "" {
		return x.Title
	}
	return strings.TrimSuffix(path.Base(x.Path), path.Ext(x.Path))
}

// extrasOf are a title's extras whose files are still there, by name.
func (s *Service) extrasOf(r *core.Record) ([]extra, error) {
	var all []extra
	_ = r.UnmarshalJSONField("extras", &all)
	ids := make([]string, len(all))
	for i, x := range all {
		ids[i] = x.File
	}
	here, err := s.here(r, ids)
	if err != nil {
		return nil, err
	}
	kept := slices.DeleteFunc(all, func(x extra) bool { return !here[x.File] })
	slices.SortFunc(kept, func(a, b extra) int {
		return cmp.Or(strings.Compare(titles.Lower(a.name()), titles.Lower(b.name())), strings.Compare(a.File, b.File))
	})
	return kept, nil
}

// here says which of a title's files are still on the share.
func (s *Service) here(r *core.Record, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	files, err := s.app.FindRecordsByIds(library.FilesCollection, ids)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.GetString("missing_since") == "" && f.GetString("item_id") == r.Id {
			out[f.Id] = true
		}
	}
	return out, nil
}

// extraEpisode is one of a series' extras, played as an episode — by the
// series' record and its file — with watch status of its own.
func extraEpisode(show *core.Record, x extra) Episode {
	return Episode{
		Collection: show.Collection().Name,
		ID:         "extra:" + x.File,
		Show:       show.Id,
		File:       x.File,
		Title:      x.name(),
		Identity:   identity.Extra(show.GetString("identity"), x.Kind, x.Title, x.Path),
		Images:     Images{},
	}
}

// seasonRow is one season as a series holds it, and how much of it one user
// watched.
type seasonRow struct {
	Season   int    `db:"season"`
	Episodes int    `db:"episodes"`
	Watched  int    `db:"watched"`
	FirstAir string `db:"first_air"`
}

// seasonsOf are a series' seasons as Foyer holds them, in order — specials
// first, as season 0 — named as TMDB names them, then its extras as a season
// of their own.
func (s *Service) seasonsOf(user string, show *core.Record, extras []extra) ([]Season, error) {
	var rows []seasonRow
	err := s.app.DB().NewQuery(
		"SELECT e.[[season]] AS season, COUNT(*) AS episodes, " +
			"COALESCE(SUM(CASE WHEN w.[[watched]] THEN 1 ELSE 0 END), 0) AS watched, " +
			"COALESCE(MIN(NULLIF(e.[[air_date]], '')), '') AS first_air " +
			"FROM {{" + library.Episodes(show.Collection().Name) + "}} e LEFT JOIN {{" + watch.Collection + "}} w " +
			"ON w.[[user]] = {:user} AND w.[[identity]] = e.[[identity]] AND e.[[identity]] != '' " +
			"WHERE e.[[show]] = {:show} AND e.[[available]] = TRUE GROUP BY e.[[season]] ORDER BY e.[[season]]").
		Bind(dbx.Params{"user": user, "show": show.Id}).All(&rows)
	if err != nil {
		return nil, err
	}
	var named []struct {
		Number     int    `json:"number"`
		Name       string `json:"name"`
		AirDate    string `json:"air_date"`
		PosterPath string `json:"poster_path"`
	}
	_ = show.UnmarshalJSONField("seasons", &named)
	seasons := make([]Season, 0, len(rows)+1)
	for _, row := range rows {
		number, unwatched := row.Season, row.Episodes-row.Watched
		season := Season{Key: strconv.Itoa(number), Number: &number, Name: "Season " + strconv.Itoa(number),
			AirDate: row.FirstAir, EpisodeCount: row.Episodes, Unwatched: &unwatched}
		if number == 0 {
			season.Name = "Specials"
		}
		for _, n := range named {
			if n.Number != number {
				continue
			}
			if n.Name != "" {
				season.Name = n.Name
			}
			if n.AirDate != "" {
				season.AirDate = n.AirDate
			}
			season.PosterPath = n.PosterPath
		}
		seasons = append(seasons, season)
	}
	if len(extras) > 0 {
		seasons = append(seasons, Season{Key: "extras", Name: "Extras", EpisodeCount: len(extras)})
	}
	return seasons, nil
}

func orEmpty[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}
