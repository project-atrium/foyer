package tmdb

import (
	"context"
	"errors"

	"foyer/internal/adapters"
)

// Key is this adapter's key in the registry.
const Key = "tmdb"

// APIKey is the field the household's key is kept under, as the app keys it.
// It was `key` before the two were one field, and that is not migrated: a
// Foyer whose key is under the old name is as without TMDB until it is
// entered again.
const APIKey = "apiKey"

// Adapter is TMDB as the Adapters page shows it. Its notice is the one TMDB's
// terms ask for wherever its data is used.
func Adapter() adapters.Adapter {
	return adapters.Adapter{
		Key:         Key,
		ID:          "metadata/tmdb",
		Category:    adapters.Metadata,
		Single:      true,
		Name:        "TMDB",
		Description: "Names every film and series in the library, with its dates, ids and artwork — with your own key.",
		Notice:      "This product uses the TMDB API but is not endorsed or certified by TMDB.",
		Fields: []adapters.Field{
			{
				Key: APIKey, Label: "API key or Read Access Token", Type: adapters.Password, Required: true,
				Help: "From themoviedb.org → Settings → API. Stored on this server as typed.",
			},
			{
				Key: "language", Label: "Language", Type: adapters.Text, Default: "en-US",
				Pattern: `^[a-z]{2}(-[A-Z]{2})?$`,
				Help:    "What titles, overviews and artwork are asked for in, such as en-US or de-DE.",
			},
		},
		Test: func(ctx context.Context, s adapters.Settings) adapters.Result {
			err := New(s.String(APIKey)).Check(ctx)
			switch {
			case err == nil:
				return adapters.Result{OK: true, Message: "TMDB takes the key."}
			case errors.Is(err, ErrNoKey), errors.Is(err, ErrRefused):
				return adapters.Result{Message: err.Error() + "."}
			default:
				return adapters.Result{Message: err.Error() + ". Try again in a while."}
			}
		},
	}
}
