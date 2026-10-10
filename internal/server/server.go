// Package server binds Foyer's rules and routes to a PocketBase app — the one
// main starts, or the one a test starts on a temporary directory.
package server

import (
	"slices"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/adapters"
	"foyer/internal/adapters/tmdb"
	"foyer/internal/adapters/zurg"
	"foyer/internal/config"
	"foyer/internal/dashboard"
	"foyer/internal/guard"
	"foyer/internal/library"
	"foyer/internal/routes"
)

// Server is Foyer on one PocketBase app: what Bind made, for main and the
// tests to reach.
type Server struct {
	App      core.App
	Config   config.Config
	Adapters *adapters.Registry
	Library  *library.Engine
}

// collections are Foyer's own static collections: written by its code alone.
var collections = []string{
	adapters.Collection,
	library.LibrariesCollection, library.TorrentsCollection, library.FilesCollection, library.IdentitiesCollection,
}

// Bind binds Foyer's guard, routes and dashboard pages, and starts nothing:
// a test that binds it runs no background work. main asks for that with
// Background.
func Bind(app core.App, cfg config.Config) *Server {
	registry := adapters.NewRegistry(app, zurg.Adapter(), tmdb.Adapter())
	s := &Server{
		App:      app,
		Config:   cfg,
		Adapters: registry,
		Library:  library.New(app, registry),
	}
	guard.Bind(app, s.managed)
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		// Never PocketBase's installer: on a server with no superuser it opens a
		// browser tab on whoever started it — one per test. The first superuser
		// comes from FOYER_ADMIN_EMAIL and FOYER_ADMIN_PASSWORD, or from
		// `superuser upsert`.
		se.InstallerFunc = nil
		if err := trustProxy(se.App, cfg); err != nil {
			return err
		}
		routes.Register(se)
		admin := se.Router.Group("/api/foyer/admin")
		admin.Bind(apis.RequireSuperuserAuth())
		s.Adapters.Routes(admin)
		s.Library.Routes(admin)
		// Read by PocketBase's own handler for UI extensions, which runs last.
		extension, err := dashboard.Extension()
		if err != nil {
			return err
		}
		se.UIExtensions = append(se.UIExtensions, extension)
		return se.Next()
	})
	return s
}

// Background is what runs beside the routes once the server serves — main
// asks for it, a test never does: the library's collections brought up to
// date at start.
func (s *Server) Background() *Server {
	s.App.OnServe().BindFunc(func(se *core.ServeEvent) error {
		s.Library.EnsureAll()
		return se.Next()
	})
	return s
}

func (s *Server) managed(app core.App, name string) bool {
	return slices.Contains(collections, name) || library.Managed(app, name)
}

// trustProxy applies FOYER_TRUST_PROXY on every start, so `.env` stays the one
// place it is set. Without a proxy nothing is trusted: anyone could send the
// header, and claim any address past the limiter.
func trustProxy(app core.App, cfg config.Config) error {
	want := []string{}
	if cfg.TrustProxy != "" {
		want = []string{cfg.TrustProxy}
	}
	settings := app.Settings()
	if slices.Equal(settings.TrustedProxy.Headers, want) {
		return nil
	}
	settings.TrustedProxy.Headers = want
	return app.Save(settings)
}
