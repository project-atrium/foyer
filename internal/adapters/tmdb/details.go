package tmdb

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Genre is TMDB's: its id is the same in every language, its name is not.
type Genre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Artwork is the paths of the images chosen for a title, empty where TMDB
// has none worth taking.
type Artwork struct {
	Poster   string
	Backdrop string
	Logo     string
}

// MovieInfo is what a library keeps of a film.
type MovieInfo struct {
	ID               int
	Title            string
	OriginalTitle    string
	OriginalLanguage string
	Overview         string
	Tagline          string
	ReleaseDate      string
	Year             int
	RuntimeMinutes   int
	Genres           []Genre
	Rating           float64
	ContentRating    string
	ExternalIDs      map[string]string
	Artwork          Artwork
}

// ShowInfo is what a library keeps of a series.
type ShowInfo struct {
	ID               int
	Name             string
	OriginalName     string
	OriginalLanguage string
	OriginCountry    []string
	Overview         string
	Tagline          string
	FirstAirDate     string
	LastAirDate      string
	Year             int
	Status           string
	Genres           []Genre
	Rating           float64
	ContentRating    string
	SeasonCount      int
	EpisodeCount     int
	Seasons          []SeasonSummary
	ExternalIDs      map[string]string
	Artwork          Artwork
}

// IsAnime is a series TMDB files under Animation, made in Japan.
func (s ShowInfo) IsAnime() bool {
	animated := slices.ContainsFunc(s.Genres, func(g Genre) bool { return g.ID == 16 })
	return animated && (s.OriginalLanguage == "ja" || slices.Contains(s.OriginCountry, "JP"))
}

type SeasonSummary struct {
	Number       int    `json:"number"`
	Name         string `json:"name"`
	AirDate      string `json:"air_date,omitempty"`
	EpisodeCount int    `json:"episode_count"`
	PosterPath   string `json:"poster_path,omitempty"`
}

// Season is a season with its episodes. Episode numbers are TMDB's own, and
// may run on from the season before: One Piece's season 21 is 892 to 1088.
type Season struct {
	Number   int
	Name     string
	Overview string
	AirDate  string
	Poster   string
	Episodes []EpisodeInfo
}

type EpisodeInfo struct {
	ID             int
	Season         int
	Number         int
	Name           string
	Overview       string
	AirDate        string
	RuntimeMinutes int
	Rating         float64
	Still          string
}

// The JSON as TMDB sends it.

type image struct {
	FilePath string  `json:"file_path"`
	Language *string `json:"iso_639_1"`
	Vote     float64 `json:"vote_average"`
}

type images struct {
	Posters   []image `json:"posters"`
	Backdrops []image `json:"backdrops"`
	Logos     []image `json:"logos"`
}

type movieJSON struct {
	ID               int              `json:"id"`
	Title            string           `json:"title"`
	OriginalTitle    string           `json:"original_title"`
	OriginalLanguage string           `json:"original_language"`
	Overview         string           `json:"overview"`
	Tagline          string           `json:"tagline"`
	ReleaseDate      string           `json:"release_date"`
	Runtime          int              `json:"runtime"`
	Genres           []Genre          `json:"genres"`
	VoteAverage      float64          `json:"vote_average"`
	PosterPath       string           `json:"poster_path"`
	BackdropPath     string           `json:"backdrop_path"`
	ExternalIDs      map[string]any   `json:"external_ids"`
	Images           images           `json:"images"`
	ReleaseDates     releaseDatesJSON `json:"release_dates"`
}

type releaseDatesJSON struct {
	Results []struct {
		Country string `json:"iso_3166_1"`
		Dates   []struct {
			Certification string `json:"certification"`
		} `json:"release_dates"`
	} `json:"results"`
}

