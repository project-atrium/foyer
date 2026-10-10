// Package config reads the server's settings from its environment, once, at
// start. What a household tunes after that — Zurg, TMDB, ffmpeg — lives in
// Foyer's own records, set on the dashboard's Adapters page.
package config

import (
	"fmt"
	"regexp"
	"strings"
)

type Config struct {
	AdminEmail    string
	AdminPassword string
	// TrustProxy names the header a reverse proxy puts the caller's address in.
	// Empty without a proxy: then anyone could send it and claim any address.
	TrustProxy string
	// Retired names variables that are set but no longer do anything, for
	// main to say so once.
	Retired []string
}

var header = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// renamed are the variables this server read before it was Foyer.
var renamed = []string{"SC_MAX_PROFILES", "SC_SIGNUP", "SC_ADMIN_EMAIL", "SC_ADMIN_PASSWORD", "SC_TRUST_PROXY", "SC_PUBLISH"}

// retired went with the account that held profiles: a user is a profile now,
// and the household's superuser makes them. Nothing is weakened by ignoring
// them, so they are only named.
var retired = []string{"FOYER_MAX_PROFILES", "FOYER_SIGNUP"}

// FromEnv reads FOYER_ADMIN_EMAIL, FOYER_ADMIN_PASSWORD and FOYER_TRUST_PROXY,
// refusing a value it cannot use rather than guessing.
func FromEnv(get func(string) string) (Config, error) {
	// An environment written before the rename would otherwise lose its proxy
	// without a word — and behind a proxy, one stranger's tries would throttle
	// the whole household.
	for _, old := range renamed {
		if get(old) != "" {
			return Config{}, fmt.Errorf("%s is FOYER_%s now", old, strings.TrimPrefix(old, "SC_"))
		}
	}

	cfg := Config{
		AdminEmail:    strings.TrimSpace(get("FOYER_ADMIN_EMAIL")),
		AdminPassword: get("FOYER_ADMIN_PASSWORD"),
		TrustProxy:    strings.TrimSpace(get("FOYER_TRUST_PROXY")),
	}
	for _, name := range retired {
		if strings.TrimSpace(get(name)) != "" {
			cfg.Retired = append(cfg.Retired, name)
		}
	}

	if (cfg.AdminEmail == "") != (cfg.AdminPassword == "") {
		return Config{}, fmt.Errorf("FOYER_ADMIN_EMAIL and FOYER_ADMIN_PASSWORD go together")
	}

	if cfg.TrustProxy != "" && !header.MatchString(cfg.TrustProxy) {
		return Config{}, fmt.Errorf("FOYER_TRUST_PROXY must be a header name, such as X-Forwarded-For, not %q", cfg.TrustProxy)
	}

	return cfg, nil
}
