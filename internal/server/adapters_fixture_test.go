package server_test

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"foyer/internal/adapters"
	"foyer/internal/fixtures"
	"foyer/internal/iptv"
	"foyer/internal/server"
)

// The app's `adapters/api/fixtures/adapters.json`: every adapter of Loge and
// Foyer, as Loge's manifests describe them — the one description both hold
// their adapters to. Help texts are each side's own and are not in it.
type adapterFixture struct {
	Categories []struct {
		ID   string   `json:"id"`
		Runs []string `json:"runs"`
	} `json:"categories"`
	Adapters []fixtureAdapter `json:"adapters"`
}

type fixtureAdapter struct {
	ID          string         `json:"id"`
	Category    string         `json:"category"`
	DisplayName string         `json:"displayName"`
	Runs        []string       `json:"runs"`
	Fields      []fixtureField `json:"fields"`
}

type fixtureField struct {
	Key      string            `json:"key"`
	Label    string            `json:"label"`
	Type     string            `json:"type"`
	Runs     []string          `json:"runs"`
	Required bool              `json:"required"`
	Default  any               `json:"default"`
	Options  []adapters.Option `json:"options"`
	Min      *int              `json:"min"`
	Max      *int              `json:"max"`
	Pattern  string            `json:"pattern"`
	Visible  bool              `json:"visible"`
}

// The app's field types, by Foyer's.
var fieldTypes = map[adapters.FieldType]string{
	adapters.Text: "text", adapters.URL: "url", adapters.Password: "password",
	adapters.Number: "number", adapters.Select: "select", adapters.Bool: "boolean",
}

var qualified = regexp.MustCompile(`^([a-z]+)/[a-z][a-z0-9-]*$`)

// listed is every adapter the Adapters page lists: as Bind registers them,
// an adapter that is built taking the place of a planned one of its key.
func listed() []adapters.Adapter {
	all := server.AllAdapters(iptv.Adapter(nil))
	return adapters.NewRegistry(nil, all...).Adapters()
}

func TestEveryAdapterIsTheAppsManifest(t *testing.T) {
	var fixture adapterFixture
	fixtures.Load(t, "../adapters.json", &fixture)
	if problems := differences(fixture, listed(), adapters.Categories); len(problems) > 0 {
		t.Fatalf("Foyer's adapters and the app's manifests (adapters/api/fixtures/adapters.json) differ:\n- %s", strings.Join(problems, "\n- "))
	}
}

