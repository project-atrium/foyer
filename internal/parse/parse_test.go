package parse

import (
	"slices"
	"strings"
	"testing"

	"foyer/internal/titles"
)

// check compares what a test cares about: the title, the year and the
// numbers always — an empty one must be empty — and the rest where the test
// names it.
func check(t *testing.T, input string, got, want Release) {
	t.Helper()
	if got.Title != want.Title || got.Year != want.Year {
		t.Errorf("%q: title %q year %d, want %q %d", input, got.Title, got.Year, want.Title, want.Year)
	}
	for _, c := range []struct {
		name      string
		got, want []int
	}{{"seasons", got.Seasons, want.Seasons}, {"episodes", got.Episodes, want.Episodes}, {"absolute", got.Absolute, want.Absolute}} {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%q: %s %v, want %v", input, c.name, c.got, c.want)
		}
	}
	strs := []struct{ name, got, want string }{
		{"episode title", got.EpisodeTitle, want.EpisodeTitle},
		{"special", got.Special, want.Special},
		{"source", got.Source, want.Source},
		{"codec", got.VideoCodec, want.VideoCodec},
		{"channels", got.Channels, want.Channels},
		{"edition", got.Edition, want.Edition},
		{"group", got.Group, want.Group},
		{"extra", got.Extra, want.Extra},
		{"alternate", got.Alternate, want.Alternate},
		{"air date", got.AirDate, want.AirDate},
		{"crc", got.CRC, want.CRC},
	}
	for _, c := range strs {
		if c.want != "" && c.got != c.want {
			t.Errorf("%q: %s %q, want %q", input, c.name, c.got, c.want)
		}
	}
	if want.Resolution != 0 && got.Resolution != want.Resolution {
		t.Errorf("%q: resolution %d, want %d", input, got.Resolution, want.Resolution)
	}
	if want.MovieNumber != 0 && got.MovieNumber != want.MovieNumber {
		t.Errorf("%q: movie %d, want %d", input, got.MovieNumber, want.MovieNumber)
	}
	if want.Pack != [2]int{} && got.Pack != want.Pack {
		t.Errorf("%q: pack %v, want %v", input, got.Pack, want.Pack)
	}
	if want.HDR != nil && !sameSet(got.HDR, want.HDR) {
		t.Errorf("%q: hdr %v, want %v", input, got.HDR, want.HDR)
	}
	if want.Audio != nil && !sameSet(got.Audio, want.Audio) {
		t.Errorf("%q: audio %v, want %v", input, got.Audio, want.Audio)
	}
	if want.Repack && !got.Repack || want.Remux && !got.Remux || want.Sample && !got.Sample ||
		want.Upscaled && !got.Upscaled || want.DualAudio && !got.DualAudio || want.MultiSub && !got.MultiSub {
		t.Errorf("%q: flags %+v", input, got)
	}
	if want.Anime != 0 && got.Anime < want.Anime {
		t.Errorf("%q: anime %d, want at least %d", input, got.Anime, want.Anime)
	}
	if want.BitDepth != 0 && got.BitDepth != want.BitDepth {
		t.Errorf("%q: bit depth %d, want %d", input, got.BitDepth, want.BitDepth)
	}
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func TestNames(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Release
	}{
		// Scene names, dotted.
		{"86.Eighty.Six.S01E01.REPACK.1080p.Blu-ray.Opus2.0.x265-koala.mkv", Release{
			Title: "86 Eighty Six", Seasons: []int{1}, Episodes: []int{1}, Repack: true, Resolution: 1080,
			Source: "bluray", VideoCodec: "hevc", Audio: []string{"opus"}, Channels: "2.0", Group: "koala"}},
		{"86.Eighty.Six.S01.REPACK.1080p.Blu-ray.Opus2.0.x265-koala", Release{Title: "86 Eighty Six", Seasons: []int{1}}},
		{"A.Man.Called.Otto.2022.1080p.BluRay.REMUX.AVC.DTS-HD.MA.5.1-FGT.mkv", Release{
			Title: "A Man Called Otto", Year: 2022, Remux: true, Source: "bluray", VideoCodec: "h264",
			Audio: []string{"dts-hd ma"}, Channels: "5.1", Group: "FGT"}},
		{"1917.2019.1080p.BluRay.x264-SPARKS.mkv", Release{Title: "1917", Year: 2019}},
		{"1917.1080p.BluRay.x264.mkv", Release{Title: "1917"}},
		{"2001.A.Space.Odyssey.1968.2160p.UHD.BluRay.x265.HDR.mkv", Release{Title: "2001 A Space Odyssey", Year: 1968, Resolution: 2160, HDR: []string{"hdr10"}}},
		{"2001 A Space Odyssey (1968).mkv", Release{Title: "2001 A Space Odyssey", Year: 1968}},
		{"Blade.Runner.2049.2017.1080p.WEB-DL.DD5.1.H.264-FGT.mkv", Release{
			Title: "Blade Runner 2049", Year: 2017, Source: "web-dl", Audio: []string{"ac3"}, Channels: "5.1", VideoCodec: "h264", Group: "FGT"}},
		{"Blade Runner 2049.mkv", Release{Title: "Blade Runner 2049"}},
		{"Wonder.Woman.1984.2020.2160p.HMAX.WEB-DL.DDP5.1.Atmos.DV.HDR10.HEVC-FLUX.mkv", Release{
			Title: "Wonder Woman 1984", Year: 2020, Audio: []string{"eac3", "atmos"}, HDR: []string{"dolby-vision", "hdr10"}, Group: "FLUX"}},
		{"Class.of.1984.1080p.BluRay.x264.mkv", Release{Title: "Class of", Year: 1984, Alternate: "Class of 1984"}},
		{"Doctor.Who.2005.S01E01.Rose.1080p.mkv", Release{Title: "Doctor Who", Year: 2005, Seasons: []int{1}, Episodes: []int{1}, EpisodeTitle: "Rose"}},
		{"The.4400.S01E01.mkv", Release{Title: "The 4400", Seasons: []int{1}, Episodes: []int{1}}},
		{"S.W.A.T.2017.S01E01.720p.mkv", Release{Title: "S.W.A.T.", Year: 2017, Seasons: []int{1}, Episodes: []int{1}}},
		{"Se7en.1995.REMASTERED.1080p.mkv", Release{Title: "Se7en", Year: 1995, Edition: "Remastered"}},
		{"Spider-Man.No.Way.Home.2021.mkv", Release{Title: "Spider-Man No Way Home", Year: 2021}},
		{"Dune.Part.Two.2024.2160p.mkv", Release{Title: "Dune Part Two", Year: 2024}},
		{"Show.Name.1x02.HDTV.x264-LOL.mkv", Release{Title: "Show Name", Seasons: []int{1}, Episodes: []int{2}, Source: "hdtv", Group: "LOL"}},
		{"Show.Name.S01E01-E03.720p.mkv", Release{Title: "Show Name", Seasons: []int{1}, Episodes: []int{1, 2, 3}}},
		{"Show.Name.S01E01E02.720p.mkv", Release{Title: "Show Name", Seasons: []int{1}, Episodes: []int{1, 2}}},
		{"Show.Name.S01E01-03.720p.mkv", Release{Title: "Show Name", Seasons: []int{1}, Episodes: []int{1, 2, 3}}},
		{"Show.Name.S01-S03.1080p", Release{Title: "Show Name", Seasons: []int{1, 2, 3}}},
		{"Show Name Season 1-3 Complete", Release{Title: "Show Name", Seasons: []int{1, 2, 3}}},
		{"Show.Name.S00E01.Special.mkv", Release{Title: "Show Name", Seasons: []int{0}, Episodes: []int{1}}},
		{"Show.Name.2024.05.14.Guest.1080p.WEB.h264-GRP.mkv", Release{Title: "Show Name", AirDate: "2024-05-14"}},
		{"The.Office.US.S01E01.720p.mkv", Release{Title: "The Office US", Seasons: []int{1}, Episodes: []int{1}, Alternate: "The Office"}},
		{"Movie.2019.1080p-GRP-sample.mkv", Release{Title: "Movie", Year: 2019, Sample: true}},
		{"Movie Name (2019) [2160p] [DV HDR10+] [TrueHD 7.1 Atmos].mkv", Release{
			Title: "Movie Name", Year: 2019, Resolution: 2160, HDR: []string{"dolby-vision", "hdr10+"}, Audio: []string{"truehd", "atmos"}, Channels: "7.1"}},
		{"Movie.Name.2019.Directors.Cut.1080p.BluRay.x264.mkv", Release{Title: "Movie Name", Year: 2019, Edition: "Director's Cut"}},
		{"Movie.Name.2019.1080p.BluRay.x264-trailer.mkv", Release{Title: "Movie Name", Year: 2019, Extra: "trailer"}},

		// Names with words and dashes.
		{"One Punch Man - S03E11 - Top Dragons - 2160p HDR Ai Upscale -Mesc.mkv", Release{
			Title: "One Punch Man", Seasons: []int{3}, Episodes: []int{11}, EpisodeTitle: "Top Dragons", Resolution: 2160,
			HDR: []string{"hdr10"}, Upscaled: true, Group: "Mesc"}},
		{"One_Punch_Man_-_S03E11_-_Top_Dragons.mkv", Release{Title: "One Punch Man", Seasons: []int{3}, Episodes: []int{11}, EpisodeTitle: "Top Dragons"}},

		// Anime.
		{"[N0handles] Blue Giant [Eng Sub].mkv", Release{Title: "Blue Giant", Group: "N0handles", Anime: 1}},
		{"[Anime Time] One Piece - 0001 - I'm Luffy! The Man Who's Gonna Be King of the Pirates!.mkv", Release{
			Title: "One Piece", Absolute: []int{1}, EpisodeTitle: "I'm Luffy! The Man Who's Gonna Be King of the Pirates!", Group: "Anime Time", Anime: 2}},
		{"[Anime Time] One Piece (0001-1071+Movies+Specials) [BD+CR] [Dual Audio] [1080p][HEVC 10bit x265][AAC][Multi Sub]", Release{
			Title: "One Piece", Pack: [2]int{1, 1071}, Source: "bluray", DualAudio: true, MultiSub: true, VideoCodec: "hevc", BitDepth: 10,
			Resolution: 1080, Audio: []string{"aac"}, Anime: 4}},
		{"[Anime Time] One Piece OVA - Defeat Him! The Pirate Ganzack!.mkv", Release{Title: "One Piece", Special: "ova", EpisodeTitle: "Defeat Him! The Pirate Ganzack!"}},
		{"[Anime Time] One Piece Special - Episode of Nami.mkv", Release{Title: "One Piece", Special: "special", EpisodeTitle: "Episode of Nami"}},
		{"[Anime Time] One Piece Movie 01 (2000) - One Piece the Movie.mkv", Release{Title: "One Piece", Year: 2000, MovieNumber: 1, EpisodeTitle: "One Piece the Movie"}},
		{"[SubsPlease] Sousou no Frieren - 01 (1080p) [A1B2C3D4].mkv", Release{Title: "Sousou no Frieren", Absolute: []int{1}, Resolution: 1080, CRC: "A1B2C3D4"}},
		{"[G] Title - 01v2 [1080p].mkv", Release{Title: "Title", Absolute: []int{1}, Repack: true}},
		{"[G] Title - 12-13 [1080p].mkv", Release{Title: "Title", Absolute: []int{12, 13}}},
		{"[G] Title S2 - 05 [1080p].mkv", Release{Title: "Title", Seasons: []int{2}, Episodes: []int{5}}},
		{"[G] Title 2nd Season - 05.mkv", Release{Title: "Title", Seasons: []int{2}, Episodes: []int{5}}},
		{"[G] Title - 13.5 [1080p].mkv", Release{Title: "Title", Special: "recap"}},
		{"[G] Title (2019) - 05 [1080p].mkv", Release{Title: "Title", Year: 2019, Absolute: []int{5}}},
		{"86 - 01 [1080p].mkv", Release{Title: "86", Absolute: []int{1}}},
		{"Episode 001-206 Uncropped (1080p Upscale)", Release{Absolute: []int{1}, Edition: "Uncropped", Resolution: 1080, Upscaled: true}},
		{"Season 01 - East Blue", Release{Seasons: []int{1}}},
	} {
		check(t, c.in, Name(c.in), c.want)
	}
}

