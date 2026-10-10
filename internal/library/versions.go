package library

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/media"
	"foyer/internal/parse"
)

// extra is a file that came with a title and is none of its versions: a
// film inside a series' pack, an OVA, a featurette.
type extra struct {
	File  string `json:"file"`
	Path  string `json:"path"`
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"`
	Size  int64  `json:"size,omitempty"`
}

// versionOf is what a file is: ffprobe's word once it has looked, its
// name's until then.
func versionOf(file *core.Record) media.Version {
	if file.GetString("probe_state") == "done" {
		var v media.Version
		if err := file.UnmarshalJSONField("probe", &v); err == nil {
			v.File, v.Path, v.Label, v.Probed = file.Id, file.GetString("path"), file.GetString("label"), true
			if v.Size == 0 {
				v.Size = file.GetInt64("size")
			}
			return v
		}
	}
	var placed parse.Placed
	_ = file.UnmarshalJSONField("parsed", &placed)
	v := media.FromName(file.Id, file.GetString("path"), file.GetInt64("size"), placed.Release)
	v.Label = file.GetString("label")
	return v
}

// lists are a title's lists over its versions.
type lists struct {
	quality, encoding, container, hdr []string
	audio, subtitles                  []map[string]any
	audioLanguages, subtitleLanguages []string
	versions                          int
	added, lastAdded                  time.Time
	available                         bool
}

func (l *lists) addVersion(v media.Version, file *core.Record) {
	l.versions++
	if v.Quality != "" {
		addValue(&l.quality, v.Quality, Qualities)
	}
	if v.Container != "" {
		addValue(&l.container, v.Container, Containers)
	}
	if v.Video != nil {
		if v.Video.Codec != "" {
			addValue(&l.encoding, normaliseCodec(v.Video.Codec), Encodings)
		}
		if v.Video.HDR != "" {
			addValue(&l.hdr, v.Video.HDR, HDRs)
		}
	}
	for _, a := range v.Audio {
		track := map[string]any{"language": a.Language, "codec": a.Codec}
		if a.Channels != 0 {
			track["channels"] = a.Channels
		}
		if a.Spatial != "" {
			track["spatial"] = a.Spatial
		}
		addTrack(&l.audio, track)
		if a.Language != "" && !slices.Contains(l.audioLanguages, a.Language) {
			l.audioLanguages = append(l.audioLanguages, a.Language)
		}
	}
	for _, s := range v.Subtitles {
		track := map[string]any{"language": s.Language, "format": s.Format}
		if s.Forced {
			track["forced"] = true
		}
		addTrack(&l.subtitles, track)
		if s.Language != "" && !slices.Contains(l.subtitleLanguages, s.Language) {
			l.subtitleLanguages = append(l.subtitleLanguages, s.Language)
		}
	}
	added := file.GetDateTime("added").Time()
	if !added.IsZero() {
		if l.added.IsZero() || added.Before(l.added) {
			l.added = added
		}
		if added.After(l.lastAdded) {
			l.lastAdded = added
		}
	}
	if file.GetString("missing_since") == "" {
		l.available = true
	}
}

func (l *lists) merge(other *core.Record) {
	for _, pair := range []struct {
		into   *[]string
		field  string
		values []string
	}{{&l.quality, "quality", Qualities}, {&l.encoding, "encoding", Encodings}, {&l.container, "container", Containers}, {&l.hdr, "hdr", HDRs}} {
		for _, v := range other.GetStringSlice(pair.field) {
			addValue(pair.into, v, pair.values)
		}
	}
	var tracks []map[string]any
	_ = other.UnmarshalJSONField("audio_tracks", &tracks)
	for _, t := range tracks {
		addTrack(&l.audio, t)
	}
	tracks = nil
	_ = other.UnmarshalJSONField("subtitles", &tracks)
	for _, t := range tracks {
		addTrack(&l.subtitles, t)
	}
	var languages []string
	_ = other.UnmarshalJSONField("audio_languages", &languages)
	for _, lang := range languages {
		if !slices.Contains(l.audioLanguages, lang) {
			l.audioLanguages = append(l.audioLanguages, lang)
		}
	}
	languages = nil
	_ = other.UnmarshalJSONField("subtitle_languages", &languages)
	for _, lang := range languages {
		if !slices.Contains(l.subtitleLanguages, lang) {
			l.subtitleLanguages = append(l.subtitleLanguages, lang)
		}
	}
	l.versions += other.GetInt("version_count")
	added := other.GetDateTime("added").Time()
	if !added.IsZero() {
		if l.added.IsZero() || added.Before(l.added) {
			l.added = added
		}
		if added.After(l.lastAdded) {
			l.lastAdded = added
		}
	}
	l.available = l.available || other.GetBool("available")
}

