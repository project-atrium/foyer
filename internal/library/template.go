// Package library turns Zurg's folders into the household's library: one set
// of collections per folder chosen as a library — a movies folder `x` is the
// collection `x`; a shows folder `x` is `x` with `x_episodes` — kept in shape
// from one template here, never from the dashboard.
package library

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/pocketbase/dbx"
	validation "github.com/pocketbase/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/pocketbase/pocketbase/tools/types"
)

// TemplateVersion is the shape of a library's collections. Raise it with any
// change to the template; Ensure brings every library up to it at start.
const TemplateVersion = 1

const (
	Movies = "movies"
	Shows  = "shows"
)

// Collections Foyer keeps for the library, beside each library's own.
const (
	LibrariesCollection  = "libraries"
	TorrentsCollection   = "media_torrents"
	FilesCollection      = "media_files"
	IdentitiesCollection = "identities"
)

// The values a title's lists may hold. Anything else a file says is "other".
var (
	Qualities  = []string{"sd", "hd", "fhd", "2k", "4k", "8k"}
	Encodings  = []string{"h264", "hevc", "av1", "vp9", "vp8", "mpeg2", "mpeg4", "vc1", "other"}
	Containers = []string{"mkv", "mp4", "avi", "ts", "m2ts", "mov", "webm", "wmv", "mpeg", "flv", "ogg", "other"}
	HDRs       = []string{"hdr10", "hdr10+", "hlg", "dolby-vision"}
)

var (
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	// reserved are names a library may never take: PocketBase's, Foyer's,
	// and what their records' tables would collide with.
	reserved = []string{"users", "adapters", "libraries", "media_torrents", "media_files", "identities", "watch_states"}
)

// Episodes is the collection of a shows library's episodes.
func Episodes(name string) string { return name + "_episodes" }

// CheckName says why a library may not be called name, or nil. Its error is
// a field error on `name`, for the form to show.
func CheckName(app core.App, name, kind, ownID string) error {
	refuse := func(message string) error {
		return validation.Errors{"name": validation.NewError("foyer_invalid_name", message)}
	}
	switch {
	case !namePattern.MatchString(name):
		return refuse("A library's name is a lower-case letter, then lower-case letters, digits or underscores.")
	case strings.Contains(name, "__") || strings.Contains(name, "_via_") || strings.HasPrefix(name, "sqlite_"):
		return refuse("That name is PocketBase's own.")
	case strings.HasSuffix(name, "_episodes"):
		return refuse("A name may not end in _episodes: a shows library's episodes are called that.")
	case slices.Contains(reserved, name):
		return refuse("That name is Foyer's own.")
	}
	names := []string{name}
	if kind == Shows {
		names = append(names, Episodes(name))
	}
	for _, n := range names {
		existing, err := app.FindCollectionByNameOrId(n)
		if err != nil {
			continue
		}
		owner, err := owningLibrary(app, existing.Name)
		if err != nil || owner == nil || owner.Id != ownID {
			return refuse(fmt.Sprintf("There is already a collection called %s.", existing.Name))
		}
	}
	return nil
}

// owningLibrary is the library a collection belongs to, or nil.
func owningLibrary(app core.App, collection string) (*core.Record, error) {
	base := strings.TrimSuffix(collection, "_episodes")
	record, err := app.FindFirstRecordByFilter(LibrariesCollection, "name = {:name}", dbx.Params{"name": base})
	if err != nil {
		return nil, nil
	}
	if base != collection && record.GetString("kind") != Shows {
		return nil, nil
	}
	return record, nil
}

// Managed reports whether a collection is a library's.
func Managed(app core.App, name string) bool {
	owner, _ := owningLibrary(app, name)
	return owner != nil
}

func usersRule(extra string) *string {
	return types.Pointer("@request.auth.collectionName = 'users' && " + extra)
}

func artwork(name string, thumbs ...string) *core.FileField {
	return &core.FileField{
		Name: name, MaxSelect: 1, MaxSize: 15 << 20,
		MimeTypes: []string{"image/jpeg", "image/png", "image/webp"},
		Thumbs:    thumbs,
	}
}

