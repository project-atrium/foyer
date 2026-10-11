package catalogue

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"foyer/internal/library"
	"foyer/internal/titles"
	"foyer/internal/watch"
)

// sorts are what a page may be ordered by: compareItems' own.
var sorts = []string{"title", "releaseDate", "addedAt", "rating"}

// query is one page asked for.
type query struct {
	kind  string
	by    string
	desc  bool
	limit int
	after *cursor
	words []string
	genre string
}

// cursor is where a page ended: its last title's keys. The next page is
// whatever sorts after them, so a title added or gone meanwhile shifts
// nothing — never an offset.
type cursor struct {
	Value string `json:"v"`
	Title string `json:"t"`
	ID    string `json:"i"`
}

func (c cursor) String() string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func readCursor(text string) (*cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return nil, err
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if c.ID == "" {
		return nil, errors.New("a cursor names the title it ended on")
	}
	return &c, nil
}

// ref is a title on a page: where it is, and its keys.
type ref struct {
	Collection string `db:"c"`
	ID         string `db:"id"`
	// The id Loge keys it by, `movies.<id>`: the last tie-break, as in the app.
	Ext   string `db:"ext"`
	Title string `db:"tk"`
	Value string `db:"v"`
}

func (s *Service) list(re *core.RequestEvent) error {
	q := re.Request.URL.Query()
	kind, ok := kindOf(q)
	if !ok {
		return re.BadRequestError("A catalogue is of movies or shows.", nil)
	}
	by := q.Get("sort")
	if by == "" {
		by = "title"
	}
	if !slices.Contains(sorts, by) {
		return re.BadRequestError("A catalogue is sorted by title, releaseDate, addedAt or rating.", nil)
	}
	order := q.Get("order")
	if order != "" && order != "asc" && order != "desc" {
		return re.BadRequestError("An order is asc or desc.", nil)
	}
	limit, ok := limitOf(q, 50)
	if !ok {
		return re.BadRequestError("A limit is a whole number above 0.", nil)
	}
	page := query{kind: kind, by: by, desc: order == "desc", limit: limit, genre: titles.GenreKey(q.Get("genre"))}
	if text := q.Get("cursor"); text != "" {
		c, err := readCursor(text)
		if err != nil {
			return re.BadRequestError("That cursor is none of this catalogue's.", nil)
		}
		page.after = c
	}
	page.words = strings.Fields(titles.Fold(q.Get("term")))
	if len(page.words) > 8 {
		page.words = page.words[:8]
	}
	refs, next, err := s.page(page)
	if err != nil {
		return re.InternalServerError("", err)
	}
	items, err := s.titles(kind, refs)
	if err != nil {
		return re.InternalServerError("", err)
	}
	if err := s.annotate(re.Auth.Id, items); err != nil {
		return re.InternalServerError("", err)
	}
	answer := map[string]any{"items": items}
	if next != nil {
		answer["nextCursor"] = next.String()
	}
	return re.JSON(http.StatusOK, answer)
}

// page is one page of a kind's titles across its libraries, in compareItems'
// order — value, then the lowered title, then Loge's id; for a title sort,
// the title and the id — and the cursor after it while more follow. The keys
// are the titles' own (library/keys.go): text SQLite orders as the app does.
func (s *Service) page(q query) ([]ref, *cursor, error) {
	names := s.shelves(q.kind)
	if len(names) == 0 {
		return nil, nil, nil
	}
	params := dbx.Params{}
	filters := []string{"[[match_state]] != 'uncertain'", "[[available]] = TRUE"}
	for i, word := range q.words {
		key := "w" + strconv.Itoa(i)
		params[key] = word
		filters = append(filters, "instr([[search_text]], {:"+key+"}) > 0")
	}
	if q.genre != "" {
		params["genre"] = q.genre
		filters = append(filters, "EXISTS (SELECT 1 FROM json_each([[genre_keys]]) WHERE json_each.value = {:genre})")
	}
	value := "''"
	switch q.by {
	case "releaseDate":
		value = "[[sort_release]]"
	case "rating":
		value = "[[sort_rating]]"
	case "addedAt":
		// A series came when its newest episode did.
		value = "[[added]]"
		if q.kind == library.Shows {
			value = "[[last_added]]"
		}
	}
	parts := make([]string, len(names))
	for i, name := range names {
		key := "c" + strconv.Itoa(i)
		params[key] = name
		parts[i] = fmt.Sprintf("SELECT {:%s} AS c, [[id]] AS id, {:%s} || '.' || [[id]] AS ext, [[sort_title_key]] AS tk, %s AS v FROM {{%s}} WHERE %s",
			key, key, value, name, strings.Join(filters, " AND "))
	}
	direction, beyond := "ASC", ">"
	if q.desc {
		direction, beyond = "DESC", "<"
	}
	sql := "SELECT c, id, ext, tk, v FROM (" + strings.Join(parts, " UNION ALL ") + ")"
	order := "tk " + direction + ", ext ASC"
	if q.by != "title" {
		order = "v " + direction + ", tk ASC, ext ASC"
	}
	if c := q.after; c != nil {
		params["cv"], params["ct"], params["ci"] = c.Value, c.Title, c.ID
		if q.by == "title" {
			sql += " WHERE tk " + beyond + " {:ct} OR (tk = {:ct} AND ext > {:ci})"
		} else {
			sql += " WHERE v " + beyond + " {:cv} OR (v = {:cv} AND (tk > {:ct} OR (tk = {:ct} AND ext > {:ci})))"
		}
	}
	params["limit"] = q.limit + 1
	var refs []ref
	if err := s.app.DB().NewQuery(sql + " ORDER BY " + order + " LIMIT {:limit}").Bind(params).All(&refs); err != nil {
		return nil, nil, err
	}
	if len(refs) <= q.limit {
		return refs, nil, nil
	}
	refs = refs[:q.limit]
	last := refs[len(refs)-1]
	return refs, &cursor{Value: last.Value, Title: last.Title, ID: last.Ext}, nil
}