func TestPaths(t *testing.T) {
	for _, c := range []struct {
		path     string
		siblings []string
		want     Release
		folder   Folder
	}{
		// Zurg adds " (id)" where two files of a torrent share a name.
		{"[Anime Time] One Piece (0001-1071+Movies+Specials) [BD+CR] [Dual Audio] [1080p][HEVC 10bit x265][AAC][Multi Sub]/Episode 001-206 Uncropped (480p)/[Anime Time] One Piece - 0002 - Enter the Great Swordsman (28).mkv",
			[]string{"[Anime Time] One Piece - 0002 - Enter the Great Swordsman (234).mkv"},
			Release{Title: "One Piece", Absolute: []int{2}, EpisodeTitle: "Enter the Great Swordsman", Resolution: 480, Edition: "Uncropped"}, Plain},
		// A season folder never takes a number counted from the start.
		{"[Anime Time] One Piece (0001-1071)/Season 01 - East Blue/[Anime Time] One Piece - 0062 - Some Title.mkv", nil,
			Release{Title: "One Piece", Absolute: []int{62}, EpisodeTitle: "Some Title"}, Seasonal},
		{"[Anime Time] One Piece (0001-1071)/[Anime Time] One Piece Specials/[Anime Time] One Piece Special - Episode of Nami.mkv", nil,
			Release{Title: "One Piece", Special: "special", EpisodeTitle: "Episode of Nami"}, Specials},
		{"[Anime Time] One Piece (0001-1071)/[Anime Time] One Piece Movies/[Anime Time] One Piece Movie 01 (2000) - One Piece the Movie.mkv", nil,
			Release{Title: "One Piece", Year: 2000, MovieNumber: 1, EpisodeTitle: "One Piece the Movie"}, Movies},
		{"Show Name (2010)/Season 1/Episode 1.mkv", nil, Release{Title: "Show Name", Year: 2010, Seasons: []int{1}, Episodes: []int{1}}, Seasonal},
		{"Show Name/Season 01/05 - Pilot.mkv", nil, Release{Title: "Show Name", Seasons: []int{1}, Episodes: []int{5}, EpisodeTitle: "Pilot"}, Seasonal},
		{"Movie.2019.1080p/Featurettes/Making Of.mkv", nil, Release{Title: "Making Of", Year: 2019, Extra: "extra"}, Extras},
		{"Movie.2019.1080p/Sample/x.mkv", nil, Release{Title: "x", Year: 2019, Sample: true}, Samples},
		{"86.Eighty.Six.S01.REPACK.1080p.Blu-ray.Opus2.0.x265-koala/86.Eighty.Six.S01E02.REPACK.1080p.Blu-ray.Opus2.0.x265-koala.mkv", nil,
			Release{Title: "86 Eighty Six", Seasons: []int{1}, Episodes: []int{2}}, Plain},
		// A lone file in a folder of its own: the torrent is the film.
		{"A.Man.Called.Otto.2022.1080p.BluRay.REMUX.AVC.DTS-HD.MA.5.1-FGT/A.Man.Called.Otto.2022.1080p.BluRay.REMUX.AVC.DTS-HD.MA.5.1-FGT.mkv", nil,
			Release{Title: "A Man Called Otto", Year: 2022, Remux: true}, Plain},
		{"loose.file.2015.mkv", nil, Release{Title: "loose file", Year: 2015}, Plain},
	} {
		got := Path(c.path, c.siblings)
		check(t, c.path, got.Release, c.want)
		if got.Folder != c.folder {
			t.Errorf("%q: folder %q, want %q", c.path, got.Folder, c.folder)
		}
	}
}

