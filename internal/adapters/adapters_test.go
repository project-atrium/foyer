package adapters

import (
	"strings"
	"testing"
)

// A value kept before its field changed type is not migrated: read back, it
// is no value, and the field is its default.
func TestAValueKeptBeforeItsFieldChangedTypeIsNone(t *testing.T) {
	number := Field{Key: "requestsAtOnce", Type: Number, Default: 1, Min: new(1), Max: new(4)}
	zone := Field{Key: "timeZone", Type: Select, Options: []Option{{Value: "", Label: "As the portal says"}, {Value: "Europe/Berlin", Label: "Europe/Berlin"}}}
	cases := []struct {
		field Field
		value any
		want  any
		fits  bool
	}{
		{number, float64(2), 2, true},
		{number, "2", nil, false},
		{number, 2.5, 2, false},
		{number, true, nil, false},
		{zone, "Europe/Berlin", "Europe/Berlin", true},
		{zone, "", "", true},
		{zone, "Europe/Atlantis", "Europe/Atlantis", false},
		{zone, float64(1), "", false},
		{Field{Key: "forced", Type: Bool}, "true", false, false},
		{Field{Key: "forced", Type: Bool}, true, true, true},
		{Field{Key: "serverUrl", Type: URL}, "http://zurg:9999/dav", "http://zurg:9999/dav", true},
		{Field{Key: "serverUrl", Type: URL}, map[string]any{}, "", false},
	}
	for _, c := range cases {
		got, fits := normalise(c.field, c.value)
		if fits != c.fits || (fits && got != c.want) {
			t.Errorf("%s %v: %v, %v; want %v, %v", c.field.Key, c.value, got, fits, c.want, c.fits)
		}
	}
}

// A bound of 0 is a bound, and no bound is none: Min and Max are told apart
// from 0 by being there.
func TestANumbersBoundsAreThereOrNot(t *testing.T) {
	room := Field{Key: "copies_room_gb", Type: Number, Min: new(0), Max: new(100000)}
	if _, err := room.check(float64(-1)); err == nil || !strings.Contains(err.Error(), "from 0 to 100000") {
		t.Errorf("below a min of 0: %v", err)
	}
	if v, err := room.check(float64(0)); err != nil || v != 0 {
		t.Errorf("0 within a min of 0: %v %v", v, err)
	}
	free := Field{Key: "n", Type: Number}
	if v, err := free.check(float64(-7)); err != nil || v != -7 {
		t.Errorf("no bounds: %v %v", v, err)
	}
	floor := Field{Key: "n", Type: Number, Min: new(1)}
	if _, err := floor.check(float64(0)); err == nil || !strings.Contains(err.Error(), "1 or more") {
		t.Errorf("a min alone: %v", err)
	}
	ceiling := Field{Key: "n", Type: Number, Max: new(0)}
	if _, err := ceiling.check(float64(1)); err == nil || !strings.Contains(err.Error(), "0 or less") {
		t.Errorf("a max of 0 alone: %v", err)
	}
	if _, err := (Field{Key: "requestsAtOnce", Type: Number, Min: new(1), Max: new(4)}).check(2.5); err == nil {
		t.Error("a number that is not whole was taken")
	}
}

// A connection is set up once every field it requires holds something.
func TestSetUpIsEveryRequiredFieldFilled(t *testing.T) {
	a := Adapter{Fields: []Field{{Key: "serverUrl", Type: URL, Required: true}, {Key: "username", Type: Text}}}
	if a.SetUp(Settings{Values: map[string]any{"username": "a"}}) {
		t.Error("set up without its address")
	}
	if a.SetUp(Settings{Values: map[string]any{"serverUrl": ""}}) {
		t.Error("set up with an empty address")
	}
	if !a.SetUp(Settings{Values: map[string]any{"serverUrl": "http://zurg:9999/dav"}}) {
		t.Error("not set up with its address")
	}
}
