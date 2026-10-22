// Package adapters keeps what Foyer connects to — WebDAV shares, TMDB,
// ffmpeg, and those still to come — and how the household set each up. An
// adapter is a kind of thing Foyer can reach; a connection is one of them set
// up: two WebDAV shares are two connections of one adapter. They are records
// in `connections`, edited on the dashboard's Adapters page and nowhere else.
//
// An adapter describes its own fields, so the page draws every form from
// them and this package validates every save against them. A password field
// is the only secret: it is stored as typed (`secrets`), never sent back —
// the page sees "******" — and kept when a save leaves it out.
package adapters

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/dbx"
	validation "github.com/pocketbase/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/core"
)

// Collection holds one record per connection.
const Collection = "connections"

// Masked stands for a secret that is set, wherever one is sent to the page.
const Masked = "******"

var (
	// ErrNoConnection is a connection that does not exist, or no longer does.
	ErrNoConnection = errors.New("there is no such connection")
	// ErrPlanned is an adapter Foyer cannot connect to yet.
	ErrPlanned = errors.New("Foyer cannot connect to it yet")
	// ErrSingle is a second connection of an adapter the household has once.
	ErrSingle = errors.New("the household has one of these, and it is set up already")
)

// Category is where the Adapters page lists an adapter.
type Category string

const (
	Sources  Category = "sources"
	IPTV     Category = "iptv"
	Metadata Category = "metadata"
	Tracking Category = "tracking"
	Tools    Category = "tools"
)

// Categories are the page's sections, in its order: the categories of Loge's
// one list (`PLUGIN_CATEGORIES` in `@loge/api`) that run on a server, in that
// list's order. A test holds them to the app's fixture.
var Categories = []Category{Sources, IPTV, Metadata, Tracking, Tools}

type FieldType string

const (
	Text     FieldType = "text"
	URL      FieldType = "url"
	Password FieldType = "password"
	Number   FieldType = "number"
	Select   FieldType = "select"
	Bool     FieldType = "bool"
)

type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Field is one value an adapter asks for. Default is a string, an int or a
// bool, as its type is.
type Field struct {
	Key      string    `json:"key"`
	Label    string    `json:"label"`
	Type     FieldType `json:"type"`
	Required bool      `json:"required,omitempty"`
	Default  any       `json:"default,omitempty"`
	Options  []Option  `json:"options,omitempty"`
	Min      int       `json:"min,omitempty"`
	Max      int       `json:"max,omitempty"`
	// Pattern, for a text field, is what its value must match.
	Pattern string `json:"pattern,omitempty"`
	// Visible, for a password, shows it as typed and sends it back: a
	// portal's MAC address is no secret to whoever holds it, and goes wrong
	// typed unseen. It is kept with the secrets all the same.
	Visible bool   `json:"visible,omitempty"`
	Help    string `json:"help,omitempty"`
}

// Result is what a test of a connection found. A provider's refusal is a
// result, not an error: the page shows it beside the form.
type Result struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

type Adapter struct {
	// Key is what `connections` stores as the connection's adapter. It stays
	// as it was stored, so no connection moves.
	Key string
	// ID is the adapter's qualified id, `category/name` — `iptv/stalker`,
	// `sources/google-drive` — the one Loge's manifest has. The app's
	// `adapters/api/fixtures/adapters.json` describes every adapter by it, and
	// a test holds each of Foyer's to its entry there.
	ID          string
	Category    Category
	Name        string
	Description string
	Fields      []Field
	// Notice is what the service's terms ask to be shown wherever it is used.
	Notice string
	// Single is an adapter the household sets up once: one TMDB key, one
	// ffmpeg.
	Single bool
	// Planned is an adapter Foyer cannot connect to yet: listed, so the
	// household sees what is coming, and never set up.
	Planned bool
	// Test tries the settings once. It never retries a refused sign-in.
	Test func(ctx context.Context, s Settings) Result
}

// Settings are a connection's values as saved, its adapter's defaults filled
// in and its secrets in place.
type Settings struct {
	Enabled bool
	Values  map[string]any
}

func (s Settings) String(key string) string {
	v, _ := s.Values[key].(string)
	return v
}

