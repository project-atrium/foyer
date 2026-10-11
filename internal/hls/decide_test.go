package hls

import (
	"slices"
	"testing"

	"foyer/internal/fixtures"
	"foyer/internal/media"
)

// The app's table of what play answers, for a player's profile and a file:
// Foyer decides each case as the app's own tests expect, and those hold each
// answer to the app's canPlay.
func TestPlayDecidesAsTheAppExpects(t *testing.T) {
	var table struct {
		Profiles map[string]Profile `json:"profiles"`
		Cases    []struct {
			Name        string        `json:"name"`
			Profile     string        `json:"profile"`
			Version     media.Version `json:"version"`
			Indexed     bool          `json:"indexed"`
			DolbyVision int           `json:"dolbyVision"`
			Expect      struct {
				Mode  string `json:"mode"`
				Audio []struct {
					Codec string `json:"codec"`
					Copy  bool   `json:"copy"`
				} `json:"audio"`
				Subtitles []int `json:"subtitles"`
			} `json:"expect"`
		} `json:"cases"`
	}
	fixtures.Load(t, "decisions.json", &table)
	if len(table.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range table.Cases {
		t.Run(c.Name, func(t *testing.T) {
			profile, ok := table.Profiles[c.Profile]
			if !ok {
				t.Fatalf("no profile %s", c.Profile)
			}
			var idx *Index
			if c.Indexed {
				idx = &Index{Keyframes: []int64{0, 2002}, DurationMs: 6_000_000, Video: Video{Codec: "hvc1.2.4.L150.90", DolbyVision: c.DolbyVision}}
			}
			got := Decide(c.Version, profile, idx)
			if got.Mode != c.Expect.Mode {
				t.Fatalf("mode %s, the app expects %s", got.Mode, c.Expect.Mode)
			}
			if got.Mode != Remux {
				return
			}
			if len(got.Plan.Audio) != len(c.Expect.Audio) {
				t.Fatalf("audio %+v, the app expects %+v", got.Plan.Audio, c.Expect.Audio)
			}
			for i, want := range c.Expect.Audio {
				if a := got.Plan.Audio[i]; a.Track != i || a.Codec != want.Codec || a.Copy != want.Copy {
					t.Errorf("rendition %d: %+v, the app expects %+v", i, a, want)
				}
			}
			if !slices.Equal(got.Plan.Subtitles, c.Expect.Subtitles) {
				t.Errorf("subtitles %v, the app expects %v", got.Plan.Subtitles, c.Expect.Subtitles)
			}
		})
	}
}

// A sound made anew is never more than an encoder here makes; one copied
// keeps what it has.
func TestASoundMadeAnewIsAtMostFivePointOne(t *testing.T) {
	profile := Profile{Protocols: []string{"hls"}, VideoCodecs: []string{"hevc"}, AudioCodecs: []string{"eac3"}}
	v := media.Version{Container: "mkv", Video: &media.Video{Codec: "hevc", Height: 2160},
		Audio: []media.Audio{{Codec: "truehd", Channels: 8}, {Codec: "eac3", Channels: 6}, {Codec: "dts"}}}
	got := Decide(v, profile, &Index{Keyframes: []int64{0}, Video: Video{Codec: "hvc1.2.4.L150.90"}})
	want := []AudioPlan{{Track: 0, Codec: "eac3", Channels: 6}, {Track: 1, Codec: "eac3", Copy: true, Channels: 6}, {Track: 2, Codec: "eac3"}}
	if got.Mode != Remux || !slices.Equal(got.Plan.Audio, want) {
		t.Fatalf("%s %+v", got.Mode, got.Plan.Audio)
	}
}