// common are the fields every title has, a film, a series or an episode.
func common() []core.Field {
	return []core.Field{
		&core.TextField{Name: "title", Required: true, Max: 500},
		&core.TextField{Name: "overview", Max: 10000},
		&core.NumberField{Name: "rating"},
		&core.NumberField{Name: "runtime_ms", OnlyInt: true},
		&core.NumberField{Name: "tmdb_id", OnlyInt: true},
		// What Loge and Foyer key watch status by: `tmdb:movie:603`,
		// `tmdb:tv:63926/s3e11`, or the title and year where TMDB said nothing.
		&core.TextField{Name: "identity", Max: 300},
		// What a title's files are, one entry per file: the file, its path in
		// Zurg, and what ffprobe — or until then its name — says.
		&core.JSONField{Name: "versions", MaxSize: 512 << 10},
		&core.SelectField{Name: "quality", MaxSelect: len(Qualities), Values: Qualities},
		&core.SelectField{Name: "encoding", MaxSelect: len(Encodings), Values: Encodings},
		&core.SelectField{Name: "container", MaxSelect: len(Containers), Values: Containers},
		&core.SelectField{Name: "hdr", MaxSelect: len(HDRs), Values: HDRs},
		&core.JSONField{Name: "audio_tracks", MaxSize: 64 << 10},
		&core.JSONField{Name: "subtitles", MaxSize: 64 << 10},
		&core.JSONField{Name: "audio_languages", MaxSize: 4 << 10},
		&core.JSONField{Name: "subtitle_languages", MaxSize: 4 << 10},
		&core.NumberField{Name: "version_count", OnlyInt: true},
		&core.DateField{Name: "added"},
		&core.BoolField{Name: "available"},
		&core.AutodateField{Name: "created", OnCreate: true},
		&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
	}
}

// titled are the fields of a film or a series: what TMDB says of it, and how
// sure Foyer is that TMDB said it of this.
func titled() []core.Field {
	return []core.Field{
		&core.TextField{Name: "original_title", Max: 500},
		&core.TextField{Name: "sort_title", Max: 500},
		&core.NumberField{Name: "year", OnlyInt: true},
		&core.TextField{Name: "tagline", Max: 1000},
		&core.JSONField{Name: "genres", MaxSize: 4 << 10},
		&core.TextField{Name: "content_rating", Max: 20},
		&core.TextField{Name: "original_language", Max: 10},
		&core.TextField{Name: "imdb_id", Max: 20},
		&core.TextField{Name: "tvdb_id", Max: 20},
		&core.JSONField{Name: "external_ids", MaxSize: 4 << 10},
		artwork("poster", "185x278", "342x513"),
		artwork("backdrop", "640x360", "1280x720"),
		artwork("logo", "500x0"),
		&core.TextField{Name: "poster_path", Max: 200},
		&core.TextField{Name: "backdrop_path", Max: 200},
		&core.TextField{Name: "logo_path", Max: 200},
		// Films, OVAs and featurettes that came with it, as files.
		&core.JSONField{Name: "extras", MaxSize: 128 << 10},
		&core.DateField{Name: "last_added"},
		// matched: TMDB was sure; uncertain: a best guess, hidden from users
		// until a superuser confirms or corrects it; unmatched: TMDB had
		// nothing, and the title is its name's; manual: a superuser decided.
		&core.SelectField{Name: "match_state", Required: true, MaxSelect: 1, Values: []string{"matched", "uncertain", "unmatched", "manual"}},
		&core.TextField{Name: "match_key", Max: 600},
		&core.DateField{Name: "metadata_at"},
	}
}