// titles are the records of a page's refs, in its order. One gone between
// the two reads is left out.
func (s *Service) titles(kind string, refs []ref) ([]Title, error) {
	byCollection := map[string][]string{}
	for _, r := range refs {
		byCollection[r.Collection] = append(byCollection[r.Collection], r.ID)
	}
	records := map[string]*core.Record{}
	for collection, ids := range byCollection {
		found, err := s.app.FindRecordsByIds(collection, ids)
		if err != nil {
			return nil, err
		}
		for _, r := range found {
			records[collection+"."+r.Id] = r
		}
	}
	items := make([]Title, 0, len(refs))
	for _, r := range refs {
		if record, ok := records[r.Ext]; ok {
			items = append(items, titleOf(record, kind))
		}
	}
	return items, nil
}

// annotate lays one user's watch status on titles — a film's own, a
// series' from its episodes — and what each series holds.
func (s *Service) annotate(user string, items []Title) error {
	var identities []string
	shows := map[string][]string{}
	for _, t := range items {
		if t.Kind == "movie" {
			identities = append(identities, t.Identity)
		} else {
			shows[t.Collection] = append(shows[t.Collection], t.ID)
		}
	}
	states, err := s.states(user, identities)
	if err != nil {
		return err
	}
	holdings := map[string]held{}
	for collection, ids := range shows {
		rows, err := s.held(user, collection, ids)
		if err != nil {
			return err
		}
		for _, h := range rows {
			holdings[collection+"/"+h.Show] = h
		}
	}
	for i := range items {
		t := &items[i]
		if t.Kind == "movie" {
			if state, ok := states[t.Identity]; ok && t.Identity != "" {
				t.Watch = watchOf(state)
			}
			continue
		}
		h := holdings[t.Collection+"/"+t.ID]
		t.SeasonCount, t.EpisodeCount = h.Seasons, h.Episodes
		t.Watch = h.watch()
	}
	return nil
}

// states are a user's watch states for identities, by identity.
func (s *Service) states(user string, identities []string) (map[string]*core.Record, error) {
	out := map[string]*core.Record{}
	wanted := make([]any, 0, len(identities))
	for _, id := range identities {
		if id != "" {
			wanted = append(wanted, id)
		}
	}
	if len(wanted) == 0 {
		return out, nil
	}
	records, err := s.app.FindAllRecords(watch.Collection, dbx.HashExp{"user": user, "identity": wanted})
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		out[r.GetString("identity")] = r
	}
	return out, nil
}

// held is what a series holds — its episodes and seasons still there — and
// how much of it one user watched.
type held struct {
	Show     string `db:"show_id"`
	Episodes int    `db:"episodes"`
	Seasons  int    `db:"seasons"`
	Watched  int    `db:"watched"`
	States   int    `db:"states"`
	Last     string `db:"last_played"`
}

// watch is a series' watch status for its user, where they watched any of
// it: watched when every episode is, and the latest playing of any.
func (h held) watch() *Watch {
	if h.States == 0 {
		return nil
	}
	unwatched := h.Episodes - h.Watched
	w := &Watch{Watched: h.Episodes > 0 && unwatched == 0, Unwatched: &unwatched}
	if last, err := types.ParseDateTime(h.Last); err == nil {
		w.LastPlayedAt = instant(last)
	}
	return w
}

func (s *Service) held(user, collection string, shows []string) ([]held, error) {
	params := dbx.Params{"user": user}
	marks := make([]string, len(shows))
	for i, id := range shows {
		key := "s" + strconv.Itoa(i)
		params[key] = id
		marks[i] = "{:" + key + "}"
	}
	var rows []held
	err := s.app.DB().NewQuery(
		"SELECT e.[[show]] AS show_id, COUNT(*) AS episodes, " +
			"COUNT(DISTINCT CASE WHEN e.[[season]] > 0 THEN e.[[season]] END) AS seasons, " +
			"COALESCE(SUM(CASE WHEN w.[[watched]] THEN 1 ELSE 0 END), 0) AS watched, COUNT(w.[[id]]) AS states, " +
			"COALESCE(MAX(w.[[last_played_at]]), '') AS last_played " +
			"FROM {{" + library.Episodes(collection) + "}} e LEFT JOIN {{" + watch.Collection + "}} w " +
			"ON w.[[user]] = {:user} AND w.[[identity]] = e.[[identity]] AND e.[[identity]] != '' " +
			"WHERE e.[[available]] = TRUE AND e.[[show]] IN (" + strings.Join(marks, ", ") + ") GROUP BY e.[[show]]").
		Bind(params).All(&rows)
	return rows, err
}
