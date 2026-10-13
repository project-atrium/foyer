package stalker

import (
	"maps"
	"testing"
	"time"

	"foyer/internal/fixtures"
)

// The app's answers, from the files both sides read: every country's zone,
// every case's country, every zone's offset and every stamp's instant.
func TestCountriesAndZonesAreTheApps(t *testing.T) {
	var countries struct {
		Zones map[string]string `json:"zones"`
		Cases []struct {
			GuideID string    `json:"guideId"`
			Names   []*string `json:"names"`
			Country *string   `json:"country"`
		} `json:"cases"`
	}
	fixtures.Load(t, "countries.json", &countries)
	if !maps.Equal(countries.Zones, countryZones) {
		t.Fatalf("the zones differ from the app's")
	}
	for country, zone := range countryZones {
		if _, ok := ZoneOffset(zone, time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)); !ok {
			t.Errorf("%s: Go knows no %s", country, zone)
		}
	}
	for _, c := range countries.Cases {
		names := make([]string, len(c.Names))
		for i, n := range c.Names {
			if n != nil {
				names[i] = *n
			}
		}
		want := ""
		if c.Country != nil {
			want = *c.Country
		}
		if got := CountryOf(c.GuideID, names...); got != want {
			t.Errorf("%q %q: %q, want %q", c.GuideID, names, got, want)
		}
	}

	var zones struct {
		Offsets []struct {
			Zone     string    `json:"zone"`
			At       time.Time `json:"at"`
			OffsetMs *int64    `json:"offsetMs"`
		} `json:"offsets"`
		WallClock []struct {
			Stamped time.Time `json:"stamped"`
			Zone    string    `json:"zone"`
			Instant time.Time `json:"instant"`
		} `json:"wallClock"`
	}
	fixtures.Load(t, "zones.json", &zones)
	for _, o := range zones.Offsets {
		got, ok := ZoneOffset(o.Zone, o.At)
		switch {
		case o.OffsetMs == nil && ok:
			t.Errorf("%s: an offset for a zone nobody knows", o.Zone)
		case o.OffsetMs != nil && (!ok || got.Milliseconds() != *o.OffsetMs):
			t.Errorf("%s at %v: %v", o.Zone, o.At, got)
		}
	}
	for _, w := range zones.WallClock {
		if got := FromZoneWallClock(w.Stamped, w.Zone); !got.Equal(w.Instant) {
			t.Errorf("%v in %s: %v, want %v", w.Stamped, w.Zone, got, w.Instant)
		}
	}
}
