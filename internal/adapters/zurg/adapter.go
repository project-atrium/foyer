package zurg

import (
	"context"
	"errors"
	"fmt"

	"foyer/internal/adapters"
)

// Key is this adapter's key in the registry.
const Key = "zurg"

// Adapter is Zurg as the Adapters page shows it. Which of its folders are
// libraries is set beside it, on the same page.
func Adapter() adapters.Adapter {
	return adapters.Adapter{
		Key:         Key,
		Name:        "Zurg",
		Description: "Your Real-Debrid library, read over WebDAV. Each Zurg folder you choose becomes a library; nothing in it is renamed or moved.",
		Fields: []adapters.Field{
			{
				Key: "url", Label: "Address", Type: adapters.URL, Required: true,
				Help: "Zurg's address, such as http://zurg:9999. Its WebDAV is under /dav/.",
			},
			{Key: "username", Label: "Username", Type: adapters.Text, Help: "Only if Zurg asks for one."},
			{Key: "password", Label: "Password", Type: adapters.Password, Help: "Stored on this server as typed."},
			{
				Key: "interval", Label: "Look for new media every", Type: adapters.Number, Default: 15, Min: 5, Max: 1440,
				Help: "Minutes. Zurg can also tell Foyer itself, from its on_library_update.",
			},
		},
		Test: func(ctx context.Context, s adapters.Settings) adapters.Result {
			client, err := New(s.String("url"), s.String("username"), s.String("password"))
			if err != nil {
				return adapters.Result{Message: err.Error() + "."}
			}
			entries, err := client.List(ctx, "")
			switch {
			case errors.Is(err, ErrRefused):
				return adapters.Result{Message: err.Error() + "."}
			case err != nil:
				return adapters.Result{Message: "Zurg did not answer as Zurg does: " + err.Error() + "."}
			}
			var folders []string
			for _, e := range entries {
				if e.Dir {
					folders = append(folders, e.Name)
				}
			}
			return adapters.Result{OK: true, Message: fmt.Sprintf("Zurg answers, with %d folders.", len(folders)), Details: map[string]any{"folders": folders}}
		},
	}
}