func (l *lists) apply(r *core.Record, titled bool) {
	r.Set("quality", ordered(l.quality, Qualities))
	r.Set("encoding", ordered(l.encoding, Encodings))
	r.Set("container", ordered(l.container, Containers))
	r.Set("hdr", ordered(l.hdr, HDRs))
	r.Set("audio_tracks", orEmpty(l.audio))
	r.Set("subtitles", orEmpty(l.subtitles))
	r.Set("audio_languages", orEmptyStrings(l.audioLanguages))
	r.Set("subtitle_languages", orEmptyStrings(l.subtitleLanguages))
	r.Set("version_count", l.versions)
	r.Set("available", l.available)
	if !l.added.IsZero() {
		r.Set("added", l.added)
	}
	if titled && !l.lastAdded.IsZero() {
		r.Set("last_added", l.lastAdded)
	}
}

func addValue(list *[]string, value string, allowed []string) {
	if !slices.Contains(allowed, value) {
		value = "other"
		if !slices.Contains(allowed, value) {
			return
		}
	}
	if !slices.Contains(*list, value) {
		*list = append(*list, value)
	}
}

func addTrack(list *[]map[string]any, track map[string]any) {
	key, _ := json.Marshal(track)
	for _, t := range *list {
		if k, _ := json.Marshal(t); string(k) == string(key) {
			return
		}
	}
	*list = append(*list, track)
}

func ordered(values, order []string) []string {
	out := []string{}
	for _, v := range order {
		if slices.Contains(values, v) {
			out = append(out, v)
		}
	}
	return out
}

func orEmpty(list []map[string]any) []map[string]any {
	if list == nil {
		return []map[string]any{}
	}
	return list
}

func orEmptyStrings(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// normaliseCodec names a video codec as a title's list does.
func normaliseCodec(codec string) string {
	switch c := strings.ToLower(codec); c {
	case "h264", "avc", "avc1":
		return "h264"
	case "hevc", "h265", "hvc1", "hev1":
		return "hevc"
	case "mpeg2video", "mpeg2":
		return "mpeg2"
	case "mpeg4", "msmpeg4v3", "xvid", "divx":
		return "mpeg4"
	default:
		return c
	}
}

// filesOf are a title's files, as its record names them.
func filesOf(app core.App, collection, id string) ([]*core.Record, error) {
	return app.FindAllRecords(FilesCollection, dbx.HashExp{"item_collection": collection, "item_id": id})
}

// refresh rebuilds a film's or an episode's versions and lists from its
// files, and a film's extras.
func refresh(app core.App, item *core.Record) error {
	files, err := filesOf(app, item.Collection().Name, item.Id)
	if err != nil {
		return err
	}
	var l lists
	versions := []media.Version{}
	extras := []extra{}
	for _, f := range files {
		switch f.GetString("role") {
		case "version":
			v := versionOf(f)
			versions = append(versions, v)
			l.addVersion(v, f)
		case "extra":
			extras = append(extras, extraOf(f))
		case "unmapped":
			// A special or an OVA TMDB has no episode for is still the series':
			// listed with its extras, and placed again by every scan, should
			// TMDB add it.
			var placed parse.Placed
			if f.UnmarshalJSONField("parsed", &placed) == nil && placed.Special != "" {
				extras = append(extras, extraOf(f))
			}
		}
	}
	slices.SortFunc(versions, func(a, b media.Version) int { return rank(b) - rank(a) })
	item.Set("versions", versions)
	titled := item.Collection().Fields.GetByName("extras") != nil
	if titled {
		item.Set("extras", extras)
	}
	l.apply(item, titled)
	return nil
}

func extraOf(f *core.Record) extra {
	var placed parse.Placed
	_ = f.UnmarshalJSONField("parsed", &placed)
	kind := "extra"
	switch {
	case placed.MovieNumber > 0 || placed.Folder == parse.Movies:
		kind = "movie"
	case placed.Special != "":
		kind = placed.Special
	case placed.Extra != "":
		kind = placed.Extra
	}
	title := placed.EpisodeTitle
	if title == "" {
		title = placed.Title
	}
	return extra{File: f.Id, Path: f.GetString("path"), Kind: kind, Title: title, Size: f.GetInt64("size")}
}

// rank orders a title's versions best first: the tallest picture, then the
// largest file.
func rank(v media.Version) int {
	score := slices.Index(Qualities, v.Quality) * 1000
	if v.Probed {
		score += 100
	}
	return score + int(min(v.Size>>30, 99))
}

// refreshShow rebuilds a series' lists from its episodes, and its extras
// from the files that came with it.
func refreshShow(app core.App, show *core.Record) error {
	episodes, err := app.FindAllRecords(Episodes(show.Collection().Name), dbx.HashExp{"show": show.Id})
	if err != nil {
		return err
	}
	var l lists
	for _, ep := range episodes {
		l.merge(ep)
	}
	files, err := filesOf(app, show.Collection().Name, show.Id)
	if err != nil {
		return err
	}
	extras := []extra{}
	for _, f := range files {
		extras = append(extras, extraOf(f))
		if f.GetString("missing_since") == "" && f.GetString("role") == "extra" {
			l.available = true
		}
	}
	show.Set("extras", extras)
	show.Set("episodes_owned", len(episodes))
	l.apply(show, true)
	return nil
}
