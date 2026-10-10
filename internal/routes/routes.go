// Package routes holds the routes that belong to no module: what a device
// may ask before anyone signs in. Signing in, sessions and reading records
// are PocketBase's own.
package routes

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/version"
)

func Register(se *core.ServeEvent) {
	// Without a session: a device checks it reached Foyer, and which one.
	se.Router.GET("/api/foyer/info", func(e *core.RequestEvent) error {
		return e.JSON(http.StatusOK, map[string]any{"serverVersion": version.Version})
	})
}
