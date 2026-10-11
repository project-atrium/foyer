package titles

import (
	"testing"

	"foyer/internal/fixtures"
)

// The cases are Loge's own (adapters/test/identity.test.ts): Foyer and the
// app must key a title alike, or one film's history splits in two.

func TestPlainTakesOffWhatAProviderMarksItsCopiesWith(t *testing.T) {
	for _, c := range []struct {
		name  string
		title string
		year  int
	}{
		{"Matrix (1999) DE 4K HDR", "matrix", 1999},
		{"INCEPTION - 2010", "inception", 2010},
		{"Inception HQ", "inception", 0},
		{"Breaking Bad (2008) TR", "breaking bad", 2008},
		{"Dark Matter – Der Zeitenläufer (2024) DE", "dark matter der zeitenlaufer", 2024},
		// A number that is part of the name stays, and a whole title never goes.
		{"Blade Runner 2049", "blade runner 2049", 0},
		{"IT", "it", 0},
		{"IT (2017) DE", "it", 2017},
	} {
		if title, year := Plain(c.name); title != c.title || year != c.year {
			t.Errorf("Plain(%q) = %q, %d; want %q, %d", c.name, title, year, c.title, c.year)
		}
	}
}

func TestBareLeavesTheTitleAsItWasWritten(t *testing.T) {
	for _, c := range []struct {
		name  string
		title string
		year  int
	}{
		{"Matrix (1999) DE 4K HDR", "Matrix", 1999},
		{"  Amélie [2001] FR ", "Amélie", 2001},
		{"Dark Matter – Der Zeitenläufer (2024) DE", "Dark Matter – Der Zeitenläufer", 2024},
		// A year at the very start is the title's own.
		{"(1999) Matrix", "(1999) Matrix", 0},
		// A dashed year must end the word.
		{"Show - 1999x - 2004", "Show - 1999x", 2004},
	} {
		if title, year := Bare(c.name); title != c.title || year != c.year {
			t.Errorf("Bare(%q) = %q, %d; want %q, %d", c.name, title, year, c.title, c.year)
		}
	}
}

func TestKeyIsWhatSpellingsShare(t *testing.T) {
	for name, want := range map[string]string{
		"Le Fabuleux Destin d’Amélie Poulain": "le fabuleux destin d amelie poulain",
		"Spider-Man: No Way Home":             "spider man no way home",
		"Брат":                                "",
		"86 EIGHTY-SIX":                       "86 eighty six",
		"One-Punch Man":                       "one punch man",
		"Pokémon":                             "pokemon",
		// The Kelvin sign decomposes to K; ß is no Latin letter of a–z.
		"Kelvin": "kelvin",
		"Straße": "stra e",
	} {
		if got := Key(name); got != want {
			t.Errorf("Key(%q) = %q, want %q", name, got, want)
		}
	}
}

// The app's own vectors, read from the file its tests read: one answer on
// both sides, or one film's history splits in two.
func TestTheAppsTitles(t *testing.T) {
	var shared struct {
		Plain, Bare []struct {
			Name  string `json:"name"`
			Title string `json:"title"`
			Year  int    `json:"year"`
		}
		Key []struct {
			Name string `json:"name"`
			Key  string `json:"key"`
		}
	}
	fixtures.Load(t, "titles.json", &shared)
	for _, c := range shared.Plain {
		if title, year := Plain(c.Name); title != c.Title || year != c.Year {
			t.Errorf("Plain(%q) = %q, %d; the app says %q, %d", c.Name, title, year, c.Title, c.Year)
		}
	}
	for _, c := range shared.Bare {
		if title, year := Bare(c.Name); title != c.Title || year != c.Year {
			t.Errorf("Bare(%q) = %q, %d; the app says %q, %d", c.Name, title, year, c.Title, c.Year)
		}
	}
	for _, c := range shared.Key {
		if got := Key(c.Name); got != c.Key {
			t.Errorf("Key(%q) = %q; the app says %q", c.Name, got, c.Key)
		}
	}
	if len(shared.Plain) == 0 || len(shared.Bare) == 0 || len(shared.Key) == 0 {
		t.Fatal("the app's titles were not read")
	}
}
