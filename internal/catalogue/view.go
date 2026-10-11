package catalogue

import (
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"foyer/internal/library"
	"foyer/internal/media"
	"foyer/internal/titles"
)

// What the catalogue answers, as `@loge/api`'s foyer.ts reads it: camelCase,
// and a field the title lacks absent rather than empty — an empty sort title
// would sort as it is.

// Watch is one user's watch status of a title.
type Watch struct {
	Watched      bool   `json:"watched"`
	PositionMs   int64  `json:"positionMs"`
	DurationMs   int64  `json:"durationMs"`
	LastPlayedAt string `json:"lastPlayedAt,omitempty"`
	// A series: how many of the episodes it holds are not watched.
	Unwatched *int `json:"unwatched,omitzero"`
}

// Images are the pictures on a record, by file name.
type Images struct {
	Poster   string `json:"poster,omitempty"`
	Backdrop string `json:"backdrop,omitempty"`
	Logo     string `json:"logo,omitempty"`
	Still    string `json:"still,omitempty"`
}

// Title is a film or a series.
type Title struct {
	Collection    string            `json:"collection"`
	ID            string            `json:"id"`
	Kind          string            `json:"kind"`
	Title         string            `json:"title"`
	SortTitle     string            `json:"sortTitle,omitempty"`
	OriginalTitle string            `json:"originalTitle,omitempty"`
	Year          int               `json:"year,omitzero"`
	ReleaseDate   string            `json:"releaseDate,omitempty"`
	AddedAt       string            `json:"addedAt,omitempty"`
	Rating        float64           `json:"rating,omitzero"`
	Overview      string            `json:"overview,omitempty"`
	Genres        []string          `json:"genres"`
	RuntimeMs     int64             `json:"runtimeMs,omitzero"`
	ContentRating string            `json:"contentRating,omitempty"`
	Identity      string            `json:"identity,omitempty"`
	ExternalIDs   map[string]string `json:"externalIds"`
	Images        Images            `json:"images"`
	Watch         *Watch            `json:"watch,omitzero"`
	Status        string            `json:"status,omitempty"`
	SeasonCount   int               `json:"seasonCount,omitzero"`
	EpisodeCount  int               `json:"episodeCount,omitzero"`
	EndYear       int               `json:"endYear,omitzero"`
}

// Episode is an episode — or one of a series' extras, played as one.
type Episode struct {
	Collection string  `json:"collection"`
	ID         string  `json:"id"`
	Show       string  `json:"show"`
	Season     *int    `json:"season,omitzero"`
	Episode    *int    `json:"episode,omitzero"`
	EpisodeEnd int     `json:"episodeEnd,omitzero"`
	Title      string  `json:"title"`
	Overview   string  `json:"overview,omitempty"`
	AirDate    string  `json:"airDate,omitempty"`
	AddedAt    string  `json:"addedAt,omitempty"`
	RuntimeMs  int64   `json:"runtimeMs,omitzero"`
	Rating     float64 `json:"rating,omitzero"`
	Identity   string  `json:"identity,omitempty"`
	Images     Images  `json:"images"`
	Watch      *Watch  `json:"watch,omitzero"`
	// An extra: the file it is.
	File string `json:"file,omitempty"`
}

// Season is a series' season, or its extras (key `extras`).
type Season struct {
	Key          string `json:"key"`
	Number       *int   `json:"number,omitzero"`
	Name         string `json:"name"`
	AirDate      string `json:"airDate,omitempty"`
	EpisodeCount int    `json:"episodeCount"`
	PosterPath   string `json:"posterPath,omitempty"`
	Unwatched    *int   `json:"unwatched,omitzero"`
}

// version is a file as a user reads it: what it is, never where on the share
// it lies — the field shadows the path it embeds.
type version struct {
	media.Version
	Path string `json:"path,omitempty"`
}

// extraView is an extra as a title's page lists it.
type extraView struct {
	File  string `json:"file"`
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"`
	Size  int64  `json:"size,omitzero"`
}

// Detail is a title's page.
type Detail struct {
	Title    Title       `json:"title"`
	Tagline  string      `json:"tagline"`
	Versions []version   `json:"versions"`
	Extras   []extraView `json:"extras"`
	Seasons  []Season    `json:"seasons,omitempty"`
}

// ResumeItem is a film begun, or an episode with its series.
type ResumeItem struct {
	Kind    string   `json:"kind"`
	Title   *Title   `json:"title,omitzero"`
	Episode *Episode `json:"episode,omitzero"`
	Show    *Title   `json:"show,omitzero"`
}

