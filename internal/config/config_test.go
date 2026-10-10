package config

import (
	"slices"
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestDefaults(t *testing.T) {
	cfg, err := FromEnv(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminEmail != "" || cfg.TrustProxy != "" || len(cfg.Retired) != 0 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestReadsEveryVariable(t *testing.T) {
	cfg, err := FromEnv(env(map[string]string{
		"FOYER_ADMIN_EMAIL":    "admin@example.com",
		"FOYER_ADMIN_PASSWORD": "a long password",
		"FOYER_TRUST_PROXY":    "X-Forwarded-For",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminEmail != "admin@example.com" || cfg.AdminPassword != "a long password" || cfg.TrustProxy != "X-Forwarded-For" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestRefusesWhatItCannotUse(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"an email without a password": {"FOYER_ADMIN_EMAIL": "admin@example.com"},
		"a password without an email": {"FOYER_ADMIN_PASSWORD": "a long password"},
		"a proxy header that is not":  {"FOYER_TRUST_PROXY": "X-Forwarded-For: 1.2.3.4"},
	} {
		if _, err := FromEnv(env(values)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRefusesTheNamesFromBeforeFoyer(t *testing.T) {
	for _, old := range []string{"SC_MAX_PROFILES", "SC_SIGNUP", "SC_ADMIN_EMAIL", "SC_ADMIN_PASSWORD", "SC_TRUST_PROXY", "SC_PUBLISH"} {
		_, err := FromEnv(env(map[string]string{old: "anything"}))
		if err == nil {
			t.Errorf("%s: accepted", old)
			continue
		}
		if want := "FOYER_" + old[len("SC_"):]; !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %q does not name %s", old, err, want)
		}
	}
}

// The profile limit and the sign-up mode went with the account that held
// profiles. Ignoring them weakens nothing, so an old .env still starts — and
// main says they do nothing now.
func TestNamesWhatNoLongerDoesAnything(t *testing.T) {
	cfg, err := FromEnv(env(map[string]string{"FOYER_MAX_PROFILES": "4", "FOYER_SIGNUP": "open"}))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Retired, []string{"FOYER_MAX_PROFILES", "FOYER_SIGNUP"}) {
		t.Fatalf("retired: %v", cfg.Retired)
	}
}
