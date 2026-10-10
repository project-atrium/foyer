// Foyer: the household's own server, on PocketBase. PocketBase does most of
// it — users, sign-in and sessions, the record APIs, the dashboard, backups
// and the rate limiter. This is the rest.
package main

import (
	"log"
	"os"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"

	"foyer/internal/config"
	"foyer/internal/server"
	"foyer/internal/version"
	_ "foyer/migrations"
)

func main() {
	if err := config.LoadDotEnv(".env"); err != nil {
		log.Fatal(err)
	}
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	for _, name := range cfg.Retired {
		log.Printf("%s does nothing any more: a user is a profile, and the superuser makes them", name)
	}

	// Dev mode — which PocketBase turns on by itself under `go run` — prints
	// every SQL statement to stdout. It is there for the asking: --dev.
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDev: false})
	app.RootCmd.Version = version.Version
	// The migrations own Foyer's collections: none is ever written from the dashboard.
	migratecmd.MustRegister(app, app.RootCmd, migratecmd.Config{Automigrate: false})
	server.Bind(app, cfg).Background()

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
