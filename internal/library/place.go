package library

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"foyer/internal/adapters/tmdb"
	"foyer/internal/identity"
	"foyer/internal/parse"
)

// placeWaiting gives every file waiting for a title its title: new files,
// files TMDB could not be asked about, and — on a full scan — a series'
// files no episode took yet.
func (e *Engine) placeWaiting(ctx context.Context, lib *core.Record, full bool, run *run) error {
	filter := "library = {:library} && item_id = '' && missing_since = ''"
	if full {
		filter = "library = {:library} && missing_since = '' && (item_id = '' || role = 'unmapped')"
	}
	files, err := e.app.FindRecordsByFilter(FilesCollection, filter, "path", 0, 0, dbx.Params{"library": lib.Id})
	if err != nil || len(files) == 0 {
		return err
	}
	torrents, err := e.app.FindAllRecords(TorrentsCollection, dbx.HashExp{"library": lib.Id})
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, t := range torrents {
		names[t.Id] = t.GetString("name")
	}
	var waiting []*placedFile
	for _, f := range files {
		pf := &placedFile{record: f, torrent: names[f.GetString("torrent")]}
		if err := f.UnmarshalJSONField("parsed", &pf.placed); err != nil {
			continue
		}
		waiting = append(waiting, pf)
	}
	return e.placeAll(ctx, lib, plan(lib.GetString("kind"), waiting), run, nil)
}

// placeAll identifies each title and writes it. A decision may be given — a
// superuser's — or else is asked for.
func (e *Engine) placeAll(ctx context.Context, lib *core.Record, candidates []*candidate, run *run, given *decision) error {
	client, language := e.tmdb()
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		var d decision
		if given != nil {
			d = *given
		} else {
			var err error
			if d, err = e.identify(ctx, lib, c, client); err != nil {
				if errors.Is(err, errTMDBDown) {
					run.note(fmt.Sprintf("TMDB did not answer for %q; it is asked again next scan", c.title))
					continue
				}
				return err
			}
		}
		var err error
		if lib.GetString("kind") == Movies {
			err = e.placeMovie(ctx, lib, c, d, client, language)
		} else {
			err = e.placeShow(ctx, lib, c, d, client, language)
		}
		if errors.Is(err, errTMDBDown) {
			run.note(fmt.Sprintf("TMDB did not answer for %q; it is asked again next scan", c.title))
			continue
		}
		if err != nil {
			return fmt.Errorf("placing %q: %w", c.title, err)
		}
		run.update(func(p *Progress) { p.Titles++ })
	}
	return nil
}

// findTitle is the record a decision makes a title: the film or series TMDB
// named, confirmed; else the one this title made before, under its key;
// else the one of its name.
func findTitle(app core.App, collection string, c *candidate, d decision, byTitle string) *core.Record {
	if d.id != 0 && (d.state == sure || d.state == manual) {
		if r, err := app.FindFirstRecordByFilter(collection, "tmdb_id = {:id} && (match_state = 'matched' || match_state = 'manual')",
			dbx.Params{"id": d.id}); err == nil {
			return r
		}
	}
	if r, err := app.FindFirstRecordByFilter(collection, "match_key = {:key}", dbx.Params{"key": c.key}); err == nil {
		return r
	}
	if byTitle != "" {
		if r, err := app.FindFirstRecordByFilter(collection, "identity = {:identity}", dbx.Params{"identity": byTitle}); err == nil {
			return r
		}
	}
	return nil
}

// art sets an artwork's TMDB path, and clears the picture where the path
// changed, so the next artwork pass fetches the new one.
func art(r *core.Record, field, path string) {
	if r.GetString(field+"_path") == path {
		return
	}
	r.Set(field+"_path", path)
	r.Set(field, "")
}

// sortTitle is a title without its article, as a shelf orders it.
func sortTitle(title string) string {
	lower := strings.ToLower(title)
	for _, article := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(lower, article) && len(title) > len(article) {
			return title[len(article):]
		}
	}
	return title
}

func genreNames(genres []tmdb.Genre) []string {
	names := []string{}
	for _, g := range genres {
		names = append(names, g.Name)
	}
	return names
}

