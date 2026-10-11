package server_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"foyer/internal/fixtures"
	"foyer/internal/userstate"
)

// The app's push fixture, as one user would send it: their own id in place of
// the fixture's.
func pushOf(t *testing.T, name, user string) (records []any, answer map[string]any) {
	t.Helper()
	var f struct {
		Request struct {
			Records json.RawMessage `json:"records"`
		} `json:"request"`
		Answer map[string]any `json:"answer"`
	}
	fixtures.Load(t, name, &f)
	raw := strings.ReplaceAll(string(f.Request.Records), "a1b2c3d4e5f6g7h", user)
	if err := json.Unmarshal([]byte(raw), &records); err != nil {
		t.Fatal(err)
	}
	return records, f.Answer
}

func (h *harness) changes(u user, after int64) map[string]any {
	h.t.Helper()
	got := h.call(http.MethodGet, "/api/foyer/me/changes?after="+strconv.FormatInt(after, 10), u.token, nil)
	if got.status != http.StatusOK {
		h.t.Fatalf("changes: %d %v", got.status, got.body)
	}
	return got.body
}

// A user's own records, pushed from one device, are what every device of
// theirs reads back — in the order they were stored, as the app reads them —
// and their name and PIN go onto their own record.
func TestAUsersOwnStateTravels(t *testing.T) {
	h := start(t, options{})
	alex, sam := h.makeUser("alex", map[string]any{"name": "Alex"}), h.makeUser("sam", nil)
	records, answer := pushOf(t, "routes/push.json", alex.id)
	got := h.call(http.MethodPost, "/api/foyer/me/push", alex.token, map[string]any{"records": records})
	if got.status != http.StatusOK {
		t.Fatalf("push: %d %v", got.status, got.body)
	}
	fits(t, "push", got.body, answer)
	record, err := h.app.FindRecordById("users", alex.id)
	if err != nil || record.GetString("name") != "Alexandra" || record.GetString("pin") != "4321" {
		t.Errorf("alex's own record: %v %v", record, err)
	}

	read := h.changes(alex, 0)
	var shared struct {
		Answer map[string]any `json:"answer"`
	}
	fixtures.Load(t, "routes/changes.json", &shared)
	fits(t, "changes", read, shared.Answer)
	kinds := []string{}
	for _, r := range read["records"].([]any) {
		kinds = append(kinds, r.(map[string]any)["kind"].(string))
	}
	if strings.Join(kinds, " ") != "preference subscription playlist favoriteChannel" {
		t.Errorf("read back %v", kinds)
	}
	seq := int64(read["seq"].(float64))
	if seq != int64(got.body["seq"].(float64)) {
		t.Errorf("read at %d, stored at %v", seq, got.body["seq"])
	}
	if again := h.changes(alex, seq); len(again["records"].([]any)) != 0 || int64(again["seq"].(float64)) != seq {
		t.Errorf("nothing new reads as %v", again)
	}
	// Exactly as sent: what the app pushed is what it reads.
	sentPlaylist, _ := json.Marshal(records[4])
	readPlaylist, _ := json.Marshal(read["records"].([]any)[2])
	var a, b any
	_ = json.Unmarshal(sentPlaylist, &a)
	_ = json.Unmarshal(readPlaylist, &b)
	if !jsonEqual(a, b) {
		t.Errorf("the playlist came back as\n%s\nnot\n%s", readPlaylist, sentPlaylist)
	}

	// Sam reads nothing of Alex's — through Foyer, or PocketBase's own routes.
	if theirs := h.changes(sam, 0); len(theirs["records"].([]any)) != 0 {
		t.Errorf("sam reads %v", theirs)
	}
	if got := h.call(http.MethodGet, "/api/collections/subscriptions/records", sam.token, nil); len(got.body["items"].([]any)) != 0 {
		t.Errorf("sam lists %v", got.body)
	}
	if got := h.call(http.MethodGet, "/api/collections/subscriptions/records", alex.token, nil); len(got.body["items"].([]any)) != 1 {
		t.Errorf("alex lists %v", got.body)
	}
	// Only Foyer writes them.
	if got := h.call(http.MethodPost, "/api/collections/preferences/records", alex.token, map[string]any{"user": alex.id, "key": alex.id + "/x"}); got.status != http.StatusForbidden {
		t.Errorf("a direct write: %d", got.status)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// A batch is stored whole or not at all: the refusal names the record that
// stopped it, and nothing of the rest was kept.
func TestAPushIsWholeOrNothing(t *testing.T) {
	h := start(t, options{})
	alex := h.makeUser("alex", nil)
	before := int64(h.changes(alex, 0)["seq"].(float64))
	records, _ := pushOf(t, "routes/push.json", alex.id)
	theirs := map[string]any{"kind": "preference", "key": "someone0000000/x", "deleted": false,
		"data": map[string]any{"userId": "someone0000000", "name": "x", "value": true}}
	got := h.call(http.MethodPost, "/api/foyer/me/push", alex.token, map[string]any{"records": append(records, theirs)})
	var shared struct {
		Answer map[string]any `json:"answer"`
	}
	fixtures.Load(t, "routes/push-refused.json", &shared)
	if got.status != http.StatusUnprocessableEntity {
		t.Fatalf("refused: %d %v", got.status, got.body)
	}
	fits(t, "a refusal", got.body, shared.Answer)
	if refused := got.body["refused"].(map[string]any); refused["index"] != float64(len(records)) || refused["reason"] != "invalid" {
		t.Errorf("refused %v", refused)
	}
	if after := h.changes(alex, 0); len(after["records"].([]any)) != 0 || int64(after["seq"].(float64)) != before {
		t.Errorf("a refused batch kept %v", after)
	}
	if record, _ := h.app.FindRecordById("users", alex.id); record.GetString("name") == "Alexandra" {
		t.Error("a refused batch renamed alex")
	}
	if got := h.call(http.MethodPost, "/api/foyer/me/push", "", map[string]any{"records": records}); got.status != http.StatusUnauthorized {
		t.Errorf("a guest: %d", got.status)
	}
}

// A delete reaches every device as a tombstone, a page at a time — and once
// deletes are let go of, a device that missed them reads everything again,
// as it does from a Foyer that went back to an older copy of itself.
func TestDeletesReachEveryDeviceThenReset(t *testing.T) {
	h := start(t, options{})
	alex := h.makeUser("alex", nil)
	follow := func(id string) map[string]any {
		return map[string]any{"kind": "subscription", "key": id, "deleted": false, "data": map[string]any{
			"subscriptionId": id, "userId": alex.id, "connectionId": "served-1", "externalId": "channel:" + id, "title": id, "addedAt": "2026-10-11"}}
	}
	push := func(records ...map[string]any) int64 {
		got := h.call(http.MethodPost, "/api/foyer/me/push", alex.token, map[string]any{"records": records})
		if got.status != http.StatusOK {
			t.Fatalf("push: %d %v", got.status, got.body)
		}
		return int64(got.body["seq"].(float64))
	}
	push(follow("a"), follow("b"), follow("c"))
	cursor := h.changes(alex, 0)["seq"].(float64)
	push(map[string]any{"kind": "subscription", "key": "b", "deleted": true})
	read := h.changes(alex, int64(cursor))
	if got := read["records"].([]any); len(got) != 1 || got[0].(map[string]any)["deleted"] != true || got[0].(map[string]any)["key"] != "b" {
		t.Fatalf("the delete reads as %v", read)
	}

	// A page at a time, in the order last stored: a row written again moves to the end.
	first, err := userstate.Changes(h.app, alex.id, 0, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Records) != 2 || !first.More || first.Records[0]["key"] != "a" || first.Records[1]["key"] != "c" {
		t.Fatalf("the first page %+v", first)
	}
	second, err := userstate.Changes(h.app, alex.id, first.Seq, 0, 2)
	if err != nil || len(second.Records) != 1 || second.More || second.Records[0]["key"] != "b" || second.Records[0]["deleted"] != true {
		t.Fatalf("the second page %+v %v", second, err)
	}

	// A month of deletes kept, then let go of.
	if _, err := h.app.DB().NewQuery("UPDATE subscriptions SET updated = '2026-01-01 00:00:00.000Z' WHERE deleted = TRUE").Execute(); err != nil {
		t.Fatal(err)
	}
	removed, err := userstate.Prune(h.app, time.Now().Add(-userstate.TombstonesKept))
	if err != nil || removed != 1 {
		t.Fatalf("pruned %d: %v", removed, err)
	}
	if missed := h.changes(alex, int64(cursor)); missed["reset"] != true {
		t.Errorf("a device that missed a let-go delete reads %v", missed)
	}
	everything := h.changes(alex, 0)
	if got := everything["records"].([]any); len(got) != 2 || everything["reset"] != nil {
		t.Errorf("everything is %v", everything)
	}
	if ahead := h.changes(alex, 1_000_000); ahead["reset"] != true {
		t.Errorf("a cursor past what Foyer has reads %v", ahead)
	}
	// A read of everything pages on past what was let go of: only where a read began decides a reset.
	for _, id := range []string{"d", "e", "f"} {
		push(follow(id))
	}
	page, err := userstate.Changes(h.app, alex.id, 0, 0, 1)
	if err != nil || !page.More {
		t.Fatalf("the first page of everything %+v %v", page, err)
	}
	if next, err := userstate.Changes(h.app, alex.id, page.Seq, 0, 1); err != nil || next.Reset || len(next.Records) != 1 {
		t.Errorf("a later page of everything reads %+v %v", next, err)
	}
	if got := h.call(http.MethodGet, "/api/foyer/me/changes?after=1&from=2", alex.token, nil); got.status != http.StatusBadRequest {
		t.Errorf("a read that began after its own page: %d", got.status)
	}
	// A user gone takes everything of theirs.
	record, _ := h.app.FindRecordById("users", alex.id)
	if err := h.app.Delete(record); err != nil {
		t.Fatal(err)
	}
	if n, _ := h.app.CountRecords("subscriptions"); n != 0 {
		t.Errorf("%d subscriptions outlived their user", n)
	}
}
