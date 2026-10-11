package server_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"

	"foyer/internal/config"
	"foyer/internal/fixtures"
	"foyer/internal/server"
	"foyer/internal/version"
)

func TestNeverOpensTheInstaller(t *testing.T) {
	app, err := tests.NewTestAppWithConfig(core.BaseAppConfig{DataDir: t.TempDir(), EncryptionEnv: "pb_test_env"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	// A test app skips PocketBase's installer by itself, first of all. Arm it
	// again, as `serve` does, so that only Bind can skip it.
	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		e.InstallerFunc = apis.DefaultInstallerFunc
		return e.Next()
	})
	server.Bind(app, config.Config{})
	router, err := apis.NewRouter(app)
	if err != nil {
		t.Fatal(err)
	}
	serve := &core.ServeEvent{App: app, Router: router}
	if err := app.OnServe().Trigger(serve, func(e *core.ServeEvent) error {
		if e.InstallerFunc != nil {
			t.Error("the installer would open a browser tab")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTheServerIsFoyer(t *testing.T) {
	h := start(t, options{})
	settings := h.app.Settings()
	labels := []string{}
	for _, rule := range settings.RateLimits.Rules {
		labels = append(labels, rule.Label)
	}
	if settings.Meta.AppName != "Foyer" || !slices.Equal(labels, []string{"*:authWithPassword", "*:authRefresh", "/api/foyer/stream/", "/api/foyer/webdav/", "/api/"}) {
		t.Fatalf("name %q, rate limit rules %v", settings.Meta.AppName, labels)
	}
}

func TestInfoNeedsNoSession(t *testing.T) {
	h := start(t, options{})
	got := h.call(http.MethodGet, "/api/foyer/info", "", nil)
	if got.status != http.StatusOK || got.body["serverVersion"] != version.Version || got.body["protocol"] != float64(version.Protocol) {
		t.Fatalf("info: %d %v", got.status, got.body)
	}
	// It answers as the app's fixture says, key for key.
	var fixture struct {
		Answer map[string]any `json:"answer"`
	}
	fixtures.Load(t, "routes/info.json", &fixture)
	if !sameKeys(got.body, fixture.Answer) || fixture.Answer["protocol"] != float64(version.Protocol) {
		t.Fatalf("info %v, the app's fixture %v", got.body, fixture.Answer)
	}
}

// sameKeys says whether two answers carry the same keys, each of the same
// JSON kind: what an adapter reading one would find in the other.
func sameKeys(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || fmt.Sprintf("%T", v) != fmt.Sprintf("%T", w) {
			return false
		}
	}
	return true
}

// --- A user is a profile ------------------------------------------------------

func TestOnlyTheSuperuserMakesOrRemovesAUser(t *testing.T) {
	h := start(t, options{})
	alex := h.makeUser("alex", nil)
	body := map[string]any{"username": "mallory", "name": "Mallory", "password": password, "passwordConfirm": password}
	if got := h.call(http.MethodPost, "/api/collections/users/records", "", body); got.status != http.StatusForbidden {
		t.Fatalf("a guest making a user: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodPost, "/api/collections/users/records", alex.token, body); got.status != http.StatusForbidden {
		t.Fatalf("a user making another: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodDelete, "/api/collections/users/records/"+alex.id, alex.token, nil); got.status != http.StatusForbidden {
		t.Fatalf("a user removing itself: %d %v", got.status, got.body)
	}
	admin := h.superuser()
	if got := h.call(http.MethodPost, "/api/collections/users/records", admin, body); got.status != http.StatusOK {
		t.Fatalf("the superuser making a user: %d %v", got.status, got.body)
	}
}

func TestAUserSeesOnlyItself(t *testing.T) {
	h := start(t, options{})
	alex, sam := h.makeUser("alex", nil), h.makeUser("sam", nil)
	got := h.call(http.MethodGet, "/api/collections/users/records", alex.token, nil)
	items, _ := got.body["items"].([]any)
	if got.status != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["id"] != alex.id {
		t.Fatalf("alex's list: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodGet, "/api/collections/users/records/"+sam.id, alex.token, nil); got.status != http.StatusNotFound {
		t.Fatalf("alex viewing sam: %d %v", got.status, got.body)
	}
}

// A PIN is a child lock on its own user's record: the user reads it, so a
// device signed in as several users can ask for it, and nobody else does.
func TestAUserKeepsItsOwnPin(t *testing.T) {
	h := start(t, options{})
	alex, sam := h.makeUser("alex", map[string]any{"pin": "1234"}), h.makeUser("sam", nil)
	got := h.call(http.MethodGet, "/api/collections/users/records/"+alex.id, alex.token, nil)
	if got.status != http.StatusOK || got.body["pin"] != "1234" {
		t.Fatalf("alex's own pin: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodPatch, "/api/collections/users/records/"+alex.id, alex.token, map[string]any{"pin": "5678"}); got.status != http.StatusOK || got.body["pin"] != "5678" {
		t.Fatalf("alex changing it: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodPatch, "/api/collections/users/records/"+alex.id, alex.token, map[string]any{"pin": "12a4"}); got.status != http.StatusBadRequest {
		t.Fatalf("a pin that is not four digits: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodPatch, "/api/collections/users/records/"+alex.id, alex.token, map[string]any{"pin": ""}); got.status != http.StatusOK || got.body["pin"] != "" {
		t.Fatalf("alex taking it off: %d %v", got.status, got.body)
	}
	if got := h.call(http.MethodPatch, "/api/collections/users/records/"+alex.id, sam.token, map[string]any{"pin": "0000"}); got.status != http.StatusNotFound {
		t.Fatalf("sam setting alex's pin: %d %v", got.status, got.body)
	}
}

func TestUsernamesAreOneUserEachWhateverTheCase(t *testing.T) {
	h := start(t, options{})
	h.makeUser("Alex", nil)
	users, err := h.app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	twin := core.NewRecord(users)
	twin.Set("username", "alex")
	twin.Set("name", "alex")
	twin.SetPassword(password)
	if err := h.app.Save(twin); err == nil {
		t.Fatal("a second alex was made")
	}
	// And signing in does not care about case either.
	if got := h.signIn("ALEX", password); got.status != http.StatusOK {
		t.Fatalf("sign-in by another case: %d %v", got.status, got.body)
	}
}

// --- Sessions -----------------------------------------------------------------

func TestASessionLastsThirtyDays(t *testing.T) {
	h := start(t, options{})
	alex := h.makeUser("alex", nil)
	claims := claimsOf(t, alex.token)
	lasts := time.Until(time.Unix(int64(claims["exp"].(float64)), 0))
	if lasts < 30*24*time.Hour-time.Minute || lasts > 30*24*time.Hour+time.Minute {
		t.Fatalf("a session lasts %s", lasts)
	}
	refreshed := h.call(http.MethodPost, "/api/collections/users/auth-refresh", alex.token, nil)
	if refreshed.status != http.StatusOK || refreshed.body["token"] == "" {
		t.Fatalf("refresh: %d %v", refreshed.status, refreshed.body)
	}
}

func TestAPasswordChangeEndsEverySession(t *testing.T) {
	h := start(t, options{})
	alex := h.makeUser("alex", nil)
	record, err := h.app.FindRecordById("users", alex.id)
	if err != nil {
		t.Fatal(err)
	}
	// As the dashboard does it: PocketBase moves the token key along.
	record.SetPassword("another long password")
	if err := h.app.Save(record); err != nil {
		t.Fatal(err)
	}
	if got := h.call(http.MethodPost, "/api/collections/users/auth-refresh", alex.token, nil); got.status != http.StatusUnauthorized {
		t.Fatalf("the old session still refreshes: %d", got.status)
	}
	if got := h.signIn("alex", password); got.status != http.StatusBadRequest {
		t.Fatalf("the old password still signs in: %d", got.status)
	}
	if got := h.signIn("alex", "another long password"); got.status != http.StatusOK {
		t.Fatalf("the new password: %d", got.status)
	}
}

func TestSigningInIsThrottledPerAddress(t *testing.T) {
	h := start(t, options{limits: true})
	h.makeUser("alex", nil)
	statuses := []int{}
	for i := 0; i < 6; i++ {
		statuses = append(statuses, h.signIn("alex", "wrong password").status)
	}
	// makeUser signed in once already.
	if statuses[3] != http.StatusBadRequest || statuses[4] != http.StatusTooManyRequests {
		t.Fatalf("sign-ins from one address: %v", statuses)
	}
}

func claimsOf(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a token: %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}
