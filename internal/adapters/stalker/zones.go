package stalker

import (
	"sync"
	"time"

	// Every zone in the binary: a guide is read the same on any host.
	_ "time/tzdata"
)

var locations sync.Map // zone → *time.Location, or nil where none is known

func location(zone string) (*time.Location, bool) {
	if v, ok := locations.Load(zone); ok {
		loc, _ := v.(*time.Location)
		return loc, loc != nil
	}
	loc, err := time.LoadLocation(zone)
	if err != nil || zone == "" || zone == "Local" {
		locations.Store(zone, (*time.Location)(nil))
		return nil, false
	}
	locations.Store(zone, loc)
	return loc, true
}

// ZoneOffset is how far a zone's wall clock is ahead of UTC at an instant,
// to the second; false where the zone is none Go knows.
func ZoneOffset(zone string, at time.Time) (time.Duration, bool) {
	loc, ok := location(zone)
	if !ok {
		return 0, false
	}
	_, seconds := at.In(loc).Zone()
	return time.Duration(seconds) * time.Second, true
}

// FromZoneWallClock is the instant a time really is that was stamped as UTC
// but is the zone's wall clock — 20:15 in Berlin, sent as 20:15Z. Asked
// twice, the second time at the first answer, so the nights summer time
// begins and ends land right. Unchanged where the zone is none Go knows.
func FromZoneWallClock(stamped time.Time, zone string) time.Time {
	first, ok := ZoneOffset(zone, stamped)
	if !ok {
		return stamped
	}
	second, _ := ZoneOffset(zone, stamped.Add(-first))
	return stamped.Add(-second)
}
