package identity

import (
	"strings"
	"testing"

	"foyer/internal/fixtures"
)

// The vectors are Loge's: an identity hashed in the app and here must match.
func TestHashIsLoges(t *testing.T) {
	for identity, want := range map[string]string{
		"tmdb:movie:603":          "d536ee13b7c5c6a1",
		"youtube:dQw4w9WgXcQ":     "38c0617abe0dabb0",
		"tmdb:tv:1396":            "6ecb6b5dbc7cbfaf",
		"tmdb:tv:1396/s1e3":       "ee75ecaaf438fecc",
		"title:movie:matrix:1999": "200072289f3e38be",
	} {
		if got := Hash(identity); got != want {
			t.Errorf("Hash(%q) = %s, want %s", identity, got, want)
		}
	}
}

func TestIdentitiesAreLoges(t *testing.T) {
	for got, want := range map[string]string{
		Movie(603):                "tmdb:movie:603",
		Show(1396):                "tmdb:tv:1396",
		Episode(Show(1396), 1, 3): "tmdb:tv:1396/s1e3",
		ByTitle("movie", "Matrix Revolutions", 2003):         "title:movie:matrix revolutions:2003",
		ByTitle("movie", "Matrix (1999) DE 4K HDR", 0):       "title:movie:matrix:1999",
		ByTitle("show", "Breaking Bad", 2008):                "title:show:breaking bad:2008",
		ByTitle("show", "Брат", 1997):                        "",
		Episode(ByTitle("show", "Breaking Bad", 2008), 1, 3): "title:show:breaking bad:2008/s1e3",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// An extra is its title's, by what it is: a better copy keeps its status,
// another extra of another title never shares it.
func TestAnExtraIsItsTitles(t *testing.T) {
	red := Extra(Show(37854), "movie", "One Piece Film: Red", "Films/One.Piece.Film.Red.2022.1080p.mkv")
	if !strings.HasPrefix(red, "tmdb:tv:37854/extra:") || len(red) != len("tmdb:tv:37854/extra:")+16 {
		t.Fatalf("Extra = %q", red)
	}
	if again := Extra(Show(37854), "movie", "One Piece Film - RED", "Films/One.Piece.Film.Red.2022.2160p.mkv"); again != red {
		t.Errorf("a better copy is another extra: %q", again)
	}
	if other := Extra(Show(1), "movie", "One Piece Film: Red", ""); other == red {
		t.Error("two titles share an extra")
	}
	if ova := Extra(Show(37854), "ova", "One Piece Film: Red", ""); ova == red {
		t.Error("an OVA and a film of one name are one")
	}
	if byFile := Extra(Show(37854), "extra", "", "Extras/Making Of.mkv"); byFile != Extra(Show(37854), "extra", "making of", "") {
		t.Errorf("an extra with no name is not known by its file's: %q", byFile)
	}
	if Extra("", "movie", "One Piece Film: Red", "") != "" {
		t.Error("an extra of a title with no identity has one")
	}
}

// The app's own vectors, read from the file its tests read.
func TestTheAppsIdentities(t *testing.T) {
	var shared struct {
		Hashes []struct {
			Identity string `json:"identity"`
			Hash     string `json:"hash"`
		}
		Identities []struct {
			Kind     string `json:"kind"`
			TMDB     int    `json:"tmdb"`
			Title    string `json:"title"`
			Year     int    `json:"year"`
			Season   int    `json:"season"`
			Episode  int    `json:"episode"`
			Identity string `json:"identity"`
		}
	}
	fixtures.Load(t, "identity.json", &shared)
	for _, v := range shared.Hashes {
		if got := Hash(v.Identity); got != v.Hash {
			t.Errorf("Hash(%q) = %s; the app says %s", v.Identity, got, v.Hash)
		}
	}
	for _, v := range shared.Identities {
		var got string
		switch {
		case v.TMDB != 0 && v.Kind == "movie":
			got = Movie(v.TMDB)
		case v.TMDB != 0 && v.Kind == "show":
			got = Show(v.TMDB)
		case v.TMDB != 0:
			got = Episode(Show(v.TMDB), v.Season, v.Episode)
		case v.Kind == "episode":
			if show := ByTitle("show", v.Title, v.Year); show != "" {
				got = Episode(show, v.Season, v.Episode)
			}
		default:
			got = ByTitle(v.Kind, v.Title, v.Year)
		}
		if got != v.Identity {
			t.Errorf("%+v: %q; the app says %q", v, got, v.Identity)
		}
	}
	if len(shared.Hashes) == 0 || len(shared.Identities) == 0 {
		t.Fatal("the app's identities were not read")
	}
}
