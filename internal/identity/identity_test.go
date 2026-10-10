package identity

import "testing"

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
