package trakt

import (
	"context"
	"errors"

	"foyer/internal/adapters"
)

// Adapter is Trakt as the Adapters page shows it: the household's own app,
// which each user connects their own account to.
func Adapter() adapters.Adapter {
	return adapters.Adapter{
		Key:         Key,
		ID:          "tracking/trakt",
		Category:    adapters.Tracking,
		Single:      true,
		Name:        "Trakt",
		Description: "Each user's watch history kept in step with their own Trakt account — both ways, or Foyer's way alone — every ten minutes.",
		Notice:      "Each user connects their own account from Loge, with a code typed at trakt.tv/activate. Films and episodes TMDB named are sent; a title known only by its name is not.",
		Fields: []adapters.Field{
			{Key: "clientId", Label: "Client ID", Type: adapters.Text, Required: true, Help: "Of the household's own app, made at trakt.tv/oauth/applications — its redirect address urn:ietf:wg:oauth:2.0:oob."},
			{Key: "clientSecret", Label: "Client secret", Type: adapters.Password, Required: true, Help: "Stored on this server as typed."},
		},
		Test: func(ctx context.Context, s adapters.Settings) adapters.Result {
			c := New(s.String("clientId"), s.String("clientSecret"))
			err := c.Check(ctx)
			switch {
			case err == nil:
				return adapters.Result{OK: true, Message: "Trakt knows the app. Its secret is checked when someone connects."}
			case errors.Is(err, ErrRefused):
				return adapters.Result{Message: "Trakt does not know that Client ID."}
			}
			return adapters.Result{Message: err.Error() + ". Try again in a while."}
		},
	}
}