// template is the collections a library of kind is made of, in order: a
// series' collection before its episodes'.
func template(name, kind, showsID string) []*core.Collection {
	visible := usersRule("match_state != 'uncertain'")
	items := core.NewBaseCollection(name)
	items.ListRule, items.ViewRule = visible, visible
	items.Fields.Add(common()...)
	items.Fields.Add(titled()...)
	items.Fields.Add(&core.TextField{Name: "release_date", Max: 10})
	items.AddIndex("idx_"+name+"_identity", true, "`identity`", "`identity` != ''")
	items.AddIndex("idx_"+name+"_tmdb", false, "`tmdb_id`", "")
	items.AddIndex("idx_"+name+"_key", false, "`match_key`", "")
	items.AddIndex("idx_"+name+"_last_added", false, "`last_added`", "")
	if kind == Movies {
		return []*core.Collection{items}
	}
	items.Fields.Add(
		&core.TextField{Name: "first_air_date", Max: 10},
		&core.TextField{Name: "last_air_date", Max: 10},
		&core.TextField{Name: "status", Max: 40},
		&core.NumberField{Name: "season_count", OnlyInt: true},
		&core.NumberField{Name: "episode_count", OnlyInt: true},
		// TMDB's seasons: number, name, air date, episode count, poster path.
		&core.JSONField{Name: "seasons", MaxSize: 128 << 10},
		// per-season, or absolute where TMDB numbers a series' episodes from
		// its start, as it does One Piece's.
		&core.TextField{Name: "numbering", Max: 20},
		&core.TextField{Name: "absolute_group", Max: 40},
		&core.NumberField{Name: "episodes_owned", OnlyInt: true},
	)
	items.Fields.RemoveByName("release_date")
	episodes := core.NewBaseCollection(Episodes(name))
	episodes.ListRule = usersRule("show.match_state != 'uncertain'")
	episodes.ViewRule = episodes.ListRule
	episodes.Fields.Add(&core.RelationField{Name: "show", CollectionId: showsID, MaxSelect: 1, Required: true, CascadeDelete: true})
	episodes.Fields.Add(common()...)
	episodes.Fields.Add(
		&core.NumberField{Name: "season", OnlyInt: true},
		&core.NumberField{Name: "episode", OnlyInt: true},
		&core.NumberField{Name: "episode_end", OnlyInt: true},
		&core.NumberField{Name: "absolute", OnlyInt: true},
		&core.TextField{Name: "air_date", Max: 10},
		artwork("still", "300x169"),
		&core.TextField{Name: "still_path", Max: 200},
		// How its files were laid on it: release, offset, absolute, group,
		// special, or unknown for a series TMDB did not name.
		&core.TextField{Name: "mapped_by", Max: 20},
	)
	episodes.AddIndex("idx_"+Episodes(name)+"_number", true, "`show`, `season`, `episode`", "")
	episodes.AddIndex("idx_"+Episodes(name)+"_identity", false, "`identity`", "")
	episodes.AddIndex("idx_"+Episodes(name)+"_added", false, "`added`", "")
	return []*core.Collection{items, episodes}
}

// Ensure makes a library's collections, or brings them to the template:
// fields added, changed or dropped, rules and indexes as the template has
// them. A new collection takes a random id: PocketBase removes a deleted
// collection's files in the background, under its id, and a library made
// again at once must not lose its new pictures to that.
func Ensure(app core.App, lib *core.Record) error {
	name, kind := lib.GetString("name"), lib.GetString("kind")
	if err := CheckName(app, name, kind, lib.Id); err != nil {
		return err
	}
	return app.RunInTransaction(func(tx core.App) error {
		showsID := ""
		for i := range template(name, kind, "") {
			// The episodes' relation points at the series' collection, saved
			// first.
			want := template(name, kind, showsID)[i]
			c, err := tx.FindCollectionByNameOrId(want.Name)
			if err != nil {
				c = core.NewBaseCollection(want.Name, "pbc_"+security.RandomStringWithAlphabet(12, "abcdefghijklmnopqrstuvwxyz0123456789"))
			}
			for _, f := range c.Fields {
				if f.GetSystem() {
					continue
				}
				if want.Fields.GetByName(f.GetName()) == nil {
					c.Fields.RemoveByName(f.GetName())
				}
			}
			for _, f := range want.Fields {
				if f.GetSystem() || f.GetName() == "id" {
					continue
				}
				c.Fields.Add(f)
			}
			c.ListRule, c.ViewRule = want.ListRule, want.ViewRule
			c.CreateRule, c.UpdateRule, c.DeleteRule = nil, nil, nil
			c.Indexes = want.Indexes
			if err := tx.Save(c); err != nil {
				return fmt.Errorf("the collection %s: %w", want.Name, err)
			}
			if i == 0 {
				showsID = c.Id
			}
		}
		lib.Set("template", TemplateVersion)
		return tx.Save(lib)
	})
}

// Remove deletes a library: its collections — PocketBase removes their files
// — and its record, with its torrents, files and identities. Watch status
// stays, keyed by what was watched.
func Remove(app core.App, lib *core.Record) error {
	name := lib.GetString("name")
	for _, n := range []string{Episodes(name), name} {
		c, err := app.FindCollectionByNameOrId(n)
		if err != nil {
			continue
		}
		if err := app.Delete(c); err != nil {
			return fmt.Errorf("the collection %s: %w", n, err)
		}
	}
	return app.Delete(lib)
}
