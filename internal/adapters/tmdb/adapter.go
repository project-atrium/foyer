package tmdb

import (
	"context"
	"errors"

	"foyer/internal/adapters"
)

// Key is this adapter's key in the registry.
const Key = "tmdb"

// Adapter is TMDB as the Adapters page shows it. Its notice is the one TMDB's
// terms ask for wherever its data is used.
func Adapter() adapters.Adapter {
	return adapters.Adapter{
		Key:         Key,
		Category:    adapters.Metadata,
		Single:      true,
		Name:        "TMDB",
		Description: "Names every film and series in the library, with its dates, ids and artwork — with your own key.",
		Notice:      "This product uses the TMDB API but is not endorsed or certified by TMDB.",
		Fields: []adapters.Field{
			{
				Key: "key", Label: "Read Access Token or API key", Type: adapters.Password, Required: true,
				Help: "From themoviedb.org → Settings → API. Stored on this server as typed.",
			},
			{
				Key: "language", Label: "Language", Type: adapters.Text, Default: "en-US",
				Pattern: `^[a-z]{2}(-[A-Z]{2})?$`,
				Help:    "What titles, overviews and artwork are asked for in, such as en-US or de-DE.",
			},
		},
		Test: func(ctx context.Context, s adapters.Settings) adapters.Result {
			err := New(s.String("key")).Check(ctx)
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
