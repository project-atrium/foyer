package iptv

import (
	"testing"
	"time"

	"foyer/internal/adapters"
)

// Requests at once is a whole number held to 1–4. One kept as text from when
// it was a choice ("2") was not migrated: it is one.
func TestRequestsAtOnceIsAWholeNumberFromOneToFour(t *testing.T) {
	for value, want := range map[any]int{1: 1, 2: 2, 4: 4, float64(3): 3, 0: 1, -2: 1, 9: 4, "2": 1, "4": 1, nil: 1} {
		if got := requestsAtOnce(adapters.Settings{Values: map[string]any{"requestsAtOnce": value}}); got != want {
			t.Errorf("%#v: %d, want %d", value, got, want)
		}
	}
	if got := requestsAtOnce(adapters.Settings{Values: map[string]any{}}); got != 1 {
		t.Errorf("none: %d", got)
	}
}

// Every guide time zone the app offers is one Go can read a guide in.
func TestEveryTimeZoneOfTheAppsIsOneGoKnows(t *testing.T) {
	if len(timeZones) < 400 || timeZones[0].Value != "" {
		t.Fatalf("%d time zones, the first %q", len(timeZones), timeZones[0].Value)
	}
	for _, zone := range timeZones[1:] {
		if _, err := time.LoadLocation(zone.Value); err != nil {
			t.Errorf("%s: %v", zone.Value, err)
		}
	}
}
