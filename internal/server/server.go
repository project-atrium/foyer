// Package server binds Foyer's rules and routes to a PocketBase app — the one
// main starts, or the one a test starts on a temporary directory.
package server

import (
	"context"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"

	"foyer/internal/adapters"
	"foyer/internal/adapters/tmdb"
	"foyer/internal/adapters/webdav"
	"foyer/internal/catalogue"
	"foyer/internal/config"
	"foyer/internal/copies"
	"foyer/internal/dashboard"
	"foyer/internal/ffmpeg"
	"foyer/internal/guard"
	"foyer/internal/hls"
	"foyer/internal/household"
	"foyer/internal/library"
	"foyer/internal/media"
	"foyer/internal/probe"
	"foyer/internal/routes"
	"foyer/internal/sessions"
	"foyer/internal/stream"
	"foyer/internal/userstate"
	"foyer/internal/watch"
	"foyer/internal/youtube"
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
	Catalogue *catalogue.Service
	Playings  *sessions.Registry
	Remux     *hls.Manager
	Copies    *copies.Manager
	YouTube   *youtube.YouTube
}

// collections are Foyer's own static collections: written by its code alone.
var collections = []string{
	adapters.Collection,
	library.LibrariesCollection, library.EntriesCollection, library.FilesCollection, library.IdentitiesCollection,
	watch.Collection,
	household.Collection, household.DevicesCollection,
	userstate.Collections[userstate.KindPreference], userstate.Collections[userstate.KindSubscription],
	userstate.Collections[userstate.KindPlaylist], userstate.Collections[userstate.KindFavoriteChannel],
	sessions.Collection,
}

