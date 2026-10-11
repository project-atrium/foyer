package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"

	"foyer/internal/config"
	"foyer/internal/server"
	_ "foyer/migrations"
)

// harness is the server on a temporary data directory: PocketBase with
// Foyer's migrations, hooks and routes, called through its real router.
type harness struct {
	t      *testing.T
	app    *tests.TestApp
	mux    http.Handler
	server *server.Server
}

type options struct {
	// Most tests sign in more often than the limiter lets one address.
	limits bool
}

func start(t *testing.T, opts options) *harness {
	t.Helper()
	app, err := tests.NewTestAppWithConfig(core.BaseAppConfig{DataDir: t.TempDir(), EncryptionEnv: "pb_test_env"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	if !opts.limits {
		app.Settings().RateLimits.Enabled = false
	}
	bound := server.Bind(app, config.Config{})

	router, err := apis.NewRouter(app)
	if err != nil {
		t.Fatal(err)
	}
	var mux http.Handler
	serve := &core.ServeEvent{App: app, Router: router}
	if err := app.OnServe().Trigger(serve, func(e *core.ServeEvent) error {
		built, err := e.Router.BuildMux()
		mux = built
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, app: app, mux: mux, server: bound}
}

type answer struct {
	status int
	body   map[string]any
	list   []any
}

func (h *harness) call(method, path, token string, body any) answer {
	h.t.Helper()
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		payload = encoded
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", token)
	}
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)

	result := answer{status: recorder.Code}
	raw := recorder.Body.Bytes()
	if len(raw) > 0 && raw[0] == '[' {
		_ = json.Unmarshal(raw, &result.list)
	} else if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result.body)
	}
	return result
}

// fetch answers a GET as it was sent, for what is not JSON. It asks for no
// compression, so the body is what the dashboard's routes serve.
func (h *harness) fetch(path string) (int, http.Header, []byte) {
	h.t.Helper()
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder.Code, recorder.Header(), recorder.Body.Bytes()
}

// fetchWithToken is fetch with a session's token, as the dashboard sends it:
// what a page's own <img> cannot.
func (h *harness) fetchWithToken(t *testing.T, path, token string) (int, http.Header, []byte) {
	t.Helper()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", token)
	h.mux.ServeHTTP(recorder, req)
	return recorder.Code, recorder.Header(), recorder.Body.Bytes()
}

// user is one profile, as the household's superuser makes it, and a session
// for it.
type user struct {
	id, token, username string
}

const password = "a long password"

// makeUser saves a profile as the dashboard does — as a superuser, past the
// rules — then signs it in through PocketBase's own route.
func (h *harness) makeUser(username string, fields map[string]any) user {
	h.t.Helper()
	users, err := h.app.FindCollectionByNameOrId("users")
	if err != nil {
		h.t.Fatal(err)
	}
	record := core.NewRecord(users)
	record.Set("username", username)
	record.Set("name", username)
	for name, value := range fields {
		record.Set(name, value)
	}
	record.SetPassword(password)
	if err := h.app.Save(record); err != nil {
		h.t.Fatalf("making %s: %v", username, err)
	}
	got := h.signIn(username, password)
	if got.status != http.StatusOK {
		h.t.Fatalf("signing in %s: %d %v", username, got.status, got.body)
	}
	return user{id: record.Id, token: got.body["token"].(string), username: username}
}

func (h *harness) signIn(username, password string) answer {
	h.t.Helper()
	return h.call(http.MethodPost, "/api/collections/users/auth-with-password", "", map[string]any{"identity": username, "password": password})
}

// superuser is a dashboard session.
func (h *harness) superuser() string {
	h.t.Helper()
	superusers, err := h.app.FindCollectionByNameOrId(core.CollectionNameSuperusers)
	if err != nil {
		h.t.Fatal(err)
	}
	record := core.NewRecord(superusers)
	record.SetEmail("admin@example.com")
	record.SetPassword(password)
	if err := h.app.Save(record); err != nil {
		h.t.Fatal(err)
	}
	token, err := record.NewAuthToken()
	if err != nil {
		h.t.Fatal(err)
	}
	return token
}
