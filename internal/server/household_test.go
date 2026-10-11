package server_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"foyer/internal/fixtures"
)

// deviceHeader is what a device says of itself on the calls that sign in.
func deviceHeader(t *testing.T, installation, name string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"installation": installation, "name": name, "platform": "android", "appVersion": "2026.10.1"})
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func TestInfoNamesTheHousehold(t *testing.T) {
	h := start(t, options{})
	got := h.call(http.MethodGet, "/api/foyer/info", "", nil)
	household, _ := got.body["household"].(map[string]any)
	if got.status != http.StatusOK || household["name"] != "Foyer" || household["id"] == "" {
		t.Fatalf("info: %d %v", got.status, got.body)
	}
	// The superuser names it.
	if renamed := h.call(http.MethodPatch, "/api/foyer/admin/household", h.superuser(), map[string]any{"name": "The Kerims"}); renamed.status != http.StatusOK {
		t.Fatalf("renaming: %d %v", renamed.status, renamed.body)
	}
	again := h.call(http.MethodGet, "/api/foyer/info", "", nil)
	if again.body["household"].(map[string]any)["name"] != "The Kerims" || again.body["household"].(map[string]any)["id"] != household["id"] {
		t.Fatalf("renamed: %v", again.body)
	}
}

// A user may know who else is in the household — their names and pictures,
// and whether a PIN locks them — and nothing more: never a PIN.
func TestAUserSeesTheHouseholdsMembersAndNoPIN(t *testing.T) {
	h := start(t, options{})
	alex := h.makeUser("alex", map[string]any{"name": "Alex", "pin": "1234"})
	h.makeUser("kids", map[string]any{"name": "Kids"})
	if got := h.call(http.MethodGet, "/api/foyer/household", "", nil); got.status != http.StatusUnauthorized {
		t.Fatalf("a guest: %d %v", got.status, got.body)
	}
	got := h.call(http.MethodGet, "/api/foyer/household", alex.token, nil)
	members, _ := got.body["members"].([]any)
	if got.status != http.StatusOK || len(members) != 2 {
		t.Fatalf("members: %d %v", got.status, got.body)
	}
	raw, _ := json.Marshal(got.body)
	if strings.Contains(string(raw), "1234") || strings.Contains(string(raw), "email") {
		t.Fatalf("a member's PIN or email reached another: %s", raw)
	}
	first := members[0].(map[string]any)
	if first["name"] != "Alex" || first["hasPin"] != true || first["username"] != "alex" || members[1].(map[string]any)["hasPin"] != false {
		t.Fatalf("members %v", members)
	}
	// The record API still shows a user only itself.
	if listed := h.call(http.MethodGet, "/api/collections/users/records", alex.token, nil); listed.body["totalItems"] != float64(1) {
		t.Fatalf("users listed: %v", listed.body)
	}
}

// A code's users come back in the order the superuser named them, whatever
// order SQLite keeps them in.
func TestAPairingAnswersItsUsersInTheirOrder(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	alex := h.makeUser("alex", map[string]any{"name": "Alex"})
	kids := h.makeUser("kids", map[string]any{"name": "Kids"})
	for _, order := range [][]string{{alex.id, kids.id}, {kids.id, alex.id}} {
		code := h.call(http.MethodPost, "/api/foyer/admin/pairings", admin, map[string]any{"users": order}).body["code"]
		paired := h.call(http.MethodPost, "/api/foyer/pair", "", map[string]any{"code": code, "device": map[string]any{"installation": "device-hash-1"}})
		users, _ := paired.body["users"].([]any)
		got := []string{}
		for _, raw := range users {
			got = append(got, raw.(map[string]any)["member"].(map[string]any)["id"].(string))
		}
		if !slices.Equal(got, order) {
			t.Errorf("named %v, answered %v", order, got)
		}
	}
}