// titleIdentity is a title's watch identity: TMDB's id where it is sure or a
// superuser said; none while it is a guess; its name and year else.
func titleIdentity(kind string, c *candidate, d decision) string {
	switch d.matchState() {
	case "matched", "manual":
		if kind == Movies {
			return identity.Movie(d.id)
		}
		return identity.Show(d.id)
	case "uncertain":
		return ""
	}
	k := "movie"
	if kind == Shows {
		k = "show"
	}
	return identity.ByTitle(k, c.title, c.year)
}

func newRecord(app core.App, collection string) (*core.Record, error) {
	c, err := app.FindCollectionByNameOrId(collection)
	if err != nil {
		return nil, err
	}
	return core.NewRecord(c), nil
}

// placeMovie writes a film, and makes its files its versions and extras.
func (e *Engine) placeMovie(ctx context.Context, lib *core.Record, c *candidate, d decision, client *tmdb.Client, language string) error {
	var info *tmdb.MovieInfo
	if d.id != 0 && client != nil {
		movie, err := client.Movie(ctx, d.id, language)
		switch {
		case err == nil:
			info = &movie
		case errors.Is(err, tmdb.ErrNotFound):
			d = decision{state: none, reason: "TMDB no longer has it"}
		case errors.Is(err, tmdb.ErrRefused), errors.Is(err, tmdb.ErrNoKey):
		default:
			return errTMDBDown
		}
	}
	name := lib.GetString("name")
	byTitle := titleIdentity(Movies, c, decision{state: none})
	lock := e.writes(lib.Id)
	lock.Lock()
	defer lock.Unlock()
	return e.app.RunInTransaction(func(tx core.App) error {
		item := findTitle(tx, name, c, d, byTitle)
		if item == nil {
			var err error
			if item, err = newRecord(tx, name); err != nil {
				return err
			}
		}
		setTitle(item, Movies, c, d)
		if info != nil {
			item.Set("title", info.Title)
			item.Set("original_title", info.OriginalTitle)
			item.Set("sort_title", sortTitle(info.Title))
			item.Set("year", info.Year)
			item.Set("release_date", info.ReleaseDate)
			item.Set("overview", info.Overview)
			item.Set("tagline", info.Tagline)
			item.Set("runtime_ms", info.RuntimeMinutes*60_000)
			item.Set("genres", genreNames(info.Genres))
			item.Set("content_rating", info.ContentRating)
			item.Set("rating", info.Rating)
			item.Set("original_language", info.OriginalLanguage)
			item.Set("tmdb_id", info.ID)
			item.Set("imdb_id", info.ExternalIDs["imdb"])
			item.Set("tvdb_id", info.ExternalIDs["tvdb"])
			item.Set("external_ids", info.ExternalIDs)
			art(item, "poster", info.Artwork.Poster)
			art(item, "backdrop", info.Artwork.Backdrop)
			art(item, "logo", info.Artwork.Logo)
		}
		if err := tx.Save(item); err != nil {
			return err
		}
		for _, f := range c.files {
			f.record.Set("role", f.role(Movies))
			f.record.Set("item_collection", name)
			f.record.Set("item_id", item.Id)
			f.record.Set("match_key", c.key)
			if err := tx.Save(f.record); err != nil {
				return err
			}
		}
		if err := refresh(tx, item); err != nil {
			return err
		}
		return tx.Save(item)
	})
}

// setTitle sets what every title has whatever TMDB said: its state, its key,
// its identity — and, where TMDB said nothing yet, its name's title and year.
func setTitle(item *core.Record, kind string, c *candidate, d decision) {
	item.Set("match_state", d.matchState())
	item.Set("match_key", c.key)
	item.Set("identity", titleIdentity(kind, c, d))
	item.Set("metadata_at", types.NowDateTime())
	if d.id == 0 {
		title := c.title
		if title == "" {
			title = "Untitled"
		}
		item.Set("title", title)
		item.Set("sort_title", sortTitle(title))
		item.Set("year", c.year)
		item.Set("tmdb_id", 0)
	}
}