func (s Settings) Int(key string) int {
	switch v := s.Values[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

func (s Settings) Bool(key string) bool {
	v, _ := s.Values[key].(bool)
	return v
}

// Connection is one adapter set up: a WebDAV share, the TMDB key.
type Connection struct {
	ID      string
	Adapter string
	Name    string
	Settings
}

// Registry is every adapter this build has, and the one way to read or
// change their connections.
type Registry struct {
	app      core.App
	adapters []Adapter
	mu       sync.Mutex
	watchers []func(Connection)
	keepers  []func(Connection) error
}

// NewRegistry lists adapters in the order given; a key given twice is the
// first's, so an adapter that is built replaces its planned stand-in.
func NewRegistry(app core.App, adapters ...Adapter) *Registry {
	r := &Registry{app: app}
	for _, a := range adapters {
		if _, ok := r.Adapter(a.Key); !ok {
			r.adapters = append(r.adapters, a)
		}
	}
	return r
}

func (r *Registry) Adapter(key string) (Adapter, bool) {
	i := slices.IndexFunc(r.adapters, func(a Adapter) bool { return a.Key == key })
	if i < 0 {
		return Adapter{}, false
	}
	return r.adapters[i], true
}

// Adapters are every adapter, in the order they were registered.
func (r *Registry) Adapters() []Adapter { return slices.Clone(r.adapters) }

// OnChange calls back after a connection was made, saved or removed, so
// whatever runs on it can start again with what it is now.
func (r *Registry) OnChange(fn func(Connection)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.watchers = append(r.watchers, fn)
}

// OnDelete asks before a connection is removed: an error keeps it, and says
// why — a share whose folders are libraries.
func (r *Registry) OnDelete(fn func(Connection) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keepers = append(r.keepers, fn)
}

func (r *Registry) changed(c Connection) {
	r.mu.Lock()
	watchers := slices.Clone(r.watchers)
	r.mu.Unlock()
	for _, fn := range watchers {
		fn(c)
	}
}

// Connections are an adapter's connections, in the page's order.
func (r *Registry) Connections(key string) ([]Connection, error) {
	a, ok := r.Adapter(key)
	if !ok {
		return nil, fmt.Errorf("no adapter %q", key)
	}
	records, err := r.records(key)
	if err != nil {
		return nil, err
	}
	out := make([]Connection, 0, len(records))
	for _, record := range records {
		out = append(out, a.connection(record))
	}
	return out, nil
}

// Connection is one connection, by its id.
func (r *Registry) Connection(id string) (Connection, error) {
	record, a, err := r.find(id)
	if err != nil {
		return Connection{}, err
	}
	return a.connection(record), nil
}

// Settings are a single adapter's settings — its one connection's values over
// its defaults: off, with the defaults, until it is first saved.
func (r *Registry) Settings(key string) (Settings, error) {
	a, ok := r.Adapter(key)
	if !ok {
		return Settings{}, fmt.Errorf("no adapter %q", key)
	}
	records, err := r.records(key)
	if err != nil {
		return Settings{}, err
	}
	if len(records) == 0 {
		return a.settings(nil), nil
	}
	return a.settings(records[0]), nil
}

func (r *Registry) records(key string) ([]*core.Record, error) {
	return r.app.FindRecordsByFilter(Collection, "adapter = {:adapter}", "position,created", 0, 0, dbx.Params{"adapter": key})
}

func (r *Registry) find(id string) (*core.Record, Adapter, error) {
	record, err := r.app.FindRecordById(Collection, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, Adapter{}, ErrNoConnection
		}
		return nil, Adapter{}, err
	}
	a, ok := r.Adapter(record.GetString("adapter"))
	if !ok {
		return nil, Adapter{}, ErrNoConnection
	}
	return record, a, nil
}

func (a Adapter) defaults() map[string]any {
	values := map[string]any{}
	for _, f := range a.Fields {
		if f.Default != nil {
			values[f.Key] = f.Default
		}
	}
	return values
}

func (a Adapter) settings(record *core.Record) Settings {
	s := Settings{Values: a.defaults()}
	if record == nil {
		return s
	}
	s.Enabled = record.GetBool("enabled")
	stored := map[string]any{}
	_ = record.UnmarshalJSONField("config", &stored)
	secrets := map[string]any{}
	_ = record.UnmarshalJSONField("secrets", &secrets)
	for _, f := range a.Fields {
		from := stored
		if f.Type == Password {
			from = secrets
		}
		if v, ok := from[f.Key]; ok {
			s.Values[f.Key] = normalise(f, v)
		}
	}
	return s
}

func (a Adapter) connection(record *core.Record) Connection {
	name := record.GetString("name")
	if name == "" {
		name = a.Name
	}
	return Connection{ID: record.Id, Adapter: a.Key, Name: name, Settings: a.settings(record)}
}

// normalise turns a stored JSON value back into the field's own type.
func normalise(f Field, v any) any {
	switch f.Type {
	case Number:
		if n, ok := v.(float64); ok {
			return int(n)
		}
	}
	return v
}

// TypeView is an adapter as the page sees it: its fields, what a new
// connection starts with, and its connections.
type TypeView struct {
	Key         string           `json:"key"`
	Category    Category         `json:"category"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Notice      string           `json:"notice,omitempty"`
	Single      bool             `json:"single"`
	Planned     bool             `json:"planned"`
	Fields      []Field          `json:"fields"`
	Defaults    map[string]any   `json:"defaults"`
	Connections []ConnectionView `json:"connections"`
}

// ConnectionView is a connection as the page sees it: its values with every
// secret masked but those shown as typed.
type ConnectionView struct {
	ID      string         `json:"id"`
	Adapter string         `json:"adapter"`
	Name    string         `json:"name"`
	Enabled bool           `json:"enabled"`
	Values  map[string]any `json:"values"`
	Status  *Status        `json:"status,omitempty"`
}

// Status is the last test's result, and when it ran.
type Status struct {
	Result
	At time.Time `json:"at"`
}

// Views are every adapter as the page lists them: by category, then in the
// order they were registered.
func (r *Registry) Views() ([]TypeView, error) {
	views := []TypeView{}
	for _, category := range Categories {
		for _, a := range r.adapters {
			if a.Category != category {
				continue
			}
			records, err := r.records(a.Key)
			if err != nil {
				return nil, err
			}
			view := TypeView{
				Key: a.Key, Category: a.Category, Name: a.Name, Description: a.Description, Notice: a.Notice,
				Single: a.Single, Planned: a.Planned, Fields: a.Fields, Defaults: a.mask(a.defaults()),
				Connections: []ConnectionView{},
			}
			for _, record := range records {
				view.Connections = append(view.Connections, a.view(record))
			}
			views = append(views, view)
		}
	}
	return views, nil
}

// View is one connection as the page sees it.
func (r *Registry) View(id string) (ConnectionView, error) {
	record, a, err := r.find(id)
	if err != nil {
		return ConnectionView{}, err
	}
	return a.view(record), nil
}

func (a Adapter) view(record *core.Record) ConnectionView {
	c := a.connection(record)
	view := ConnectionView{ID: c.ID, Adapter: a.Key, Name: c.Name, Enabled: c.Enabled, Values: a.mask(c.Values)}
	var status Status
	if err := record.UnmarshalJSONField("status", &status); err == nil && !status.At.IsZero() {
		view.Status = &status
	}
	return view
}

// mask hides every secret that is set, but a password shown as typed.
func (a Adapter) mask(values map[string]any) map[string]any {
	values = maps.Clone(values)
	for _, f := range a.Fields {
		if f.Type != Password || f.Visible {
			continue
		}
		if v, _ := values[f.Key].(string); v != "" {
			values[f.Key] = Masked
		} else {
			values[f.Key] = ""
		}
	}
	return values
}

// merge lays what the page sent over what is saved — over the adapter's
// defaults for a connection not yet made. A secret left out, or sent back as
// Masked, keeps its saved value; one sent empty is cleared. The answer is
// validated: field errors are keyed `values.<key>`, as the page's inputs are
// named.
func (a Adapter) merge(record *core.Record, sent map[string]any) (Settings, error) {
	s := a.settings(record)
	errs := validation.Errors{}
	for _, f := range a.Fields {
		v, present := sent[f.Key]
		if !present || (f.Type == Password && v == Masked) {
			continue
		}
		value, err := f.check(v)
		if err != nil {
			errs[f.Key] = err
			continue
		}
		s.Values[f.Key] = value
	}
	for _, f := range a.Fields {
		if _, failed := errs[f.Key]; failed || !f.Required {
			continue
		}
		if v, ok := s.Values[f.Key]; !ok || v == "" || v == nil {
			errs[f.Key] = validation.NewError("validation_required", "Cannot be blank.")
		}
	}
	if len(errs) > 0 {
		return s, validation.Errors{"values": errs}
	}
	return s, nil
}

// Create sets an adapter up once more: a second share. A single adapter is
// set up once, and through Save.
func (r *Registry) Create(key, name string, enabled bool, sent map[string]any) (Connection, error) {
	a, ok := r.Adapter(key)
	switch {
	case !ok:
		return Connection{}, fmt.Errorf("no adapter %q", key)
	case a.Planned:
		return Connection{}, fmt.Errorf("%s: %w", a.Name, ErrPlanned)
	}
	records, err := r.records(key)
	if err != nil {
		return Connection{}, err
	}
	if a.Single && len(records) > 0 {
		return Connection{}, fmt.Errorf("%s: %w", a.Name, ErrSingle)
	}
	collection, err := r.app.FindCollectionByNameOrId(Collection)
	if err != nil {
		return Connection{}, err
	}
	record := core.NewRecord(collection)
	record.Set("adapter", key)
	position := 0
	for _, existing := range records {
		position = max(position, existing.GetInt("position")+1)
	}
	record.Set("position", position)
	return r.write(a, record, name, enabled, sent)
}

// Update saves a connection's name, switch and values.
func (r *Registry) Update(id, name string, enabled bool, sent map[string]any) (Connection, error) {
	record, a, err := r.find(id)
	if err != nil {
		return Connection{}, err
	}
	return r.write(a, record, name, enabled, sent)
}

// Save saves a single adapter's one connection, making it if it never was.
func (r *Registry) Save(key string, enabled bool, sent map[string]any) (Connection, error) {
	a, ok := r.Adapter(key)
	if !ok {
		return Connection{}, fmt.Errorf("no adapter %q", key)
	}
	records, err := r.records(key)
	if err != nil {
		return Connection{}, err
	}
	if len(records) == 0 {
		return r.Create(key, a.Name, enabled, sent)
	}
	return r.write(a, records[0], records[0].GetString("name"), enabled, sent)
}

func (r *Registry) write(a Adapter, record *core.Record, name string, enabled bool, sent map[string]any) (Connection, error) {
	s, err := a.merge(record, sent)
	name = strings.TrimSpace(name)
	if name == "" {
		name = a.Name
	}
	if len(name) > 100 {
		fields := validation.Errors{"name": validation.NewError("validation_max_text_constraint", "Must be no more than 100 characters.")}
		if values, ok := err.(validation.Errors); ok {
			maps.Copy(fields, values)
		}
		return Connection{}, fields
	}
	if err != nil {
		return Connection{}, err
	}
	s.Enabled = enabled
	// A secret Foyer made itself — a token no field asks for — is kept.
	config, secrets := map[string]any{}, map[string]any{}
	_ = record.UnmarshalJSONField("secrets", &secrets)
	for _, f := range a.Fields {
		v, ok := s.Values[f.Key]
		if !ok {
			continue
		}
		if f.Type == Password {
			secrets[f.Key] = v
		} else {
			config[f.Key] = v
		}
	}
	record.Set("name", name)
	record.Set("enabled", enabled)
	record.Set("config", config)
	record.Set("secrets", secrets)
	if err := r.app.Save(record); err != nil {
		return Connection{}, err
	}
	c := a.connection(record)
	r.changed(c)
	return c, nil
}

// Delete removes a connection, unless what depends on it keeps it.
func (r *Registry) Delete(id string) error {
	record, a, err := r.find(id)
	if err != nil {
		return err
	}
	c := a.connection(record)
	r.mu.Lock()
	keepers := slices.Clone(r.keepers)
	r.mu.Unlock()
	for _, keep := range keepers {
		if err := keep(c); err != nil {
			return err
		}
	}
	if err := r.app.Delete(record); err != nil {
		return err
	}
	r.changed(c)
	return nil
}

// Secret is a secret a connection keeps that no field asks for: a token Foyer
// made. Empty when there is none.
func (r *Registry) Secret(id, name string) (string, error) {
	record, _, err := r.find(id)
	if err != nil {
		return "", err
	}
	secrets := map[string]any{}
	_ = record.UnmarshalJSONField("secrets", &secrets)
	v, _ := secrets[name].(string)
	return v, nil
}

// SetSecret keeps such a secret on a connection.
func (r *Registry) SetSecret(id, name, value string) error {
	record, _, err := r.find(id)
	if err != nil {
		return err
	}
	secrets := map[string]any{}
	_ = record.UnmarshalJSONField("secrets", &secrets)
	secrets[name] = value
	record.Set("secrets", secrets)
	return r.app.Save(record)
}

// Test runs an adapter's test on what the page sent, laid over a saved
// connection's values — or over the adapter's defaults, for one not yet
// made — and keeps the result beside a saved connection's settings.
func (r *Registry) Test(ctx context.Context, key, id string, sent map[string]any) (Result, error) {
	a, ok := r.Adapter(key)
	var record *core.Record
	if id != "" {
		found, adapter, err := r.find(id)
		if err != nil {
			return Result{}, err
		}
		record, a, ok = found, adapter, true
	}
	if !ok {
		return Result{}, fmt.Errorf("no adapter %q", key)
	}
	if a.Planned {
		return Result{Message: a.Name + ": Foyer cannot connect to it yet."}, nil
	}
	s, err := a.merge(record, sent)
	if err != nil {
		return Result{}, err
	}
	result := Result{OK: false, Message: "This adapter has nothing to test."}
	if a.Test != nil {
		result = a.Test(ctx, s)
	}
	if record != nil {
		record.Set("status", Status{Result: result, At: time.Now().UTC()})
		if err := r.app.Save(record); err != nil {
			return result, err
		}
	}
	return result, nil
}

// check validates one value sent for the field, and answers it in the
// field's own type.
func (f Field) check(v any) (any, error) {
	switch f.Type {
	case Bool:
		b, ok := v.(bool)
		if !ok {
			return nil, validation.NewError("validation_invalid_bool", "Must be on or off.")
		}
		return b, nil
	case Number:
		n, ok := v.(float64)
		if !ok || n != float64(int(n)) {
			return nil, validation.NewError("validation_invalid_number", "Must be a whole number.")
		}
		if (f.Min != 0 || f.Max != 0) && (int(n) < f.Min || int(n) > f.Max) {
			return nil, validation.NewError("validation_out_of_range", fmt.Sprintf("Must be from %d to %d.", f.Min, f.Max))
		}
		return int(n), nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, validation.NewError("validation_invalid_text", "Must be text.")
	}
	s = strings.TrimSpace(s)
	if len(s) > 2048 {
		return nil, validation.NewError("validation_max_text_constraint", "Must be no more than 2048 characters.")
	}
	switch f.Type {
	case URL:
		if s == "" {
			return s, nil
		}
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, validation.NewError("validation_invalid_url", "Must be an http or https address.")
		}
		return strings.TrimRight(s, "/"), nil
	case Select:
		if s != "" && !slices.ContainsFunc(f.Options, func(o Option) bool { return o.Value == s }) {
			return nil, validation.NewError("validation_invalid_value", "Must be one of the choices.")
		}
	case Text:
		if s != "" && f.Pattern != "" && !matches(f.Pattern, s) {
			return nil, validation.NewError("validation_invalid_format", "Invalid value format.")
		}
	}
	return s, nil
}

var patterns sync.Map

// matches reports whether s matches the field's pattern, compiled once.
func matches(pattern, s string) bool {
	compiled, ok := patterns.Load(pattern)
	if !ok {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false
		}
		compiled, _ = patterns.LoadOrStore(pattern, re)
	}
	return compiled.(*regexp.Regexp).MatchString(s)
}
