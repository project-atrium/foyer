// Package server binds Foyer's rules and routes to a PocketBase app — the one
// main starts, or the one a test starts on a temporary directory.
package server

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/adapters"
	"foyer/internal/adapters/tmdb"
	"foyer/internal/adapters/webdav"
	"foyer/internal/config"
	"foyer/internal/dashboard"
	"foyer/internal/ffmpeg"
	"foyer/internal/guard"
	"foyer/internal/household"
	"foyer/internal/library"
	"foyer/internal/probe"
	"foyer/internal/routes"
	"foyer/internal/stream"
	"foyer/internal/watch"
)

// Server is Foyer on one PocketBase app: what Bind made, for main and the
// tests to reach.
type Server struct {
	App       core.App
	Config    config.Config
	Adapters  *adapters.Registry
	Library   *library.Engine
	Streams   *stream.Streamer
	Probes    *probe.Queue
	Household *household.Service
}

// collections are Foyer's own static collections: written by its code alone.
var collections = []string{
	adapters.Collection,
	library.LibrariesCollection, library.EntriesCollection, library.FilesCollection, library.IdentitiesCollection,
	watch.Collection,
	household.Collection, household.DevicesCollection,
}

// Bind binds Foyer's guard, routes and dashboard pages, and starts nothing:
// a test that binds it runs no background work. main asks for that with
// Background.
func Bind(app core.App, cfg config.Config) *Server {
	// What is built first: an adapter that is built takes its planned
	// stand-in's place.
	built := []adapters.Adapter{webdav.Adapter(), tmdb.Adapter(), ffmpeg.Adapter()}
	registry := adapters.NewRegistry(app, append(built, adapters.Planned()...)...)
	lib := library.New(app, registry)
	s := &Server{
		App:      app,
		Config:   cfg,
		Adapters: registry,
		Library:  lib,
		Streams:  stream.New(app, library.FilesCollection, lib.OpenFile),
	}
	s.Probes = probe.New(app, registry, s.Streams, lib, library.FilesCollection)
	s.Household = household.New(app)
	s.Household.Bind()
	lib.OnScanned(s.Probes.Poke)
	lib.OnIdentityMoved(func(from, to string) error { return watch.Move(app, from, to) })
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
		se.Router.Bind(stream.Quiet())
		s.Streams.Routes(se.Router)
		s.Library.PlayRoutes(se.Router, s.Streams)
		watch.Routes(se.Router, library.Managed)
		admin := se.Router.Group("/api/foyer/admin")
		admin.Bind(apis.RequireSuperuserAuth())
		s.Adapters.Routes(admin)
		s.Library.Routes(admin)
		s.Household.Routes(se.Router, admin)
		s.Library.HookRoutes(se.Router)
		// Read by PocketBase's own handler for UI extensions, which runs last.
		se.Router.BindFunc(dashboard.Fresh)
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
// date, the loopback the tools read files through, then the scans and the
// probes, until the server stops.
func (s *Server) Background() *Server {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	s.App.OnServe().BindFunc(func(se *core.ServeEvent) error {
		s.Library.EnsureAll()
		if err := s.Streams.StartTwin(); err != nil {
			return err
		}
		wg.Go(func() { s.Library.Run(ctx) })
		wg.Go(func() { s.Probes.Run(ctx) })
		return se.Next()
	})
	s.App.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		cancel()
		stop, release := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.Streams.StopTwin(stop)
		release()
		// A scan stops at its next request; the database closes after.
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
		return e.Next()
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
