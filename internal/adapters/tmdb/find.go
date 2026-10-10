package tmdb

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"

	"foyer/internal/titles"
)

// Kind is what is being identified: a film or a series.
type Kind string

const (
	Movie Kind = "movie"
	Show  Kind = "show"
)

func (k Kind) path() string {
	if k == Show {
		return "tv"
	}
	return "movie"
}

// Query is a title as a release names it.
type Query struct {
	Kind          Kind
	Title         string
	OriginalTitle string
	// Year is 0 when the name gave none.
	Year int
}

// Found is one search result: its id, every name it answered with, and its
// year, 0 when TMDB gave none.
type Found struct {
	ID    int
	Names []string
	Year  int
}

// FindResult is what a search settled. ID is set when Loge's matcher would
// answer it. Named is set instead when several results share the name asked
// for and no year tells them apart: Loge's matcher answers nothing then, and
// the library decides with what it knows of the files.
type FindResult struct {
	ID    int
	Named []Found
}

// askNamesOf is how many of the results in the year are asked for their
// names in other languages, when none is called by the one asked for. TMDB
// ranks the best first; past the second, a name in another language is
// rarely the film.
const askNamesOf = 2

// Find is Loge's `identify`, ported line for line
// (adapters/metadata/tmdb/src/provider.ts): the title in its own language
// first, a year either side of the one given, and a name in another language
// only once the film's own translations or alternative titles hold it.
func (c *Client) Find(ctx context.Context, q Query) (FindResult, error) {
	type name struct{ key, text string }
	var names []name
	seen := map[string]bool{}
	for _, n := range []string{q.OriginalTitle, q.Title} {
		key := titles.Key(n)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		names = append(names, name{key, strings.TrimSpace(n)})
	}
	var named []Found
	for _, n := range names {
		result, several, err := c.find(ctx, q, n.text, seen)
		if err != nil {
			return FindResult{}, err
		}
		if result != nil {
			return FindResult{ID: result.ID}, nil
		}
		if named == nil && len(several) > 0 {
			named = several
		}
	}
	return FindResult{Named: named}, nil
}

// find is the one result `name` names, or the several that share it with no
// year to tell them apart.
func (c *Client) find(ctx context.Context, q Query, name string, keys map[string]bool) (*Found, []Found, error) {
	found, err := c.search(ctx, q, name, true)
	if err != nil {
		return nil, nil, err
	}
	found = within(found, q.Year)
	if len(found) == 0 && q.Year != 0 {
		if found, err = c.search(ctx, q, name, false); err != nil {
			return nil, nil, err
		}
		found = within(found, q.Year)
	}
	var named []Found
	for _, result := range found {
		for _, candidate := range result.Names {
			if keys[titles.Key(candidate)] {
				named = append(named, result)
				break
			}
		}
	}
	if len(named) == 0 {
		wanted := looseKeys(name)
		for _, result := range found {
			if slices.ContainsFunc(result.Names, func(candidate string) bool {
				return slices.ContainsFunc(looseKeys(candidate), func(k string) bool { return slices.Contains(wanted, k) })
			}) {
				named = append(named, result)
			}
		}
	}
	if len(named) == 1 || (len(named) > 1 && q.Year != 0) {
		return &named[0], nil, nil
	}
	if len(named) > 1 {
		return nil, named, nil
	}
	wanted := titles.Key(name)
	for _, result := range found[:min(len(found), askNamesOf)] {
		names, err := c.namesOf(ctx, q.Kind, result.ID)
		if err != nil {
			return nil, nil, err
		}
		if names[wanted] {
			return &result, nil, nil
		}
	}
	return nil, nil, nil
}

// Foyer's own, past Loge's matcher: a release's name is typed by hand, and
// drops what TMDB's keeps — an apostrophe ("Dont", "Dead Mans Chest"), a
// possessive ("Fred Road Trip" for "Fred's"), an article ("Accountant 2"),
// a superscript ("The Accountant²"), the space between. Compared only where
// the exact names find nothing, and kept to the year as they are.
var (
	apostrophes = strings.NewReplacer("'", "", "’", "", "‘", "", "`", "", "´", "")
	possessive  = regexp.MustCompile(`(?i)(\pL)['’]s\b`)
)