// placeShow writes a series, its episodes as TMDB numbers them, and makes its
// files their versions — or the series' extras, or files no episode took
// yet.
func (e *Engine) placeShow(ctx context.Context, lib *core.Record, c *candidate, d decision, client *tmdb.Client, language string) error {
	var info *tmdb.ShowInfo
	var cat *catalog
	absoluteGroup := ""
	if d.id != 0 && client != nil {
		show, err := client.Show(ctx, d.id, language)
		switch {
		case err == nil:
			info = &show
			cat, absoluteGroup, err = e.catalogOf(ctx, client, show, language, c)
			if err != nil {
				return errTMDBDown
			}
		case errors.Is(err, tmdb.ErrNotFound):
			d = decision{state: none, reason: "TMDB no longer has it"}
		case errors.Is(err, tmdb.ErrRefused), errors.Is(err, tmdb.ErrNoKey):
		default:
			return errTMDBDown
		}
	}
	name := lib.GetString("name")
	episodes := Episodes(name)
	byTitle := titleIdentity(Shows, c, decision{state: none})
	lock := e.writes(lib.Id)
	lock.Lock()
	defer lock.Unlock()
	return e.app.RunInTransaction(func(tx core.App) error {
		show := findTitle(tx, name, c, d, byTitle)
		if show == nil {
			var err error
			if show, err = newRecord(tx, name); err != nil {
				return err
			}
		}
		setTitle(show, Shows, c, d)
		if info != nil {
			show.Set("title", info.Name)
			show.Set("original_title", info.OriginalName)
			show.Set("sort_title", sortTitle(info.Name))
			show.Set("year", info.Year)
			show.Set("first_air_date", info.FirstAirDate)
			show.Set("last_air_date", info.LastAirDate)
			show.Set("status", info.Status)
			show.Set("overview", info.Overview)
			show.Set("tagline", info.Tagline)
			show.Set("genres", genreNames(info.Genres))
			show.Set("content_rating", info.ContentRating)
			show.Set("rating", info.Rating)
			show.Set("original_language", info.OriginalLanguage)
			show.Set("season_count", info.SeasonCount)
			show.Set("episode_count", info.EpisodeCount)
			show.Set("seasons", info.Seasons)
			show.Set("tmdb_id", info.ID)
			show.Set("imdb_id", info.ExternalIDs["imdb"])
			show.Set("tvdb_id", info.ExternalIDs["tvdb"])
			show.Set("external_ids", info.ExternalIDs)
			numbering := "per-season"
			if cat != nil && cat.absoluteInSeason() {
				numbering = "absolute"
			}
			show.Set("numbering", numbering)
			show.Set("absolute_group", absoluteGroup)
			art(show, "poster", info.Artwork.Poster)
			art(show, "backdrop", info.Artwork.Backdrop)
			art(show, "logo", info.Artwork.Logo)
		}
		if err := tx.Save(show); err != nil {
			return err
		}
		showIdentity := show.GetString("identity")

		touched := map[[2]int]*core.Record{}
		var order [][2]int
		for _, f := range c.files {
			f.record.Set("match_key", c.key)
			role := f.role(Shows)
			p, ok := placement{}, false
			if role == "version" {
				p, ok = place(cat, f.placed.Release)
				if !ok {
					role = "unmapped"
				}
			}
			if role != "version" {
				f.record.Set("role", role)
				f.record.Set("item_collection", name)
				f.record.Set("item_id", show.Id)
				if err := tx.Save(f.record); err != nil {
					return err
				}
				continue
			}
			key := [2]int{p.season, p.episode}
			ep, seen := touched[key]
			if !seen {
				found, err := tx.FindFirstRecordByFilter(episodes, "show = {:show} && season = {:season} && episode = {:episode}",
					dbx.Params{"show": show.Id, "season": p.season, "episode": p.episode})
				if err == nil {
					ep = found
				} else if ep, err = newRecord(tx, episodes); err != nil {
					return err
				}
				touched[key] = ep
				order = append(order, key)
			}
			setEpisode(ep, show, p, cat, f.placed.Release, showIdentity)
			if err := tx.Save(ep); err != nil {
				return err
			}
			f.record.Set("role", "version")
			f.record.Set("item_collection", episodes)
			f.record.Set("item_id", ep.Id)
			if err := tx.Save(f.record); err != nil {
				return err
			}
		}
		for _, key := range order {
			ep := touched[key]
			if err := refresh(tx, ep); err != nil {
				return err
			}
			if err := tx.Save(ep); err != nil {
				return err
			}
		}
		if err := refreshShow(tx, show); err != nil {
			return err
		}
		return tx.Save(show)
	})
}