// A pairing code signs the users it names in on one device, once, within
// ten minutes — with no password crossing — and any code that will not do
// is refused alike.
func TestAPairingCodeSignsItsUsersInOnce(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	alex := h.makeUser("alex", map[string]any{"name": "Alex", "pin": "1234"})
	kids := h.makeUser("kids", map[string]any{"name": "Kids"})
	if got := h.call(http.MethodPost, "/api/foyer/admin/pairings", alex.token, map[string]any{"users": []string{alex.id}}); got.status != http.StatusUnauthorized && got.status != http.StatusForbidden {
		t.Fatalf("a user making a code: %d", got.status)
	}
	made := h.call(http.MethodPost, "/api/foyer/admin/pairings", admin, map[string]any{"users": []string{alex.id, kids.id}})
	code, _ := made.body["code"].(string)
	if made.status != http.StatusOK || len(code) != 9 || code[4] != '-' {
		t.Fatalf("a code: %d %v", made.status, made.body)
	}
	device := map[string]any{"installation": "device-hash-1", "name": "Living room TV", "platform": "tvos", "appVersion": "2026.10.1"}
	// Typed with the wrong case, without its dash and with an O for a 0, it is the same code.
	typed := strings.ToLower(strings.ReplaceAll(code, "-", ""))
	typed = strings.ReplaceAll(typed, "0", "o")
	paired := h.call(http.MethodPost, "/api/foyer/pair", "", map[string]any{"code": typed, "device": device})
	users, _ := paired.body["users"].([]any)
	if paired.status != http.StatusOK || len(users) != 2 || paired.body["household"].(map[string]any)["name"] != "Foyer" {
		t.Fatalf("pairing: %d %v", paired.status, paired.body)
	}
	for _, raw := range users {
		u := raw.(map[string]any)
		member := u["member"].(map[string]any)
		token, _ := u["token"].(string)
		// Each session is that user's own, and works.
		if me := h.call(http.MethodGet, "/api/collections/users/records/"+member["id"].(string), token, nil); me.status != http.StatusOK {
			t.Fatalf("%s's session: %d", member["name"], me.status)
		}
		if member["name"] == "Alex" && u["pin"] != "1234" {
			t.Errorf("Alex's PIN came as %v", u["pin"])
		}
	}
	// Used once, it is spent: refused as a code that never was.
	used := h.call(http.MethodPost, "/api/foyer/pair", "", map[string]any{"code": code, "device": device})
	never := h.call(http.MethodPost, "/api/foyer/pair", "", map[string]any{"code": "ABCD-EFGH", "device": device})
	if used.status != http.StatusBadRequest || never.status != http.StatusBadRequest || used.body["message"] != never.body["message"] {
		t.Fatalf("used %d %v; never %d %v", used.status, used.body, never.status, never.body)
	}
	// Ten minutes on, a code no longer does.
	late := h.call(http.MethodPost, "/api/foyer/admin/pairings", admin, map[string]any{"users": []string{alex.id}})
	h.server.Household.Codes.SetClock(func() time.Time { return time.Now().Add(11 * time.Minute) })
	if got := h.call(http.MethodPost, "/api/foyer/pair", "", map[string]any{"code": late.body["code"], "device": device}); got.status != http.StatusBadRequest || got.body["message"] != never.body["message"] {
		t.Fatalf("an old code: %d %v", got.status, got.body)
	}
	// A device that does not say which it is gets nothing.
	h.server.Household.Codes.SetClock(time.Now)
	fresh := h.call(http.MethodPost, "/api/foyer/admin/pairings", admin, map[string]any{"users": []string{alex.id}})
	if got := h.call(http.MethodPost, "/api/foyer/pair", "", map[string]any{"code": fresh.body["code"]}); got.status != http.StatusBadRequest {
		t.Fatalf("no device: %d %v", got.status, got.body)
	}
	// A code for nobody, or for someone who is not there, is never made.
	for _, users := range [][]string{{}, {"nosuchuser12345"}} {
		if got := h.call(http.MethodPost, "/api/foyer/admin/pairings", admin, map[string]any{"users": users}); got.status != http.StatusBadRequest {
			t.Errorf("a code for %v: %d", users, got.status)
		}
	}
	// The device that paired is remembered, with its users.
	household := h.call(http.MethodGet, "/api/foyer/admin/household", admin, nil)
	devices, _ := household.body["devices"].([]any)
	if len(devices) != 1 {
		t.Fatalf("devices %v", household.body)
	}
	d := devices[0].(map[string]any)
	names := []string{}
	for _, n := range d["users"].([]any) {
		names = append(names, n.(string))
	}
	if d["name"] != "Living room TV" || d["paired"] != true || !slices.Equal(names, []string{"Alex", "Kids"}) {
		t.Fatalf("device %v", d)
	}
}