// looseKeys is what a name keys to with those forgiven: its apostrophes
// dropped, or a possessive with them.
func looseKeys(name string) []string {
	name = strings.ReplaceAll(norm.NFKC.String(name), "&", " and ")
	var keys []string
	for _, v := range []string{name, possessive.ReplaceAllString(name, "$1")} {
		key := titles.Key(apostrophes.Replace(v))
		for _, article := range []string{"the ", "a ", "an "} {
			if rest, ok := strings.CutPrefix(key, article); ok {
				key = rest
				break
			}
		}
		key = strings.ReplaceAll(key, " ", "")
		if key != "" && !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	return keys
}

// within keeps the results a year either side of year — a film comes out a
// year apart in two countries — and every one where the year is not known.
func within(results []Found, year int) []Found {
	if year == 0 {
		return results
	}
	var kept []Found
	for _, r := range results {
		if r.Year != 0 && abs(r.Year-year) <= 1 {
			kept = append(kept, r)
		}
	}
	return kept
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (c *Client) search(ctx context.Context, q Query, name string, withYear bool) ([]Found, error) {
	params := url.Values{"query": {name}, "include_adult": {"false"}}
	if withYear && q.Year != 0 {
		// A film's `year` is any of its releases', a series' only its first air date's.
		if q.Kind == Show {
			params.Set("first_air_date_year", strconv.Itoa(q.Year))
		} else {
			params.Set("year", strconv.Itoa(q.Year))
		}
	}
	var body struct {
		Results []map[string]any `json:"results"`
	}
	if err := c.get(ctx, "/search/"+q.Kind.path(), params, &body); err != nil {
		return nil, err
	}
	return readSearch(q.Kind, body.Results), nil
}

// namesOf is every name a title has, keyed: its own, its translations' and
// its alternative titles'. A title gone since the search has none.
func (c *Client) namesOf(ctx context.Context, kind Kind, id int) (map[string]bool, error) {
	var body map[string]any
	err := c.get(ctx, "/"+kind.path()+"/"+strconv.Itoa(id), url.Values{"append_to_response": {"translations,alternative_titles"}}, &body)
	if errors.Is(err, ErrNotFound) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for _, n := range readNames(kind, body) {
		if key := titles.Key(n); key != "" {
			keys[key] = true
		}
	}
	return keys, nil
}

// TMDB's answers are read defensively: what comes back is whatever answered
// at the address, so nothing is taken on trust.

func titleFields(kind Kind) [2]string {
	if kind == Show {
		return [2]string{"name", "original_name"}
	}
	return [2]string{"title", "original_title"}
}

func dateField(kind Kind) string {
	if kind == Show {
		return "first_air_date"
	}
	return "release_date"
}

func readSearch(kind Kind, results []map[string]any) []Found {
	var found []Found
	for _, entry := range results {
		id, ok := integer(entry["id"])
		if !ok {
			continue
		}
		var names []string
		for _, field := range titleFields(kind) {
			if s := text(entry[field]); s != "" {
				names = append(names, s)
			}
		}
		found = append(found, Found{ID: id, Names: names, Year: yearOf(entry[dateField(kind)])})
	}
	return found
}

func readNames(kind Kind, body map[string]any) []string {
	var names []string
	fields := titleFields(kind)
	for _, field := range fields {
		if s := text(body[field]); s != "" {
			names = append(names, s)
		}
	}
	if translations, ok := body["translations"].(map[string]any); ok {
		list, _ := translations["translations"].([]any)
		for _, raw := range list {
			translation, _ := raw.(map[string]any)
			data, _ := translation["data"].(map[string]any)
			if s := text(data[fields[0]]); s != "" {
				names = append(names, s)
			}
		}
	}
	// A film's alternative titles are `titles`; a series' are `results`.
	if alternatives, ok := body["alternative_titles"].(map[string]any); ok {
		list, has := alternatives["titles"].([]any)
		if !has {
			list, _ = alternatives["results"].([]any)
		}
		for _, raw := range list {
			alternative, _ := raw.(map[string]any)
			if s := text(alternative["title"]); s != "" {
				names = append(names, s)
			}
		}
	}
	return names
}

func integer(v any) (int, bool) {
	n, ok := v.(float64)
	if !ok || n != float64(int(n)) {
		return 0, false
	}
	return int(n), true
}

func text(v any) string {
	s, _ := v.(string)
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return s
}

var leadingYear = regexp.MustCompile(`^(\d{4})-`)

func yearOf(v any) int {
	s, _ := v.(string)
	m := leadingYear.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	year, _ := strconv.Atoi(m[1])
	return year
}
