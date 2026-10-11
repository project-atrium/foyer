package userstate

import (
	"encoding/json"
	"strings"
	"testing"

	"foyer/internal/fixtures"
)

// The app's own vectors — what isAccountRecord takes and refuses — judged
// alike here, for every kind a user keeps on Foyer.
func TestRecordsAreJudgedAsTheAppJudgesThem(t *testing.T) {
	var shared struct {
		Valid   []json.RawMessage `json:"valid"`
		Invalid []struct {
			Why    string          `json:"why"`
			Record json.RawMessage `json:"record"`
		} `json:"invalid"`
	}
	fixtures.Load(t, "../account-records.json", &shared)
	ownerOf := func(raw json.RawMessage) (string, string, bool) {
		var r struct {
			Kind    string         `json:"kind"`
			Key     string         `json:"key"`
			Deleted bool           `json:"deleted"`
			Data    map[string]any `json:"data"`
		}
		_ = json.Unmarshal(raw, &r)
		user, _ := r.Data["userId"].(string)
		if user == "" {
			user = strings.Split(r.Key, "/")[0]
		}
		return r.Kind, user, r.Deleted
	}
	judged := 0
	for _, raw := range shared.Valid {
		kind, user, deleted := ownerOf(raw)
		// A user never sends a tombstone of themselves; the app never makes one on a household.
		if !kept(kind) || deleted && (kind == KindProfile || kind == KindPin) {
			continue
		}
		judged++
		if _, ok := read(user, raw); !ok {
			t.Errorf("refused what the app takes: %s", raw)
		}
	}
	for _, v := range shared.Invalid {
		kind, user, _ := ownerOf(v.Record)
		if !kept(kind) {
			continue
		}
		judged++
		if _, ok := read(user, v.Record); ok {
			t.Errorf("took what the app refuses (%s): %s", v.Why, v.Record)
		}
	}
	if judged < 10 {
		t.Fatalf("only %d vectors judged", judged)
	}
}

func TestAUserSendsOnlyTheirOwn(t *testing.T) {
	mine := json.RawMessage(`{"kind":"subscription","key":"s1","deleted":false,"data":{"subscriptionId":"s1","userId":"u1","connectionId":"c1","externalId":"channel:x","title":"X","addedAt":"2026-10-11"}}`)
	if _, ok := read("u1", mine); !ok {
		t.Error("their own subscription was refused")
	}
	if _, ok := read("u2", mine); ok {
		t.Error("another's subscription was taken")
	}
	for _, raw := range []string{
		`{"kind":"preference","key":"u2/homeLayout","deleted":true}`,
		`{"kind":"profile","key":"u1","deleted":true}`,
		`{"kind":"pin","key":"u1","deleted":true}`,
		`{"kind":"connection","key":"c1","deleted":true}`,
		`{"kind":"watchProgress","key":"u1/0123456789abcdef","deleted":true}`,
		`{"kind":"profile","key":"u1","deleted":false,"data":{"userId":"u1","name":"` + strings.Repeat("x", 51) + `"}}`,
	} {
		if _, ok := read("u1", json.RawMessage(raw)); ok {
			t.Errorf("took %s", raw)
		}
	}
	if _, ok := read("u1", json.RawMessage(`{"kind":"preference","key":"u1/homeLayout","deleted":true}`)); !ok {
		t.Error("their own preference's tombstone was refused")
	}
}
