package tmdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Candidate is one of TMDB's answers to a search a superuser typed: what
// they pick a title by.
type Candidate struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"originalTitle,omitempty"`
	Year          int    `json:"year,omitzero"`
	Overview      string `json:"overview,omitempty"`
	// Poster is TMDB's path for it, `/abc.jpg`: fetched through Foyer.
	Poster string `json:"poster,omitempty"`
}

// searchMost is how many answers a search shows: TMDB's first page.
const searchMost = 20

// Search is TMDB's own search, as a superuser types it, in the language
// asked for: the best first, a year narrowing it where one is given.
func (c *Client) Search(ctx context.Context, kind Kind, name string, year int, language string) ([]Candidate, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return []Candidate{}, nil
	}
	params := url.Values{"query": {name}, "include_adult": {"false"}}
	if language != "" {
		params.Set("language", language)
	}
	if year != 0 {
		if kind == Show {
			params.Set("first_air_date_year", strconv.Itoa(year))
		} else {
			params.Set("year", strconv.Itoa(year))
		}
	}
	var body struct {
		Results []map[string]any `json:"results"`
	}
	if err := c.get(ctx, "/search/"+kind.path(), params, &body); err != nil {
		return nil, err
	}
	fields := titleFields(kind)
	out := []Candidate{}
	for _, entry := range body.Results {
		id, ok := integer(entry["id"])
		if !ok {
			continue
		}
		cand := Candidate{ID: id, Title: text(entry[fields[0]]), Year: yearOf(entry[dateField(kind)]), Overview: text(entry["overview"]), Poster: text(entry["poster_path"])}
		if original := text(entry[fields[1]]); original != cand.Title {
			cand.OriginalTitle = original
		}
		if !validPoster.MatchString(cand.Poster) {
			cand.Poster = ""
		}
		out = append(out, cand)
		if len(out) == searchMost {
			break
		}
	}
	return out, nil
}

// PosterSizes are the sizes a poster is fetched at for a superuser to pick
// by: small ones, never TMDB's originals.
var PosterSizes = []string{"w92", "w154", "w185"}

// validPoster is a path TMDB names a picture by.
var validPoster = regexp.MustCompile(`^/[A-Za-z0-9_-]{4,64}\.(jpg|png)$`)

// ErrNoSuchPicture is a picture no search could name.
var ErrNoSuchPicture = errors.New("no picture of TMDB's is called so")

// Poster is one of TMDB's posters, in a small size, as its bytes: what the
// dashboard shows beside a candidate. Only a path TMDB names a picture by,
// at a size listed, ever reaches TMDB's image host.
func (c *Client) Poster(ctx context.Context, size, path string) ([]byte, string, error) {
	if !slices.Contains(PosterSizes, size) || !validPoster.MatchString(path) {
		return nil, "", ErrNoSuchPicture
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ImageURL(size, path), nil)
	if err != nil {
		return nil, "", err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, "", ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, "", ErrNoSuchPicture
	}
	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("TMDB's pictures answered %d", res.StatusCode)
	}
	kind := res.Header.Get("Content-Type")
	if !strings.HasPrefix(kind, "image/") {
		return nil, "", ErrNoSuchPicture
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	return b, kind, err
}