func TestStripCollision(t *testing.T) {
	for _, c := range []struct {
		name     string
		siblings []string
		want     string
	}{
		{"Episode - 0001 (233).mkv", []string{"Episode - 0001 (27).mkv"}, "Episode - 0001.mkv"},
		{"Episode - 0001 (233).mkv", nil, "Episode - 0001.mkv"},
		{"Movie Name (2019).mkv", nil, "Movie Name (2019).mkv"},
		{"Movie Name (2019).mkv", []string{"Movie Name (2020).mkv"}, "Movie Name.mkv"},
		{"Plain.mkv", nil, "Plain.mkv"},
	} {
		if got := StripCollision(c.name, c.siblings); got != c.want {
			t.Errorf("StripCollision(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestIsVideo(t *testing.T) {
	for name, want := range map[string]bool{"a.mkv": true, "a.MP4": true, "a.m2ts": true, "a.srt": false, "a.nfo": false, "a.iso": false} {
		if IsVideo(name) != want {
			t.Errorf("IsVideo(%q) = %v", name, !want)
		}
	}
}

// A name is whatever its uploader typed: reading one never panics, and never
// makes a title longer than the name.
func FuzzName(f *testing.F) {
	for _, seed := range []string{"86.Eighty.Six.S01E01.mkv", "[G] T - 01v2 [1080p].mkv", "(", "[", " - - ", "S1E", "2001 2002 2003"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		r := Name(name)
		if len(r.Title) > len(name)+8 {
			t.Fatalf("%q: title %q", name, r.Title)
		}
		if key := titles.Key(r.Title); titles.Key(key) != key {
			t.Fatalf("%q: key %q", name, key)
		}
		_ = strings.TrimSpace(r.EpisodeTitle)
	})
}
