package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/pocketbase/dbx"
	validation "github.com/pocketbase/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"foyer/internal/adapters/tmdb"
)

// GridItem is one poster on the dashboard's Library page.
type GridItem struct {
	CollectionID   string   `db:"collection_id" json:"collectionId"`
	CollectionName string   `db:"collection_name" json:"collectionName"`
	Library        string   `db:"library" json:"library"`
	Kind           string   `db:"kind" json:"kind"`
	ID             string   `db:"id" json:"id"`
	Title          string   `db:"title" json:"title"`
	Year           int      `db:"year" json:"year"`
	Poster         string   `db:"poster" json:"poster"`
	MatchState     string   `db:"match_state" json:"matchState"`
	LastAdded      string   `db:"last_added" json:"lastAdded"`
	VersionCount   int      `db:"version_count" json:"versionCount"`
	Quality        string   `db:"quality" json:"-"`
	Qualities      []string `db:"-" json:"quality"`
}

// gridRoutes are the dashboard's Library page: the scans, the posters, a
// title, and a superuser's corrections.
func (e *Engine) gridRoutes(admin *router.RouterGroup[*core.RequestEvent]) {
	admin.GET("/status", func(re *core.RequestEvent) error {
		return re.JSON(http.StatusOK, map[string]any{"scans": e.status.snapshot()})
	})

	admin.POST("/scan", func(re *core.RequestEvent) error {
		var body struct {
			Library string `json:"library"`
			Full    bool   `json:"full"`
		}
		if err := re.BindBody(&body); err != nil {
			return re.BadRequestError("The request could not be read.", err)
		}
		e.Trigger(body.Library, body.Full)
		return re.NoContent(http.StatusAccepted)
	})

	admin.GET("/library", func(re *core.RequestEvent) error {
		q := re.Request.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		perPage, _ := strconv.Atoi(q.Get("perPage"))
		page, perPage = max(page, 1), min(max(perPage, 1), 100)
		if q.Get("perPage") == "" {
			perPage = 48
		}
		result, err := e.Grid(q.Get("library"), q.Get("state"), q.Get("q"), page, perPage)
		if err != nil {
			return re.BadRequestError("The library could not be read.", err)
		}
		return re.JSON(http.StatusOK, result)
	})

	admin.GET("/library/{collection}/{id}", func(re *core.RequestEvent) error {
		detail, err := e.Detail(re.Request.PathValue("collection"), re.Request.PathValue("id"))
		if err != nil {
			return re.NotFoundError("", nil)
		}
		return re.JSON(http.StatusOK, detail)
	})

	admin.POST("/library/{collection}/{id}/confirm", func(re *core.RequestEvent) error {
		err := e.Correct(re.Request.Context(), re.Request.PathValue("collection"), re.Request.PathValue("id"), 0)
		return answerCorrection(re, err)
	})

	admin.POST("/library/{collection}/{id}/match", func(re *core.RequestEvent) error {
		var body struct {
			TMDBID int `json:"tmdbId"`
		}
		if err := re.BindBody(&body); err != nil || body.TMDBID <= 0 {
			return re.BadRequestError("A TMDB id is a whole number.", validation.Errors{"tmdbId": validation.NewError("validation_required", "A TMDB id is a whole number.")})
		}
		err := e.Correct(re.Request.Context(), re.Request.PathValue("collection"), re.Request.PathValue("id"), body.TMDBID)
		return answerCorrection(re, err)
	})
}

func answerCorrection(re *core.RequestEvent, err error) error {
	switch {
	case err == nil:
		return re.NoContent(http.StatusNoContent)
	case errors.Is(err, errNoTitle):
		return re.NotFoundError("", nil)
	case errors.Is(err, errTMDBDown), errors.Is(err, tmdb.ErrRefused), errors.Is(err, tmdb.ErrNoKey):
		return re.Error(http.StatusBadGateway, err.Error(), nil)
	default:
		return re.BadRequestError(err.Error(), nil)
	}
}

// GridPage is a page of posters, and how many there are of each state.
type GridPage struct {
	Page       int            `json:"page"`
	PerPage    int            `json:"perPage"`
	TotalItems int            `json:"totalItems"`
	Counts     map[string]int `json:"counts"`
	Items      []GridItem     `json:"items"`
}