// Pairing is limited to five tries a minute from an address, before the
// catch-all.
func TestPairingIsRateLimited(t *testing.T) {
	h := start(t, options{limits: true})
	labels := []string{}
	for _, rule := range h.app.Settings().RateLimits.Rules {
		labels = append(labels, rule.Label)
	}
	at := slices.Index(labels, "/api/foyer/pair")
	if at < 0 || at > slices.Index(labels, "/api/") {
		t.Fatalf("rules %v", labels)
	}
	device := map[string]any{"installation": "device-hash-1", "name": "TV"}
	limited := false
	for range 7 {
		if got := h.call(http.MethodPost, "/api/foyer/pair", "", map[string]any{"code": "ABCD-EFGH", "device": device}); got.status == http.StatusTooManyRequests {
			limited = true
		}
	}
	if !limited {
		t.Fatal("seven tries in a row, none throttled")
	}
}

// A device that signs a user in says which it is, and is remembered.
func TestADeviceIsRememberedWhenAUserSignsIn(t *testing.T) {
	h := start(t, options{})
	alex := h.makeUser("alex", map[string]any{"name": "Alex"})
	body, _ := json.Marshal(map[string]any{"identity": alex.username, "password": password})
	request := httptest.NewRequest(http.MethodPost, "/api/collections/users/auth-with-password", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Foyer-Device", deviceHeader(t, "device-hash-2", "Alex’s phone"))
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("sign-in: %d %s", recorder.Code, recorder.Body)
	}
	admin := h.superuser()
	household := h.call(http.MethodGet, "/api/foyer/admin/household", admin, nil)
	devices, _ := household.body["devices"].([]any)
	if len(devices) != 1 || devices[0].(map[string]any)["name"] != "Alex’s phone" || devices[0].(map[string]any)["paired"] != false {
		t.Fatalf("devices %v", household.body)
	}
	// Forgotten, it is off the list.
	id := devices[0].(map[string]any)["id"].(string)
	if got := h.call(http.MethodDelete, "/api/foyer/admin/devices/"+id, admin, nil); got.status != http.StatusNoContent {
		t.Fatalf("forgetting: %d", got.status)
	}
}

// The household's collections are Foyer's, as the others are.
func TestTheHouseholdsCollectionsAreFoyers(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	for _, name := range []string{"household", "devices"} {
		if got := h.call(http.MethodPost, "/api/collections/"+name+"/records", admin, map[string]any{"name": "x"}); got.status != http.StatusForbidden {
			t.Errorf("%s: %d %v", name, got.status, got.body)
		}
	}
}

// The household's answers are as the app's fixtures say, key for key: what
// Loge's household adapter reads is what Foyer sends.
func TestTheHouseholdAnswersAsTheAppReadsThem(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	alex := h.makeUser("alex", map[string]any{"name": "Alex", "pin": "1234"})
	var household, pair, signIn struct {
		Answer map[string]any `json:"answer"`
	}
	fixtures.Load(t, "routes/household.json", &household)
	fixtures.Load(t, "routes/pair.json", &pair)
	fixtures.Load(t, "routes/sign-in.json", &signIn)

	got := h.call(http.MethodGet, "/api/foyer/household", alex.token, nil)
	if !sameKeys(got.body, household.Answer) {
		t.Errorf("household %v; the app's %v", got.body, household.Answer)
	}
	member := got.body["members"].([]any)[0].(map[string]any)
	if !sameKeys(member, household.Answer["members"].([]any)[0].(map[string]any)) {
		t.Errorf("a member %v", member)
	}

	code := h.call(http.MethodPost, "/api/foyer/admin/pairings", admin, map[string]any{"users": []string{alex.id}}).body["code"]
	paired := h.call(http.MethodPost, "/api/foyer/pair", "", map[string]any{"code": code, "device": map[string]any{"installation": "device-hash-1"}})
	wantUser := pair.Answer["users"].([]any)[0].(map[string]any)
	gotUser := paired.body["users"].([]any)[0].(map[string]any)
	if !sameKeys(paired.body, pair.Answer) || !sameKeys(gotUser, wantUser) || !sameKeys(gotUser["member"].(map[string]any), wantUser["member"].(map[string]any)) {
		t.Errorf("pairing %v; the app's %v", paired.body, pair.Answer)
	}

	signed := h.signIn("alex", password)
	record := signed.body["record"].(map[string]any)
	for _, key := range []string{"id", "username", "name", "pin"} {
		if _, ok := record[key]; !ok {
			t.Errorf("a sign-in's record has no %s: %v", key, record)
		}
	}
	if !sameKeys(signed.body, signIn.Answer) {
		t.Errorf("sign-in %v; the app's %v", signed.body, signIn.Answer)
	}
}
