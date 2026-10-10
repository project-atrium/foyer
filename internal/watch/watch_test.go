package watch

import (
	"testing"
	"time"
)

func TestTheRulesLogeKeeps(t *testing.T) {
	now := time.Date(2026, 10, 10, 20, 0, 0, 0, time.UTC)
	hour := int64(3_600_000)
	for _, c := range []struct {
		name   string
		start  State
		report Report
		want   State
	}{
		{"a first progress", State{}, Report{Kind: "progress", PositionMs: 600_000, DurationMs: hour},
			State{PositionMs: 600_000, DurationMs: hour, LastPlayedAt: now}},
		{"a report at 0 never lowers it", State{PositionMs: 600_000, DurationMs: hour}, Report{Kind: "stopped", PositionMs: 0, DurationMs: hour},
			State{PositionMs: 600_000, DurationMs: hour, LastPlayedAt: now}},
		{"a stop short of 90 %", State{DurationMs: hour}, Report{Kind: "stopped", PositionMs: hour * 89 / 100},
			State{PositionMs: hour * 89 / 100, DurationMs: hour, LastPlayedAt: now}},
		{"a stop at 90 % is watched", State{DurationMs: hour}, Report{Kind: "stopped", PositionMs: hour * 90 / 100, Session: "a"},
			State{Watched: true, PlayCount: 1, DurationMs: hour, LastPlayedAt: now, CompletedSession: "a"}},
		{"the end is watched", State{}, Report{Kind: "end", PositionMs: 10, Session: "b"},
			State{Watched: true, PlayCount: 1, LastPlayedAt: now, CompletedSession: "b"}},
		{"a playing's end sent twice counts once", State{Watched: true, PlayCount: 1, CompletedSession: "b"}, Report{Kind: "end", Session: "b"},
			State{Watched: true, PlayCount: 1, LastPlayedAt: now, CompletedSession: "b"}},
		{"watched holds while it is played again", State{Watched: true, PlayCount: 1}, Report{Kind: "progress", PositionMs: 120_000, DurationMs: hour},
			State{Watched: true, PlayCount: 1, PositionMs: 120_000, DurationMs: hour, LastPlayedAt: now}},
	} {
		if got := Apply(c.start, c.report, now); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	if got := Mark(State{PositionMs: 5_000}, true, now); !got.Watched || got.PlayCount != 1 || got.PositionMs != 0 {
		t.Errorf("marked watched: %+v", got)
	}
	if got := Mark(State{Watched: true, PlayCount: 2, PositionMs: 5_000}, false, now); got.Watched || got.PlayCount != 2 || got.PositionMs != 0 {
		t.Errorf("marked unwatched: %+v", got)
	}
}
