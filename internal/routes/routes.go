// Package routes holds the routes that belong to no module: what a device
// may ask before anyone signs in. Signing in, sessions and reading records
// are PocketBase's own.
package routes

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/household"
	"foyer/internal/version"
)

func Register(se *core.ServeEvent) {
	// Without a session: a device checks it reached Foyer, which household's,
	// and which protocol it speaks — the household's id is what it keys this
	// Foyer by.
	se.Router.GET("/api/foyer/info", func(e *core.RequestEvent) error {
		h, err := household.Get(e.App)
		if err != nil {
			return e.InternalServerError("", err)
		}
		return e.JSON(http.StatusOK, map[string]any{"serverVersion": version.Version, "protocol": version.Protocol, "household": h})
	})
}