// The comparison fails where it should: each kind of difference, made on a
// copy of what is registered, is named.
func TestAnAdapterThatDiffersFromItsManifestIsNamed(t *testing.T) {
	var fixture adapterFixture
	fixtures.Load(t, "../adapters.json", &fixture)
	edit := func(id string, change func(*adapters.Adapter)) []adapters.Adapter {
		out := []adapters.Adapter{}
		for _, a := range listed() {
			a.Fields = slices.Clone(a.Fields)
			if a.ID == id {
				change(&a)
			}
			out = append(out, a)
		}
		return out
	}
	field := func(a *adapters.Adapter, key string) *adapters.Field {
		i := slices.IndexFunc(a.Fields, func(f adapters.Field) bool { return f.Key == key })
		if i < 0 {
			t.Fatalf("%s has no field %q", a.ID, key)
		}
		return &a.Fields[i]
	}
	cases := map[string]struct {
		list       []adapters.Adapter
		categories []adapters.Category
		want       string
	}{
		"no entry":    {list: edit("sources/webdav", func(a *adapters.Adapter) { a.ID = "sources/webdavs" }), want: `"sources/webdavs" has no entry`},
		"device only": {list: append(listed(), adapters.Adapter{Key: "mpv", ID: "players/mpv", Category: "players", Name: "mpv"}), want: `"players/mpv" does not run on the server`},
		"category":    {list: edit("metadata/tmdb", func(a *adapters.Adapter) { a.Category = adapters.Tools }), want: `"metadata/tmdb" is in tools`},
		"label":       {list: edit("iptv/stalker", func(a *adapters.Adapter) { field(a, "guideByCountry").Label = "Put other countries' guides right" }), want: `"iptv/stalker" field "guideByCountry": label`},
		"type":        {list: edit("sources/webdav", func(a *adapters.Adapter) { field(a, "interval").Type = adapters.Text }), want: `"sources/webdav" field "interval": type`},
		"required":    {list: edit("tracking/trakt", func(a *adapters.Adapter) { field(a, "clientSecret").Required = false }), want: `"tracking/trakt" field "clientSecret": required`},
		"default":     {list: edit("tools/ffmpeg", func(a *adapters.Adapter) { field(a, "copies_room_gb").Default = 50 }), want: `"tools/ffmpeg" field "copies_room_gb": default`},
		"options":     {list: edit("tools/ffmpeg", func(a *adapters.Adapter) { field(a, "copy_effort").Options = field(a, "copy_effort").Options[:2] }), want: `"tools/ffmpeg" field "copy_effort": options`},
		"min":         {list: edit("iptv/stalker", func(a *adapters.Adapter) { field(a, "streams").Min = new(2) }), want: `"iptv/stalker" field "streams": min`},
		"max":         {list: edit("iptv/stalker", func(a *adapters.Adapter) { field(a, "lineupDays").Max = new(31) }), want: `"iptv/stalker" field "lineupDays": max`},
		// A bound of 0 is one: the app's `min: 0` against none here is a difference.
		"min 0, or none": {list: edit("tools/ffmpeg", func(a *adapters.Adapter) { field(a, "copies_room_gb").Min = nil }), want: `"tools/ffmpeg" field "copies_room_gb": min is none here, 0 in the manifest`},
		"a bound more":   {list: edit("sources/webdav", func(a *adapters.Adapter) { field(a, "interval").Max = nil; field(a, "interval").Min = new(0) }), want: `"sources/webdav" field "interval": min is 0 here, 5 in the manifest`},
		"a choice less":  {list: edit("iptv/stalker", func(a *adapters.Adapter) { f := field(a, "timeZone"); f.Options = f.Options[:len(f.Options)-1] }), want: `"iptv/stalker" field "timeZone": options`},
		"pattern":        {list: edit("sources/youtube", func(a *adapters.Adapter) { field(a, "region").Pattern = `^[A-Z]{3}$` }), want: `"sources/youtube" field "region": pattern`},
		"visible":        {list: edit("iptv/stalker", func(a *adapters.Adapter) { field(a, "mac").Visible = false }), want: `"iptv/stalker" field "mac": visible`},
		"a field more": {list: edit("sources/local", func(a *adapters.Adapter) {
			a.Fields = append(a.Fields, adapters.Field{Key: "depth", Label: "Depth", Type: adapters.Number})
		}), want: `"sources/local" field "depth" is not in the manifest`},
		"a field less":  {list: edit("sources/plex", func(a *adapters.Adapter) { a.Fields = a.Fields[:1] }), want: `"sources/plex" has no field "token"`},
		"no adapter":    {list: slices.DeleteFunc(listed(), func(a adapters.Adapter) bool { return a.ID == "tracking/simkl" }), want: `"tracking/simkl" runs on the server, and Foyer has no such adapter`},
		"section order": {list: listed(), categories: []adapters.Category{adapters.Sources, adapters.Metadata, adapters.IPTV, adapters.Tracking, adapters.Tools}, want: "sections"},
		"the name":      {list: edit("sources/local", func(a *adapters.Adapter) { a.Name = "Folder" }), want: `"sources/local" is named "Folder"`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			categories := c.categories
			if categories == nil {
				categories = adapters.Categories
			}
			problems := differences(fixture, c.list, categories)
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, c.want) }) {
				t.Fatalf("want a problem naming %q, got %q", c.want, problems)
			}
		})
	}
}