// setEpisode sets what an episode is: where it lies, what TMDB says of it,
// and its watch identity under its series'.
func setEpisode(ep, show *core.Record, p placement, cat *catalog, r parse.Release, showIdentity string) {
	ep.Set("show", show.Id)
	ep.Set("season", p.season)
	ep.Set("episode", p.episode)
	if p.end > ep.GetInt("episode_end") {
		ep.Set("episode_end", p.end)
	}
	if p.absolute != 0 {
		ep.Set("absolute", p.absolute)
	}
	ep.Set("mapped_by", p.by)
	if showIdentity != "" {
		ep.Set("identity", identity.Episode(showIdentity, p.season, p.episode))
	} else {
		ep.Set("identity", "")
	}
	if cat != nil {
		if info, ok := cat.episode(p.season, p.episode); ok {
			title := info.Name
			if title == "" {
				title = fmt.Sprintf("Episode %d", p.episode)
			}
			ep.Set("title", title)
			ep.Set("overview", info.Overview)
			ep.Set("air_date", info.AirDate)
			ep.Set("runtime_ms", info.RuntimeMinutes*60_000)
			ep.Set("rating", info.Rating)
			ep.Set("tmdb_id", info.ID)
			art(ep, "still", info.Still)
			return
		}
	}
	if ep.GetString("title") == "" || ep.GetInt("tmdb_id") == 0 {
		title := r.EpisodeTitle
		if title == "" {
			title = fmt.Sprintf("Episode %d", p.episode)
		}
		ep.Set("title", title)
	}
}

// catalogOf fetches the seasons a series' files need — every one where a
// file is counted from the start or is a special — and its absolute ordering
// where TMDB numbers each season from one.
func (e *Engine) catalogOf(ctx context.Context, client *tmdb.Client, show tmdb.ShowInfo, language string, c *candidate) (*catalog, string, error) {
	everything := false
	wanted := map[int]bool{}
	for _, f := range c.files {
		r := f.placed.Release
		switch {
		case len(r.Absolute) > 0, r.Special != "":
			everything = true
		case len(r.Seasons) == 1:
			wanted[r.Seasons[0]] = true
		}
	}
	var numbers []int
	for _, s := range show.Seasons {
		if everything || wanted[s.Number] {
			numbers = append(numbers, s.Number)
		}
	}
	if len(numbers) == 0 {
		return newCatalog(nil, nil), "", nil
	}
	slices.Sort(numbers)
	seasons, err := client.Seasons(ctx, show.ID, language, numbers)
	if err != nil {
		return nil, "", err
	}
	cat := newCatalog(seasons, nil)
	if !everything || cat.absoluteInSeason() {
		return cat, "", nil
	}
	hasAbsolute := slices.ContainsFunc(c.files, func(f *placedFile) bool { return len(f.placed.Absolute) > 0 })
	if !hasAbsolute {
		return cat, "", nil
	}
	groups, err := client.EpisodeGroups(ctx, show.ID)
	if err != nil {
		return cat, "", nil
	}
	best := -1
	for i, g := range groups {
		if g.Type != tmdb.EpisodeGroupAbsolute {
			continue
		}
		if best < 0 || abs(g.EpisodeCount-show.EpisodeCount) < abs(groups[best].EpisodeCount-show.EpisodeCount) {
			best = i
		}
	}
	if best < 0 {
		return cat, "", nil
	}
	ordered, err := client.Ordered(ctx, groups[best].ID)
	if err != nil {
		return cat, "", nil
	}
	cat.absolute = ordered
	return cat, groups[best].ID, nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
