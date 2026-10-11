// Package catalogue is the library as a user's Loge reads it: every library
// of a kind as one list, in exactly the order Loge merges its sources by
// (`compareItems`), a title's page, a series' seasons, episodes and extras,
// the genres, and what was begun — each with that user's own watch status.
// It writes nothing.
//
// What a user may not see is never here: a guess not yet confirmed, a title
// whose files are all gone, a library switched off.
package catalogue

import (
	"net/url"
	"slices"
	"strconv"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"foyer/internal/library"
)

// Service answers the catalogue's routes.
type Service struct {
	app core.App
}

func New(app core.App) *Service {
	return &Service{app: app}
}

// Routes mounts the catalogue, for users.
func (s *Service) Routes(r *router.Router[*core.RequestEvent]) {
	users := apis.RequireAuth("users")
	r.GET("/api/foyer/catalogue", s.list).Bind(users)
	r.GET("/api/foyer/catalogue/genres", s.genres).Bind(users)
	r.GET("/api/foyer/catalogue/resume", s.resume).Bind(users)
	r.GET("/api/foyer/catalogue/{collection}/{id}", s.detail).Bind(users)
	r.GET("/api/foyer/catalogue/{collection}/{id}/seasons", s.seasons).Bind(users)
	r.GET("/api/foyer/catalogue/{collection}/{id}/seasons/{season}", s.season).Bind(users)
}

// Served is what Foyer serves of its library, for the household's answer:
// the library, while one is switched on. Every device in the household makes
// a connection of it.
func Served(app core.App) []map[string]any {
	libs, err := app.FindAllRecords(library.LibrariesCollection)
	if err == nil && slices.ContainsFunc(libs, func(l *core.Record) bool { return l.GetBool("enabled") }) {
		return []map[string]any{{"key": "library", "type": "library", "name": "Library"}}
	}
	return []map[string]any{}
}

// shelves are the collections of the libraries switched on of a kind, in a
// steady order, whose titles carry the keys the catalogue sorts by.
func (s *Service) shelves(kind string) []string {
	libs, err := s.app.FindAllRecords(library.LibrariesCollection, dbx.HashExp{"kind": kind})
	if err != nil {
		return nil
	}
	var names []string
	for _, lib := range libs {
		if !lib.GetBool("enabled") {
			continue
		}
		// A library its template could not be brought up to has no keys yet.
		c, err := s.app.FindCollectionByNameOrId(lib.GetString("name"))
		if err != nil || c.Fields.GetByName("sort_title_key") == nil {
			continue
		}
		names = append(names, c.Name)
	}
	slices.Sort(names)
	return names
}

// shelfOf is the kind of the library switched on whose titles are in
// collection.
func (s *Service) shelfOf(collection string) (string, bool) {
	lib, err := s.app.FindFirstRecordByFilter(library.LibrariesCollection, "name = {:name}", dbx.Params{"name": collection})
	if err != nil || !lib.GetBool("enabled") {
		return "", false
	}
	return lib.GetString("kind"), true
}

// titleRecord is a film or a series a user may read, and its kind: in a
// library switched on, and not a guess. A title whose files are gone still
// has its page.
func (s *Service) titleRecord(collection, id string) (*core.Record, string, bool) {
	kind, ok := s.shelfOf(collection)
	if !ok {
		return nil, "", false
	}
	r, err := s.app.FindRecordById(collection, id)
	if err != nil || r.GetString("match_state") == "uncertain" {
		return nil, "", false
	}
	return r, kind, true
}

// kindOf reads a kind asked for: movies or shows.
func kindOf(q url.Values) (string, bool) {
	kind := q.Get("kind")
	return kind, kind == library.Movies || kind == library.Shows
}

// limitOf reads a page's size: fallback when unasked, a hundred at most.
func limitOf(q url.Values, fallback int) (int, bool) {
	raw := q.Get("limit")
	if raw == "" {
		return fallback, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, false
	}
	return min(n, 100), true
}