// differences are what keeps Foyer's adapters from being the app's: an
// adapter with no entry, or one that does not run on the server, or whose
// category, name or fields — key, label, type, required, default, options,
// min, max, pattern, visible — differ from the fields its entry runs on the
// server; an entry that runs on the server with no adapter here; and sections
// out of the shared order.
func differences(fixture adapterFixture, list []adapters.Adapter, categories []adapters.Category) []string {
	var problems []string
	server := []string{}
	for _, c := range fixture.Categories {
		if slices.Contains(c.Runs, "server") {
			server = append(server, c.ID)
		}
	}
	sections := []string{}
	for _, c := range categories {
		sections = append(sections, string(c))
	}
	if !slices.Equal(sections, server) {
		problems = append(problems, fmt.Sprintf("the sections are %v; the categories that run on the server are %v, in that order", sections, server))
	}

	entries := map[string]fixtureAdapter{}
	for _, e := range fixture.Adapters {
		entries[e.ID] = e
	}
	seen := map[string]bool{}
	for _, a := range list {
		seen[a.ID] = true
		m := qualified.FindStringSubmatch(a.ID)
		if m == nil || m[1] != string(a.Category) {
			problems = append(problems, fmt.Sprintf("%q (key %q) is not a qualified id in its category, %s", a.ID, a.Key, a.Category))
		}
		e, ok := entries[a.ID]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%q has no entry: the app has no manifest of that id", a.ID))
			continue
		case !slices.Contains(e.Runs, "server"):
			problems = append(problems, fmt.Sprintf("%q does not run on the server, by its manifest", a.ID))
			continue
		}
		if string(a.Category) != e.Category {
			problems = append(problems, fmt.Sprintf("%q is in %s; its manifest is in %s", a.ID, a.Category, e.Category))
		}
		if a.Name != e.DisplayName {
			problems = append(problems, fmt.Sprintf("%q is named %q; its manifest %q", a.ID, a.Name, e.DisplayName))
		}
		problems = append(problems, fieldDifferences(a, e)...)
	}
	for _, e := range fixture.Adapters {
		if slices.Contains(e.Runs, "server") && !seen[e.ID] {
			problems = append(problems, fmt.Sprintf("%q runs on the server, and Foyer has no such adapter", e.ID))
		}
	}
	return problems
}

func fieldDifferences(a adapters.Adapter, e fixtureAdapter) []string {
	var problems []string
	wanted := map[string]fixtureField{}
	for _, f := range e.Fields {
		if slices.Contains(f.Runs, "server") {
			wanted[f.Key] = f
		}
	}
	has := map[string]bool{}
	for _, f := range a.Fields {
		has[f.Key] = true
		w, ok := wanted[f.Key]
		if !ok {
			problems = append(problems, fmt.Sprintf("%q field %q is not in the manifest, or not as one that runs on the server", a.ID, f.Key))
			continue
		}
		differ := func(what string, got, want any) {
			if !reflect.DeepEqual(got, want) {
				problems = append(problems, fmt.Sprintf("%q field %q: %s is %v here, %v in the manifest", a.ID, f.Key, what, shown(got), shown(want)))
			}
		}
		differ("label", f.Label, w.Label)
		differ("type", fieldTypes[f.Type], w.Type)
		differ("required", f.Required, w.Required)
		differ("default", normalDefault(f), normalFixtureDefault(w))
		differ("options", optionsOf(f.Options), optionsOf(w.Options))
		// Presence too: nil is no bound, and a bound of 0 is one.
		differ("min", f.Min, w.Min)
		differ("max", f.Max, w.Max)
		differ("pattern", f.Pattern, w.Pattern)
		differ("visible", f.Visible, w.Visible)
	}
	for _, f := range e.Fields {
		if slices.Contains(f.Runs, "server") && !has[f.Key] {
			problems = append(problems, fmt.Sprintf("%q has no field %q, which its manifest runs on the server", a.ID, f.Key))
		}
	}
	return problems
}

// A Go default is a string, an int or a bool; nothing is a bool's false, a
// number's nothing, a text's "" — as Settings reads them.
func normalDefault(f adapters.Field) any {
	switch v := f.Default.(type) {
	case int:
		return float64(v)
	case nil:
		if f.Type == adapters.Bool {
			return false
		}
		return nil
	case string:
		if v == "" {
			return nil
		}
	}
	return f.Default
}

func normalFixtureDefault(f fixtureField) any {
	if f.Default == nil && f.Type == "boolean" {
		return false
	}
	if s, ok := f.Default.(string); ok && s == "" {
		return nil
	}
	return f.Default
}

func optionsOf(options []adapters.Option) []adapters.Option {
	if len(options) == 0 {
		return nil
	}
	return options
}

// shown is a value as a difference names it: a bound by its number, or none.
func shown(v any) any {
	if n, ok := v.(*int); ok {
		if n == nil {
			return "none"
		}
		return *n
	}
	return v
}