type showJSON struct {
	ID               int            `json:"id"`
	Name             string         `json:"name"`
	OriginalName     string         `json:"original_name"`
	OriginalLanguage string         `json:"original_language"`
	OriginCountry    []string       `json:"origin_country"`
	Overview         string         `json:"overview"`
	Tagline          string         `json:"tagline"`
	FirstAirDate     string         `json:"first_air_date"`
	LastAirDate      string         `json:"last_air_date"`
	Status           string         `json:"status"`
	Genres           []Genre        `json:"genres"`
	VoteAverage      float64        `json:"vote_average"`
	NumberOfSeasons  int            `json:"number_of_seasons"`
	NumberOfEpisodes int            `json:"number_of_episodes"`
	PosterPath       string         `json:"poster_path"`
	BackdropPath     string         `json:"backdrop_path"`
	ExternalIDs      map[string]any `json:"external_ids"`
	Images           images         `json:"images"`
	ContentRatings   struct {
		Results []struct {
			Country string `json:"iso_3166_1"`
			Rating  string `json:"rating"`
		} `json:"results"`
	} `json:"content_ratings"`
	Seasons []struct {
		SeasonNumber int    `json:"season_number"`
		Name         string `json:"name"`
		AirDate      string `json:"air_date"`
		EpisodeCount int    `json:"episode_count"`
		PosterPath   string `json:"poster_path"`
	} `json:"seasons"`
}

type seasonJSON struct {
	SeasonNumber int    `json:"season_number"`
	Name         string `json:"name"`
	Overview     string `json:"overview"`
	AirDate      string `json:"air_date"`
	PosterPath   string `json:"poster_path"`
	Episodes     []struct {
		ID            int     `json:"id"`
		SeasonNumber  int     `json:"season_number"`
		EpisodeNumber int     `json:"episode_number"`
		Name          string  `json:"name"`
		Overview      string  `json:"overview"`
		AirDate       string  `json:"air_date"`
		Runtime       int     `json:"runtime"`
		VoteAverage   float64 `json:"vote_average"`
		StillPath     string  `json:"still_path"`
	} `json:"episodes"`
}

// language is the two letters of a language tag such as `de-DE`; region its
// country.
func languageOf(tag string) (language, region string) {
	language, region, _ = strings.Cut(tag, "-")
	return strings.ToLower(language), strings.ToUpper(region)
}

func imageLanguages(tag string) string {
	language, _ := languageOf(tag)
	if language == "" || language == "en" {
		return "en,null"
	}
	return language + ",en,null"
}

// Movie is a film's details in the language asked for, with its ids, its
// certification where the language names a country, and its artwork.
func (c *Client) Movie(ctx context.Context, id int, language string) (MovieInfo, error) {
	var m movieJSON
	params := url.Values{
		"append_to_response":     {"external_ids,images,release_dates"},
		"include_image_language": {imageLanguages(language)},
	}
	if language != "" {
		params.Set("language", language)
	}
	if err := c.get(ctx, "/movie/"+strconv.Itoa(id), params, &m); err != nil {
		return MovieInfo{}, err
	}
	_, region := languageOf(language)
	info := MovieInfo{
		ID:               m.ID,
		Title:            m.Title,
		OriginalTitle:    m.OriginalTitle,
		OriginalLanguage: m.OriginalLanguage,
		Overview:         m.Overview,
		Tagline:          m.Tagline,
		ReleaseDate:      m.ReleaseDate,
		Year:             yearOf(m.ReleaseDate),
		RuntimeMinutes:   m.Runtime,
		Genres:           m.Genres,
		Rating:           m.VoteAverage,
		ExternalIDs:      externalIDs(m.ExternalIDs, m.ID),
		Artwork:          choose(m.Images, language, m.PosterPath, m.BackdropPath),
	}
	for _, country := range []string{region, "US"} {
		for _, r := range m.ReleaseDates.Results {
			if r.Country != country || info.ContentRating != "" {
				continue
			}
			for _, d := range r.Dates {
				if d.Certification != "" {
					info.ContentRating = d.Certification
					break
				}
			}
		}
	}
	return info, nil
}

