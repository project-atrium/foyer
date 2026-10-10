// Package adapters keeps what Foyer connects to — Zurg, TMDB, ffmpeg — and
// how the household set each up: one record per adapter in `adapters`, edited
// on the dashboard's Adapters page and nowhere else.
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

// Collection holds one record per adapter that has been saved.
const Collection = "adapters"

// Masked stands for a secret that is set, wherever one is sent to the page.
const Masked = "******"

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
	Help    string `json:"help,omitempty"`
}

// Result is what a test of an adapter found. A provider's refusal is a
// result, not an error: the page shows it beside the form.
type Result struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

type Adapter struct {
	Key         string
	Name        string
	Description string
	Fields      []Field
	// Notice is what the service's terms ask to be shown wherever it is used.
	Notice string
	// Test tries the settings once. It never retries a refused sign-in.
	Test func(ctx context.Context, s Settings) Result
}

// Settings are an adapter's values as saved, its defaults filled in and its
// secrets in place.
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

// Registry is every adapter this build has, and the one way to read or
// change their settings.
type Registry struct {
	app      core.App
	adapters []Adapter
	mu       sync.Mutex
	watchers []func(key string)
}

func NewRegistry(app core.App, adapters ...Adapter) *Registry {
	return &Registry{app: app, adapters: adapters}
}

func (r *Registry) Adapter(key string) (Adapter, bool) {
	i := slices.IndexFunc(r.adapters, func(a Adapter) bool { return a.Key == key })
	if i < 0 {
		return Adapter{}, false
	}
	return r.adapters[i], true
}

// OnChange calls back after an adapter's settings were saved, so whatever runs
// on them can start again with the new ones.
func (r *Registry) OnChange(fn func(key string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.watchers = append(r.watchers, fn)
}

// Settings are the adapter's saved values over its defaults: off, with the
// defaults, until it is first saved.
func (r *Registry) Settings(key string) (Settings, error) {
	a, ok := r.Adapter(key)
	if !ok {
		return Settings{}, fmt.Errorf("no adapter %q", key)
	}
	record, err := r.record(key)
	if err != nil {
		return Settings{}, err
	}
	return a.settings(record), nil
}

func (r *Registry) record(key string) (*core.Record, error) {
	record, err := r.app.FindFirstRecordByFilter(Collection, "key = {:key}", dbx.Params{"key": key})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return record, nil
}

func (a Adapter) settings(record *core.Record) Settings {
	s := Settings{Values: map[string]any{}}
	for _, f := range a.Fields {
		if f.Default != nil {
			s.Values[f.Key] = f.Default
		}
	}
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

// View is an adapter as the page sees it: its fields, and its values with
// every secret masked.
type View struct {
	Key         string         `json:"key"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Notice      string         `json:"notice,omitempty"`
	Enabled     bool           `json:"enabled"`
	Fields      []Field        `json:"fields"`
	Values      map[string]any `json:"values"`
	Status      *Status        `json:"status,omitempty"`
}

// Status is the last test's result, and when it ran.
type Status struct {
	Result
	At time.Time `json:"at"`
}

func (r *Registry) View(key string) (View, error) {
	a, ok := r.Adapter(key)
	if !ok {
		return View{}, fmt.Errorf("no adapter %q", key)
	}
	record, err := r.record(key)
	if err != nil {
		return View{}, err
	}
	s := a.settings(record)
	values := maps.Clone(s.Values)
	for _, f := range a.Fields {
		if f.Type != Password {
			continue
		}
		if v, _ := values[f.Key].(string); v != "" {
			values[f.Key] = Masked
		} else {
			values[f.Key] = ""
		}
	}
	view := View{Key: a.Key, Name: a.Name, Description: a.Description, Notice: a.Notice, Enabled: s.Enabled, Fields: a.Fields, Values: values}
	if record != nil {
		var status Status
		if err := record.UnmarshalJSONField("status", &status); err == nil && !status.At.IsZero() {
			view.Status = &status
		}
	}
	return view, nil
}

// Merge lays what the page sent over what is saved. A secret left out, or
// sent back as Masked, keeps its saved value; one sent empty is cleared.
// The answer is validated: field errors are keyed `values.<key>`, as the
// page's inputs are named.
func (r *Registry) Merge(key string, sent map[string]any) (Settings, error) {
	a, ok := r.Adapter(key)
	if !ok {
		return Settings{}, fmt.Errorf("no adapter %q", key)
	}
	record, err := r.record(key)
	if err != nil {
		return Settings{}, err
	}
	s := a.settings(record)
	errs := validation.Errors{}
	for _, f := range a.Fields {
		v, present := sent[f.Key]
		if f.Type == Password && (!present || v == Masked) {
			continue
		}
		if !present {
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

// Save stores the adapter's settings, merged as Merge does, and tells
// whoever watches.
func (r *Registry) Save(key string, enabled bool, sent map[string]any) (Settings, error) {
	a, _ := r.Adapter(key)
	s, err := r.Merge(key, sent)
	if err != nil {
		return s, err
	}
	s.Enabled = enabled
	record, err := r.record(key)
	if err != nil {
		return s, err
	}
	if record == nil {
		collection, err := r.app.FindCollectionByNameOrId(Collection)
		if err != nil {
			return s, err
		}
		record = core.NewRecord(collection)
		record.Set("key", key)
	}
	config, secrets := map[string]any{}, map[string]any{}
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
	record.Set("enabled", enabled)
	record.Set("config", config)
	record.Set("secrets", secrets)
	if err := r.app.Save(record); err != nil {
		return s, err
	}
	r.mu.Lock()
	watchers := slices.Clone(r.watchers)
	r.mu.Unlock()
	for _, fn := range watchers {
		fn(key)
	}
	return s, nil
}

// Test runs the adapter's test on what the page sent, laid over what is
// saved, and keeps the result beside the settings once they are saved.
func (r *Registry) Test(ctx context.Context, key string, sent map[string]any) (Result, error) {
	a, ok := r.Adapter(key)
	if !ok {
		return Result{}, fmt.Errorf("no adapter %q", key)
	}
	s, err := r.Merge(key, sent)
	if err != nil {
		return Result{}, err
	}
	result := Result{OK: false, Message: "This adapter has nothing to test."}
	if a.Test != nil {
		result = a.Test(ctx, s)
	}
	if record, err := r.record(key); err == nil && record != nil {
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