// Bind binds Foyer's guard, routes and dashboard pages, and starts nothing:
// a test that binds it runs no background work. main asks for that with
// Background.
func Bind(app core.App, cfg config.Config) *Server {
	// What is built first: an adapter that is built takes its planned
	// stand-in's place.
	built := []adapters.Adapter{webdav.Adapter(), tmdb.Adapter(), ffmpeg.Adapter(), youtube.Adapter()}
	registry := adapters.NewRegistry(app, append(built, adapters.Planned()...)...)
	lib := library.New(app, registry)
	s := &Server{
		App:      app,
		Config:   cfg,
		Adapters: registry,
		Library:  lib,
		Streams:  stream.New(app, library.FilesCollection, lib.OpenFile),
	}
	s.Playings = sessions.New(app, library.FilesCollection)
	s.Streams.SetPlayings(s.Playings)
	fileOf := func(id string) (*core.Record, error) { return app.FindRecordById(library.FilesCollection, id) }
	s.Remux = hls.NewManager(hls.Options{
		Log: func(msg string, args ...any) { app.Logger().Warn(msg, args...) },
		// PocketBase's own temporary place: never in a backup, and emptied at
		// every start.
		Root: filepath.Join(app.DataDir(), core.LocalTempDirName, "hls"),
		Sign: func(payload []byte) ([]byte, error) { return s.Streams.Sign("hls ticket v1", payload) },
		Tool: func() (string, bool) {
			settings, err := registry.Settings(ffmpeg.Key)
			if err != nil || !settings.Enabled {
				return "", false
			}
			tool := ffmpeg.FromSettings(settings).FFmpeg
			return tool, tool != ""
		},
		Converting: func() bool {
			settings, err := registry.Settings(ffmpeg.Key)
			return err == nil && settings.Enabled && settings.Bool("convert")
		},
		Grants: s.Streams,
		Index: func(ctx context.Context, id string) (hls.Index, error) {
			file, err := fileOf(id)
			if err != nil {
				return hls.Index{}, err
			}
			return lib.Keyframes(ctx, file)
		},
		Version: func(id string) (media.Version, error) {
			file, err := fileOf(id)
			if err != nil {
				return media.Version{}, err
			}
			return library.VersionOf(file), nil
		},
		Playings: s.Playings,
	})
	// A remux stops with its playing, whyever it ends.
	s.Playings.OnEnd(s.Remux.End)
	s.Copies = copies.New(copies.Options{
		Root: filepath.Join(app.DataDir(), copies.DirName),
		Settings: func() copies.Settings {
			settings, err := registry.Settings(ffmpeg.Key)
			if err != nil {
				return copies.Settings{}
			}
			c := copies.Settings{Effort: settings.String("copy_effort"), AtOnce: settings.Int("copies_at_once"),
				KeepDays: settings.Int("copies_kept_days"), RoomBytes: int64(settings.Int("copies_room_gb")) << 30}
			if settings.Enabled && settings.Bool("copies") {
				c.Tool = ffmpeg.FromSettings(settings).FFmpeg
			}
			return c
		},
		Grants: s.Streams,
		Sign:   func(payload []byte) ([]byte, error) { return s.Streams.Sign(copies.SignLabel, payload) },
		Log:    func(msg string, args ...any) { app.Logger().Warn(msg, args...) },
	})
	// yt-dlp's cache and Deno's in PocketBase's own temporary place: never in
	// a backup, and emptied at every start.
	s.YouTube = youtube.New(func() (string, bool) {
		settings, err := registry.Settings(youtube.Key)
		if err != nil || !settings.Enabled {
			return "", false
		}
		tool := settings.String("ytdlp")
		if tool == "" {
			tool = "yt-dlp"
		}
		return tool, true
	}, func() string {
		if settings, err := registry.Settings(youtube.Key); err == nil && settings.String("region") != "" {
			return settings.String("region")
		}
		return "US"
	}, filepath.Join(app.DataDir(), core.LocalTempDirName, "youtube"), s.Streams, s.Playings)
	// Copies are a cache anyone can make again: never in a backup, and a
	// restore leaves the ones here where they are.
	for _, backups := range []*hook.Hook[*core.BackupEvent]{app.OnBackupCreate(), app.OnBackupRestore()} {
		backups.BindFunc(func(e *core.BackupEvent) error {
			e.Exclude = append(e.Exclude, copies.DirName)
			return e.Next()
		})
	}
	s.Probes = probe.New(app, registry, s.Streams, lib, library.FilesCollection)
	s.Household = household.New(app)
	s.Household.Bind()
	s.Household.Serving(func(app core.App) []map[string]any {
		served := catalogue.Served(app)
		// The site's videos, while YouTube is switched on.
		if settings, err := registry.Settings(youtube.Key); err == nil && settings.Enabled {
			served = append(served, map[string]any{"key": "videos", "type": "videos", "name": "YouTube"})
		}
		return served
	})
	s.Catalogue = catalogue.New(app)
	lib.OnScanned(s.Probes.Poke)
	lib.ProbeNow(s.Probes.Now)
	lib.OnIdentityMoved(func(from, to string) error {
		if err := watch.Move(app, from, to); err != nil {
			return err
		}
		return watch.MoveExtras(app, from, to)
	})
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
		// A playing stopped before a restart stays stopped.
		if err := s.Playings.Load(); err != nil {
			return err
		}
		s.Library.PlayRoutes(se.Router, s.Streams, s.Playings, s.Remux, s.Copies)
		s.Remux.Routes(se.Router)
		s.YouTube.Routes(se.Router)
		watch.Routes(se.Router, library.Managed, func(id string) (watch.Item, bool) {
			if !youtube.ValidID.MatchString(id) {
				return watch.Item{}, false
			}
			item := watch.Item{Collection: watch.Videos, ID: id, Identity: youtube.Identity(id)}
			if d, ok := s.YouTube.Describe(id); ok {
				item.Title, item.Subtitle, item.SubtitleID, item.Picture = d.Title, d.Channel, d.ChannelID, d.Thumbnail
			}
			return item, true
		}, s.Playings.Heard)
		s.Catalogue.Routes(se.Router)
		userstate.Routes(se.Router)
		admin := se.Router.Group("/api/foyer/admin")
		admin.Bind(apis.RequireSuperuserAuth())
		s.Adapters.Routes(admin)
		s.Playings.Routes(admin)
		s.Copies.Routes(se.Router, admin, s.Playings)
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
		wg.Go(func() {
			// Titles saved before their keys existed get them first: the catalogue sorts by them.
			if err := s.Library.Backfill(ctx); err != nil && ctx.Err() == nil {
				s.App.Logger().Error("Titles' keys could not be filled in", "error", err)
			}
			s.Library.Run(ctx)
		})
		wg.Go(func() { s.Probes.Run(ctx) })
		wg.Go(func() { s.Copies.Run(ctx) })
		wg.Go(func() { s.pruneTombstones(ctx) })
		wg.Go(func() { s.sweepPlayings(ctx) })
		return se.Next()
	})
	s.App.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		cancel()
		s.Remux.Close()
		s.Copies.Close()
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

// pruneTombstones lets go of deletes older than a device is ever away for,
// once a day: one away longer reads everything again.
func (s *Server) pruneTombstones(ctx context.Context) {
	for {
		if removed, err := userstate.Prune(s.App, time.Now().Add(-userstate.TombstonesKept)); err != nil {
			s.App.Logger().Error("Old deletes could not be let go of", "error", err)
		} else if removed > 0 {
			s.App.Logger().Info("Old deletes let go of", "records", removed)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}

// sweepPlayings ends what is over, every half minute: a playing whose
// addresses expired, a download whose bytes stopped.
func (s *Server) sweepPlayings(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
			s.Playings.Sweep()
		}
	}
}