// Show is a series' details in the language asked for: its seasons as TMDB
// counts them, its ids, its content rating and its artwork. Episodes are
// Seasons'.
func (c *Client) Show(ctx context.Context, id int, language string) (ShowInfo, error) {
	var s showJSON
	params := url.Values{
		"append_to_response":     {"external_ids,images,content_ratings"},
		"include_image_language": {imageLanguages(language)},
	}
	if language != "" {
		params.Set("language", language)
	}
	if err := c.get(ctx, "/tv/"+strconv.Itoa(id), params, &s); err != nil {
		return ShowInfo{}, err
	}
	return readShow(s, language), nil
}

func readShow(s showJSON, language string) ShowInfo {
	_, region := languageOf(language)
	info := ShowInfo{
		ID:               s.ID,
		Name:             s.Name,
		OriginalName:     s.OriginalName,
		OriginalLanguage: s.OriginalLanguage,
		OriginCountry:    s.OriginCountry,
		Overview:         s.Overview,
		Tagline:          s.Tagline,
		FirstAirDate:     s.FirstAirDate,
		LastAirDate:      s.LastAirDate,
		Year:             yearOf(s.FirstAirDate),
		Status:           s.Status,
		Genres:           s.Genres,
		Rating:           s.VoteAverage,
		SeasonCount:      s.NumberOfSeasons,
		EpisodeCount:     s.NumberOfEpisodes,
		ExternalIDs:      externalIDs(s.ExternalIDs, s.ID),
		Artwork:          choose(s.Images, language, s.PosterPath, s.BackdropPath),
	}
	for _, season := range s.Seasons {
		info.Seasons = append(info.Seasons, SeasonSummary{
			Number: season.SeasonNumber, Name: season.Name, AirDate: season.AirDate,
			EpisodeCount: season.EpisodeCount, PosterPath: season.PosterPath,
		})
	}
	for _, country := range []string{region, "US"} {
		for _, r := range s.ContentRatings.Results {
			if r.Country == country && r.Rating != "" && info.ContentRating == "" {
				info.ContentRating = r.Rating
			}
		}
	}
	return info
}

// Seasons fetches the seasons asked for, with their episodes, twenty to a
// call: TMDB appends no more than twenty answers to one.
func (c *Client) Seasons(ctx context.Context, id int, language string, numbers []int) ([]Season, error) {
	var seasons []Season
	for start := 0; start < len(numbers); start += 20 {
		batch := numbers[start:min(start+20, len(numbers))]
		appended := make([]string, len(batch))
		for i, n := range batch {
			appended[i] = "season/" + strconv.Itoa(n)
		}
		params := url.Values{"append_to_response": {strings.Join(appended, ",")}}
		if language != "" {
			params.Set("language", language)
		}
		var body map[string]any
		if err := c.get(ctx, "/tv/"+strconv.Itoa(id), params, &body); err != nil {
			return nil, err
		}
		for _, n := range batch {
			raw, ok := body["season/"+strconv.Itoa(n)]
			if !ok {
				continue
			}
			var s seasonJSON
			if err := remarshal(raw, &s); err != nil {
				continue
			}
			season := Season{Number: s.SeasonNumber, Name: s.Name, Overview: s.Overview, AirDate: s.AirDate, Poster: s.PosterPath}
			for _, e := range s.Episodes {
				season.Episodes = append(season.Episodes, EpisodeInfo{
					ID: e.ID, Season: e.SeasonNumber, Number: e.EpisodeNumber, Name: e.Name, Overview: e.Overview,
					AirDate: e.AirDate, RuntimeMinutes: e.Runtime, Rating: e.VoteAverage, Still: e.StillPath,
				})
			}
			seasons = append(seasons, season)
		}
	}
	return seasons, nil
}

// EpisodeGroupRef is one of a series' other orderings of its episodes.
type EpisodeGroupRef struct {
	ID           string `json:"id"`
	Type         int    `json:"type"`
	Name         string `json:"name"`
	EpisodeCount int    `json:"episode_count"`
}

