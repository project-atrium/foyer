package titles

import (
	"slices"
	"strings"
	"testing"
	"time"

	"foyer/internal/fixtures"
)

// order is the app's order.json: the orders compareItems gives, which the
// catalogue's keys must give in SQL — the cases where Go's and JavaScript's
// lower case, and their string order, differ among them.
type order struct {
	Items []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		SortTitle   *string  `json:"sortTitle"`
		Year        int      `json:"year"`
		ReleaseDate string   `json:"releaseDate"`
		AddedAt     string   `json:"addedAt"`
		Rating      *float64 `json:"rating"`
	} `json:"items"`
	Orders []struct {
		By    string   `json:"by"`
		Order string   `json:"order"`
		IDs   []string `json:"ids"`
	} `json:"orders"`
}

// keyed is an item as the catalogue keeps it: every value a key compared as
// bytes, a missing one empty.
type keyed struct{ id, title, release, added, rating string }

func TestKeysOrderAsCompareItemsDoes(t *testing.T) {
	var fixture order
	fixtures.Load(t, "order.json", &fixture)
	items := make([]keyed, 0, len(fixture.Items))
	for _, i := range fixture.Items {
		// A sort title present, even empty, is the one compared: Foyer never
		// sends an empty one, so the app falls back as the catalogue does.
		text := i.Title
		if i.SortTitle != nil {
			text = *i.SortTitle
		}
		added := ""
		if i.AddedAt != "" {
			at, err := time.Parse(time.RFC3339, i.AddedAt)
			if err != nil {
				t.Fatal(err)
			}
			// As PocketBase stores a date.
			added = at.UTC().Format("2006-01-02 15:04:05.000Z")
		}
		items = append(items, keyed{i.ID, SortKey(text), ReleaseKey(i.ReleaseDate, i.Year), added, RatingKey(i.Rating)})
	}
	for _, o := range fixture.Orders {
		dir := 1
		if o.Order == "desc" {
			dir = -1
		}
		value := func(k keyed) string {
			switch o.By {
			case "releaseDate":
				return k.release
			case "addedAt":
				return k.added
			case "rating":
				return k.rating
			}
			return k.title
		}
		sorted := slices.Clone(items)
		slices.SortFunc(sorted, func(a, b keyed) int {
			if c := dir * strings.Compare(value(a), value(b)); c != 0 {
				return c
			}
			// Ties: the title, then the id, both ascending whatever the order.
			if c := strings.Compare(a.title, b.title); c != 0 {
				return c
			}
			return strings.Compare(a.id, b.id)
		})
		got := make([]string, len(sorted))
		for i, k := range sorted {
			got[i] = k.id
		}
		if !slices.Equal(got, o.IDs) {
			t.Errorf("%s %s:\n got %v\nwant %v", o.By, o.Order, got, o.IDs)
		}
	}
}

func TestLowerIsJavaScripts(t *testing.T) {
	for in, want := range map[string]string{
		"İstanbul":    "i\u0307stanbul",
		"ΟΔΥΣΣΕΥΣ":    "οδυσσευς",
		"ΣΑΣ":         "σας",
		"Σ":           "σ",
		"ΑΣ.":         "ας.",
		"ΑΣ'Β":        "ασ'β",
		"Straße":      "straße",
		"𝔄lpha":       "𝔄lpha",
		"THE THING":   "the thing",
		"ǅungla":      "ǆungla",
		"ΜΑΣ ΚΑΙ ΣΑΣ": "μας και σας",
	} {
		if got := Lower(in); got != want {
			t.Errorf("Lower(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFoldAndGenreKeyAreTheApps(t *testing.T) {
	if got := Fold("Pokémon İstanbul"); got != "pokemon istanbul" {
		t.Errorf("Fold = %q", got)
	}
	if got := GenreKey("  Science  Fiction "); got != "science fiction" {
		t.Errorf("GenreKey = %q", got)
	}
	if got := GenreKey("Ação"); got != "acao" {
		t.Errorf("GenreKey = %q", got)
	}
}

func TestReleaseAndRatingKeys(t *testing.T) {
	for _, c := range []struct {
		date string
		year int
		want string
	}{
		{"2022-12-28", 2022, "2022-12-28"},
		{"", 2020, "2020-01-01"},
		{"not a date", 1999, "1999-01-01"},
		{"2010", 0, "2010-01-01"},
		{"", 0, ""},
	} {
		if got := ReleaseKey(c.date, c.year); got != c.want {
			t.Errorf("ReleaseKey(%q, %d) = %q, want %q", c.date, c.year, got, c.want)
		}
	}
	zero, high, long := 0.0, 10.0, 7.91234
	if RatingKey(nil) != "" || RatingKey(&zero) != "0000.000" || RatingKey(&high) != "0010.000" || RatingKey(&long) != "0007.912" {
		t.Error("RatingKey does not keep the order of the numbers")
	}
}
