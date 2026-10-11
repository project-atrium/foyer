package catalogue

import (
	"net/http"
	"slices"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/identity"
	"foyer/internal/library"
	"foyer/internal/titles"
	"foyer/internal/watch"
)

// resumeFrom is how far in something must be to be resumed: less is a look.
const resumeFrom = 30_000

func (s *Service) genres(re *core.RequestEvent) error {
	kind, ok := kindOf(re.Request.URL.Query())
	if !ok {
		return re.BadRequestError("Genres are of movies or shows.", nil)
	}
	seen := map[string]bool{}
	genres := []string{}
	for _, name := range s.shelves(kind) {
		var rows []struct {
			Genre string `db:"genre"`
		}
		err := s.app.DB().NewQuery("SELECT DISTINCT j.value AS genre FROM {{" + name + "}} t, json_each(t.[[genres]]) j " +
			"WHERE t.[[match_state]] != 'uncertain' AND t.[[available]] = TRUE AND j.type = 'text'").All(&rows)
		if err != nil {
			return re.InternalServerError("", err)
		}
		// One genre once, spelled as it came first: the app keys them alike.
		for _, row := range rows {
			if key := titles.GenreKey(row.Genre); key != "" && !seen[key] {
				seen[key] = true
				genres = append(genres, row.Genre)
			}
		}
	}
	slices.SortFunc(genres, func(a, b string) int { return strings.Compare(titles.Lower(a), titles.Lower(b)) })
	return re.JSON(http.StatusOK, map[string]any{"genres": genres})
}

// resume is what a user began and has not finished, the latest first: a
// film as its title, an episode — or a series' extra — with its series.
func (s *Service) resume(re *core.RequestEvent) error {
	limit, ok := limitOf(re.Request.URL.Query(), 20)
	if !ok {
		return re.BadRequestError("A limit is a whole number above 0.", nil)
	}
	const chunk = 100
	var items []ResumeItem
	params := dbx.Params{"user": re.Auth.Id, "from": resumeFrom}
	// A state whose title is gone or hidden is passed over: read on until the
	// page is full, a thousand states at most.
	for offset := 0; len(items) < limit && offset < 1000; offset += chunk {
		states, err := s.app.FindRecordsByFilter(watch.Collection, "user = {:user} && position_ms >= {:from}", "-last_played_at", chunk, offset, params)
		if err != nil {
			return re.InternalServerError("", err)
		}
		for _, state := range states {
			if len(items) == limit {
				break
			}
			if item, ok := s.resumed(state); ok {
				items = append(items, item)
			}
		}
		if len(states) < chunk {
			break
		}
	}
	// The titles' own watch status and holdings, laid on in one go.
	var shown []Title
	for _, item := range items {
		if item.Title != nil {
			shown = append(shown, *item.Title)
		} else {
			shown = append(shown, *item.Show)
		}
	}
	if err := s.annotate(re.Auth.Id, shown); err != nil {
		return re.InternalServerError("", err)
	}
	for i := range items {
		t := shown[i]
		if items[i].Title != nil {
			items[i].Title = &t
		} else {
			items[i].Show = &t
		}
	}
	return re.JSON(http.StatusOK, map[string]any{"items": orEmpty(items)})
}

// resumed is what a watch state was last played as, if a user may still see
// and play it.
func (s *Service) resumed(state *core.Record) (ResumeItem, bool) {
	collection, id, known := state.GetString("item_collection"), state.GetString("item_id"), state.GetString("identity")
	if shows, ok := strings.CutSuffix(collection, "_episodes"); ok {
		if kind, ok := s.shelfOf(shows); !ok || kind != library.Shows {
			return ResumeItem{}, false
		}
		ep := s.recordOf(collection, id, known)
		if ep == nil || !ep.GetBool("available") {
			return ResumeItem{}, false
		}
		show, _, ok := s.titleRecord(shows, ep.GetString("show"))
		if !ok {
			return ResumeItem{}, false
		}
		episode, title := episodeOf(ep, show.Id), titleOf(show, library.Shows)
		episode.Watch = watchOf(state)
		return ResumeItem{Kind: "episode", Episode: &episode, Show: &title}, true
	}
	kind, ok := s.shelfOf(collection)
	if !ok {
		return ResumeItem{}, false
	}
	isExtra := strings.Contains(known, "/extra:")
	var r *core.Record
	if isExtra {
		r = s.recordOf(collection, id, "")
	} else {
		r = s.recordOf(collection, id, known)
	}
	if r == nil || r.GetString("match_state") == "uncertain" {
		return ResumeItem{}, false
	}
	if isExtra {
		// A series' extra plays as one of its episodes.
		if kind != library.Shows {
			return ResumeItem{}, false
		}
		extras, err := s.extrasOf(r)
		if err != nil {
			return ResumeItem{}, false
		}
		for _, x := range extras {
			if identity.Extra(r.GetString("identity"), x.Kind, x.Title, x.Path) == known {
				episode, title := extraEpisode(r, x), titleOf(r, kind)
				episode.Watch = watchOf(state)
				return ResumeItem{Kind: "episode", Episode: &episode, Show: &title}, true
			}
		}
		return ResumeItem{}, false
	}
	if kind != library.Movies || r.GetString("identity") != known || !r.GetBool("available") {
		return ResumeItem{}, false
	}
	title := titleOf(r, kind)
	return ResumeItem{Kind: "movie", Title: &title}, true
}

// recordOf is a record by its id — or, where it went and came back under
// another, by what it is.
func (s *Service) recordOf(collection, id, known string) *core.Record {
	if r, err := s.app.FindRecordById(collection, id); err == nil {
		return r
	}
	if known == "" {
		return nil
	}
	r, err := s.app.FindFirstRecordByFilter(collection, "identity = {:identity}", dbx.Params{"identity": known})
	if err != nil {
		return nil
	}
	return r
}