// EpisodeGroupAbsolute is TMDB's type for an absolute ordering.
const EpisodeGroupAbsolute = 2

// EpisodeGroups lists a series' other orderings.
func (c *Client) EpisodeGroups(ctx context.Context, id int) ([]EpisodeGroupRef, error) {
	var body struct {
		Results []EpisodeGroupRef `json:"results"`
	}
	if err := c.get(ctx, "/tv/"+strconv.Itoa(id)+"/episode_groups", nil, &body); err != nil {
		return nil, err
	}
	return body.Results, nil
}

// EpisodeRef is an episode by its season and number.
type EpisodeRef struct {
	Season int
	Number int
}

type groupEpisodeJSON struct {
	Order         int `json:"order"`
	SeasonNumber  int `json:"season_number"`
	EpisodeNumber int `json:"episode_number"`
}

type groupJSON struct {
	Order    int                `json:"order"`
	Episodes []groupEpisodeJSON `json:"episodes"`
}

// Ordered is an episode group flattened: the n-th episode of the ordering,
// counted from one, is Ordered[n-1].
func (c *Client) Ordered(ctx context.Context, group string) ([]EpisodeRef, error) {
	var body struct {
		Groups []groupJSON `json:"groups"`
	}
	if err := c.get(ctx, "/tv/episode_group/"+url.PathEscape(group), nil, &body); err != nil {
		return nil, err
	}
	slices.SortStableFunc(body.Groups, func(a, b groupJSON) int { return a.Order - b.Order })
	var ordered []EpisodeRef
	for _, g := range body.Groups {
		episodes := slices.Clone(g.Episodes)
		slices.SortStableFunc(episodes, func(a, b groupEpisodeJSON) int { return a.Order - b.Order })
		for _, e := range episodes {
			ordered = append(ordered, EpisodeRef{Season: e.SeasonNumber, Number: e.EpisodeNumber})
		}
	}
	return ordered, nil
}

// remarshal reads one appended answer, decoded loosely, into its own shape.
func remarshal(raw any, out any) error {
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// externalIDs keeps the catalogue ids a library and Loge key by.
func externalIDs(raw map[string]any, tmdb int) map[string]string {
	ids := map[string]string{"tmdb": strconv.Itoa(tmdb)}
	if s := text(raw["imdb_id"]); s != "" {
		ids["imdb"] = s
	}
	if n, ok := integer(raw["tvdb_id"]); ok && n > 0 {
		ids["tvdb"] = strconv.Itoa(n)
	}
	if s := text(raw["wikidata_id"]); s != "" {
		ids["wikidata"] = s
	}
	return ids
}

// choose picks a title's artwork: a poster and a logo in the language asked
// for, else English, else one with no words; a backdrop with no words first,
// since a title's own name is drawn over it. The best voted wins. Logos must
// be PNG — PocketBase makes no thumbnails of SVG. TMDB's own poster and
// backdrop stand in where the lists are empty.
func choose(set images, language, poster, backdrop string) Artwork {
	lang, _ := languageOf(language)
	art := Artwork{
		Poster:   best(set.Posters, []string{lang, "en", ""}, nil),
		Backdrop: best(set.Backdrops, []string{"", lang, "en"}, nil),
		Logo:     best(set.Logos, []string{lang, "en"}, func(i image) bool { return strings.HasSuffix(strings.ToLower(i.FilePath), ".png") }),
	}
	if art.Poster == "" {
		art.Poster = poster
	}
	if art.Backdrop == "" {
		art.Backdrop = backdrop
	}
	return art
}

func best(list []image, languages []string, keep func(image) bool) string {
	for _, want := range languages {
		pick, vote := "", -1.0
		for _, i := range list {
			got := ""
			if i.Language != nil {
				got = *i.Language
			}
			if got != want || (keep != nil && !keep(i)) || i.FilePath == "" {
				continue
			}
			if i.Vote > vote {
				pick, vote = i.FilePath, i.Vote
			}
		}
		if pick != "" {
			return pick
		}
	}
	return ""
}