// Grid is every library's titles as one list, the newest added first: one
// query over all their collections, filtered by library, state and title.
func (e *Engine) Grid(library, state, term string, page, perPage int) (GridPage, error) {
	libs, err := e.app.FindAllRecords(LibrariesCollection)
	if err != nil {
		return GridPage{}, err
	}
	params := dbx.Params{}
	var parts []string
	for i, lib := range libs {
		if library != "" && lib.Id != library {
			continue
		}
		name := lib.GetString("name")
		c, err := e.app.FindCollectionByNameOrId(name)
		if err != nil {
			continue
		}
		p := "c" + strconv.Itoa(i)
		params[p+"id"], params[p+"name"], params[p+"lib"], params[p+"kind"] = c.Id, name, lib.Id, lib.GetString("kind")
		parts = append(parts, fmt.Sprintf(
			"SELECT {:%sid} AS collection_id, {:%sname} AS collection_name, {:%slib} AS library, {:%skind} AS kind, "+
				"[[id]], [[title]], [[year]], [[poster]], [[match_state]], [[last_added]], [[version_count]], [[quality]] FROM {{%s}}",
			p, p, p, p, name))
	}
	result := GridPage{Page: page, PerPage: perPage, Counts: map[string]int{}, Items: []GridItem{}}
	if len(parts) == 0 {
		return result, nil
	}
	union := "(" + strings.Join(parts, " UNION ALL ") + ")"
	where := []string{"1=1"}
	if term != "" {
		where = append(where, "[[title]] LIKE {:term} ESCAPE '\\'")
		params["term"] = "%" + escapeLike(term) + "%"
	}
	var counts []struct {
		State string `db:"match_state"`
		N     int    `db:"n"`
	}
	if err := e.app.DB().NewQuery("SELECT [[match_state]], COUNT(*) AS n FROM " + union + " WHERE " + strings.Join(where, " AND ") + " GROUP BY [[match_state]]").
		Bind(params).All(&counts); err != nil {
		return result, err
	}
	for _, c := range counts {
		result.Counts[c.State] = c.N
		result.Counts["all"] += c.N
		if state == "" || state == c.State {
			result.TotalItems += c.N
		}
	}
	if state != "" {
		where = append(where, "[[match_state]] = {:state}")
		params["state"] = state
	}
	params["limit"], params["offset"] = perPage, (page-1)*perPage
	err = e.app.DB().NewQuery("SELECT * FROM " + union + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY [[last_added]] DESC, [[title]] ASC LIMIT {:limit} OFFSET {:offset}").Bind(params).All(&result.Items)
	for i := range result.Items {
		var qualities []string
		_ = jsonUnmarshal(result.Items[i].Quality, &qualities)
		result.Items[i].Qualities = qualities
	}
	return result, err
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

var errNoTitle = errors.New("no such title")

// titleOf finds a title by its collection and id, with its library.
func (e *Engine) titleOf(collection, id string) (*core.Record, *core.Record, error) {
	lib, err := owningLibrary(e.app, collection)
	if err != nil || lib == nil || collection != lib.GetString("name") {
		return nil, nil, errNoTitle
	}
	item, err := e.app.FindRecordById(collection, id)
	if err != nil {
		return nil, nil, errNoTitle
	}
	return item, lib, nil
}

// Detail is a title as the dashboard's panel shows it: the record, its
// files, what TMDB was asked and answered, and a series' episodes by season.
func (e *Engine) Detail(collection, id string) (map[string]any, error) {
	item, lib, err := e.titleOf(collection, id)
	if err != nil {
		return nil, err
	}
	files, err := e.app.FindAllRecords(FilesCollection, dbx.HashExp{"item_collection": collection, "item_id": id})
	if err != nil {
		return nil, err
	}
	var fileViews []map[string]any
	add := func(f *core.Record, episode string) {
		fileViews = append(fileViews, map[string]any{
			"id": f.Id, "path": f.GetString("path"), "size": f.GetInt64("size"), "role": f.GetString("role"),
			"label": f.GetString("label"), "probeState": f.GetString("probe_state"), "missing": f.GetString("missing_since") != "",
			"added": f.GetString("added"), "episode": episode,
		})
	}
	for _, f := range files {
		add(f, "")
	}
	seasons := map[int]int{}
	if lib.GetString("kind") == Shows {
		episodes, err := e.app.FindAllRecords(Episodes(collection), dbx.HashExp{"show": id})
		if err != nil {
			return nil, err
		}
		for _, ep := range episodes {
			seasons[ep.GetInt("season")]++
			epFiles, err := e.app.FindAllRecords(FilesCollection, dbx.HashExp{"item_collection": Episodes(collection), "item_id": ep.Id})
			if err != nil {
				return nil, err
			}
			for _, f := range epFiles {
				add(f, fmt.Sprintf("S%02dE%02d", ep.GetInt("season"), ep.GetInt("episode")))
			}
		}
	}
	var identity map[string]any
	if key := item.GetString("match_key"); key != "" {
		if r, err := e.app.FindFirstRecordByFilter(IdentitiesCollection, "library = {:library} && key = {:key}",
			dbx.Params{"library": lib.Id, "key": key}); err == nil {
			var candidates any
			_ = r.UnmarshalJSONField("candidates", &candidates)
			identity = map[string]any{"state": r.GetString("state"), "reason": r.GetString("reason"), "tmdbId": r.GetInt("tmdb_id"), "candidates": candidates}
		}
	}
	return map[string]any{
		"item": item, "library": viewOf(lib), "files": fileViews, "seasons": seasons, "identity": identity,
	}, nil
}

// Correct is a superuser's word on a title: confirm TMDB's guess (tmdbID
// 0), or name the TMDB title it is. Every key its files were read by is
// decided so for good, and its files are placed again: into the title that
// already holds that TMDB id where one does, merging the two.
func (e *Engine) Correct(ctx context.Context, collection, id string, tmdbID int) error {
	item, lib, err := e.titleOf(collection, id)
	if err != nil {
		return err
	}
	if tmdbID == 0 {
		if item.GetString("match_state") != "uncertain" || item.GetInt("tmdb_id") == 0 {
			return errors.New("only a guess can be confirmed")
		}
		tmdbID = item.GetInt("tmdb_id")
	}
	client, language := e.tmdb()
	if client == nil {
		return tmdb.ErrNoKey
	}
	if lib.GetString("kind") == Movies {
		_, err = client.Movie(ctx, tmdbID, language)
	} else {
		_, err = client.Show(ctx, tmdbID, language)
	}
	if errors.Is(err, tmdb.ErrNotFound) {
		return fmt.Errorf("TMDB has no %s with the id %d", strings.TrimSuffix(lib.GetString("kind"), "s"), tmdbID)
	}
	if err != nil {
		return err
	}

	// Every file the title holds — its own, and a series' episodes'.
	files, err := filesOf(e.app, collection, id)
	if err != nil {
		return err
	}
	var episodes []*core.Record
	if lib.GetString("kind") == Shows {
		if episodes, err = e.app.FindAllRecords(Episodes(collection), dbx.HashExp{"show": id}); err != nil {
			return err
		}
		for _, ep := range episodes {
			epFiles, err := filesOf(e.app, Episodes(collection), ep.Id)
			if err != nil {
				return err
			}
			files = append(files, epFiles...)
		}
	}
	d := decision{id: tmdbID, state: manual, reason: "a superuser"}
	torrents := map[string]string{}
	var waiting []*placedFile
	for _, f := range files {
		t, ok := torrents[f.GetString("torrent")]
		if !ok {
			if r, err := e.app.FindRecordById(TorrentsCollection, f.GetString("torrent")); err == nil {
				t = r.GetString("name")
			}
			torrents[f.GetString("torrent")] = t
		}
		pf := &placedFile{record: f, torrent: t}
		_ = f.UnmarshalJSONField("parsed", &pf.placed)
		waiting = append(waiting, pf)
	}
	candidates := plan(lib.GetString("kind"), waiting)
	for _, c := range candidates {
		if err := e.remember(lib, c, d, nil); err != nil {
			return err
		}
	}
	// Its files placed again under the decision: into the title that holds
	// the TMDB id already, if one does, else into this one, by its key.
	lock := e.writes(lib.Id)
	lock.Lock()
	err = e.app.RunInTransaction(func(tx core.App) error {
		for _, f := range files {
			f.Set("item_id", "")
			f.Set("role", "pending")
			if err := tx.Save(f); err != nil {
				return err
			}
		}
		return nil
	})
	lock.Unlock()
	if err != nil {
		return err
	}
	r := e.status.begin(lib.Id, lib.GetString("name"), false)
	err = e.placeAll(ctx, lib, candidates, r, &d)
	r.finish(err)
	if err != nil {
		return err
	}
	// What the correction left with nothing goes.
	lock.Lock()
	err = e.app.RunInTransaction(func(tx core.App) error {
		affected := map[string]map[string]bool{collection: {id: true}}
		for _, ep := range episodes {
			if affected[Episodes(collection)] == nil {
				affected[Episodes(collection)] = map[string]bool{}
			}
			affected[Episodes(collection)][ep.Id] = true
		}
		return tidy(tx, lib, affected)
	})
	lock.Unlock()
	if err != nil {
		return err
	}
	e.fetchArtwork(ctx, lib, r)
	return nil
}

func jsonUnmarshal(raw string, out any) error {
	if raw == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), out)
}
