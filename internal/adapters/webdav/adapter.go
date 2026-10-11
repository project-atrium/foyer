package webdav

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"foyer/internal/adapters"
)

// Key is this adapter's key in the registry.
const Key = "webdav"

// Adapter is a WebDAV share as the Adapters page shows it — Zurg's above
// all. Which of its folders are libraries is set beside it, on the page.
func Adapter() adapters.Adapter {
	return adapters.Adapter{
		Key:      Key,
		Category: adapters.Sources,
		Name:     "WebDAV",
		Description: "A WebDAV share: Zurg's Real-Debrid library, rclone's, a NAS's. Each of its folders you choose becomes a library; " +
			"nothing in it is renamed or moved.",
		Fields: []adapters.Field{
			{
				Key: "url", Label: "Address", Type: adapters.URL, Required: true,
				Help: "The share's WebDAV root. Zurg's is under /dav: http://zurg:9999/dav.",
			},
			{Key: "username", Label: "Username", Type: adapters.Text, Help: "Only if the share asks for one."},
			{Key: "password", Label: "Password", Type: adapters.Password, Help: "Stored on this server as typed."},
			{
				Key: "interval", Label: "Look for new media every", Type: adapters.Number, Default: 15, Min: 5, Max: 1440,
				Help: "Minutes. The share can also tell Foyer itself — Zurg from its on_library_update.",
			},
		},
		Test: func(ctx context.Context, s adapters.Settings) adapters.Result {
			client, err := New(s.String("url"), s.String("username"), s.String("password"))
			if err != nil {
				return adapters.Result{Message: capital(err.Error()) + "."}
			}
			entries, err := client.List(ctx, "")
			switch {
			case errors.Is(err, ErrRefused):
				return adapters.Result{Message: capital(err.Error()) + "."}
			case errors.Is(err, ErrNotWebDAV), errors.Is(err, ErrNotFound):
				return adapters.Result{Message: "Nothing answers as WebDAV at that address. The address is the share's WebDAV root: Zurg's is http://zurg:9999/dav."}
			case err != nil:
				return adapters.Result{Message: capital(err.Error()) + "."}
			}
			var folders []string
			for _, e := range entries {
				if e.Dir {
					folders = append(folders, e.Name)
				}
			}
			return adapters.Result{OK: true, Message: fmt.Sprintf("The share answers, with %d folders.", len(folders)), Details: map[string]any{"folders": folders}}
		},
	}
}

// capital starts a sentence with an error's words.
func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