// titleOf is a film or a series as the catalogue sends it — every value its
// keys were made from, so the app orders it as SQLite did.
func titleOf(r *core.Record, kind string) Title {
	t := Title{
		Collection:    r.Collection().Name,
		ID:            r.Id,
		Kind:          "movie",
		Title:         r.GetString("title"),
		SortTitle:     r.GetString("sort_title"),
		OriginalTitle: r.GetString("original_title"),
		Year:          r.GetInt("year"),
		Overview:      r.GetString("overview"),
		Genres:        stringsOf(r, "genres"),
		RuntimeMs:     r.GetInt64("runtime_ms"),
		ContentRating: r.GetString("content_rating"),
		Identity:      r.GetString("identity"),
		ExternalIDs:   externalIDs(r),
		Images:        Images{Poster: r.GetString("poster"), Backdrop: r.GetString("backdrop"), Logo: r.GetString("logo")},
	}
	date, added := r.GetString("release_date"), r.GetDateTime("added")
	if kind == library.Shows {
		t.Kind = "show"
		date, added = r.GetString("first_air_date"), r.GetDateTime("last_added")
		t.Status, t.EndYear = seriesStatus(r)
	}
	// Only a date the keys could read: where they fell back to the year, the
	// app must too.
	if titles.ReleaseKey(date, 0) != "" {
		t.ReleaseDate = date
	}
	t.AddedAt = instant(added)
	if v := r.GetFloat("rating"); v > 0 {
		t.Rating = titles.Rating(v)
	}
	return t
}

// episodeOf is an episode of the series show.
func episodeOf(r *core.Record, show string) Episode {
	season, number := r.GetInt("season"), r.GetInt("episode")
	e := Episode{
		Collection: r.Collection().Name,
		ID:         r.Id,
		Show:       show,
		Season:     &season,
		Episode:    &number,
		Title:      r.GetString("title"),
		Overview:   r.GetString("overview"),
		AirDate:    r.GetString("air_date"),
		AddedAt:    instant(r.GetDateTime("added")),
		RuntimeMs:  r.GetInt64("runtime_ms"),
		Identity:   r.GetString("identity"),
		Images:     Images{Still: r.GetString("still")},
	}
	if end := r.GetInt("episode_end"); end > number {
		e.EpisodeEnd = end
	}
	if v := r.GetFloat("rating"); v > 0 {
		e.Rating = titles.Rating(v)
	}
	return e
}

// watchOf is a user's watch state as the catalogue sends it.
func watchOf(state *core.Record) *Watch {
	return &Watch{
		Watched:      state.GetBool("watched"),
		PositionMs:   state.GetInt64("position_ms"),
		DurationMs:   state.GetInt64("duration_ms"),
		LastPlayedAt: instant(state.GetDateTime("last_played_at")),
	}
}

// instant is a moment as ISO 8601 with milliseconds, or nothing.
func instant(t types.DateTime) string {
	if t.IsZero() {
		return ""
	}
	return t.Time().UTC().Format("2006-01-02T15:04:05.000Z")
}

func stringsOf(r *core.Record, field string) []string {
	var out []string
	_ = r.UnmarshalJSONField(field, &out)
	if out == nil {
		return []string{}
	}
	return out
}

// externalIDs are a title's ids in public catalogues, keyed as Loge keys
// them.
func externalIDs(r *core.Record) map[string]string {
	ids := map[string]string{}
	var stored map[string]any
	_ = r.UnmarshalJSONField("external_ids", &stored)
	for key, value := range stored {
		if text, ok := value.(string); ok && text != "" {
			ids[key] = text
		}
	}
	if n := r.GetInt("tmdb_id"); n > 0 {
		ids["tmdb"] = strconv.Itoa(n)
	}
	for key, field := range map[string]string{"imdb": "imdb_id", "tvdb": "tvdb_id"} {
		if v := r.GetString(field); v != "" {
			ids[key] = v
		}
	}
	return ids
}

// seriesStatus is TMDB's word on a series in Loge's — continuing, ended or
// upcoming — and the year one that ended did.
func seriesStatus(r *core.Record) (string, int) {
	switch strings.ToLower(r.GetString("status")) {
	case "ended", "canceled", "cancelled":
		if t, err := time.Parse("2006-01-02", r.GetString("last_air_date")); err == nil {
			return "ended", t.Year()
		}
		return "ended", 0
	case "returning series":
		return "continuing", 0
	case "planned", "in production", "pilot":
		if t, err := time.Parse("2006-01-02", r.GetString("first_air_date")); err == nil && !t.After(time.Now()) {
			return "continuing", 0
		}
		return "upcoming", 0
	}
	return "", 0
}
