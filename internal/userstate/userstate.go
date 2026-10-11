// Package userstate is what each user keeps for themselves on Foyer — their
// preferences, the channels they follow, their lists and their ★ — written
// by their devices as records (@loge/api's AccountRecord) and read back by
// every device they sign in on, in the order Foyer stored them. Their name
// and PIN arrive the same way, and go onto their own user record.
package userstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// The kinds of record a user keeps here, as the app names them.
const (
	KindProfile         = "profile"
	KindPin             = "pin"
	KindPreference      = "preference"
	KindSubscription    = "subscription"
	KindPlaylist        = "playlist"
	KindFavoriteChannel = "favoriteChannel"
)

// Collections hold each kind a user keeps, in the order a read lists them.
var Collections = map[string]string{
	KindPreference:      "preferences",
	KindSubscription:    "subscriptions",
	KindPlaylist:        "playlists",
	KindFavoriteChannel: "favorite_channels",
}

// stateKinds are the kinds kept in collections of their own, in a steady order.
var stateKinds = []string{KindPreference, KindSubscription, KindPlaylist, KindFavoriteChannel}

func kept(kind string) bool {
	_, inCollection := Collections[kind]
	return inCollection || kind == KindProfile || kind == KindPin
}

// TombstonesKept is how long a delete is kept for a device to learn of: one
// away longer reads everything again.
const TombstonesKept = 90 * 24 * time.Hour

const seqTable = "_foyer_seq"

// Refusal is why a push was refused, and which of its records stopped it:
// nothing of the batch was stored.
type Refusal struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// Push stores a user's records — all of them, or none — each with the next
// seq, in the order they came: a refusal names the first that will not do.
// Their name and PIN go onto their own record.
func Push(app core.App, user string, raws []json.RawMessage) (int64, *Refusal, error) {
	records := make([]Record, len(raws))
	for i, raw := range raws {
		r, ok := read(user, raw)
		if !ok {
			return 0, &Refusal{Index: i, Reason: "invalid"}, nil
		}
		records[i] = r
	}
	var last int64
	err := app.RunInTransaction(func(tx core.App) error {
		stored := 0
		for _, r := range records {
			if _, inCollection := Collections[r.Kind]; inCollection {
				stored++
			}
		}
		// The first write of the transaction: SQLite's one writer holds it until the commit, so seqs are in commit order.
		if err := tx.DB().NewQuery("UPDATE {{" + seqTable + "}} SET [[value]] = [[value]] + {:n} WHERE [[name]] = 'state' RETURNING [[value]]").
			Bind(dbx.Params{"n": stored}).Row(&last); err != nil {
			return fmt.Errorf("the seq: %w", err)
		}
		seq := last - int64(stored)
		for _, r := range records {
			switch r.Kind {
			case KindProfile, KindPin:
				if err := own(tx, user, r); err != nil {
					return err
				}
				continue
			}
			seq++
			if err := store(tx, user, r, seq); err != nil {
				return err
			}
		}
		return nil
	})
	return last, nil, err
}

// own writes a user's name or PIN onto their own record.
func own(tx core.App, user string, r Record) error {
	record, err := tx.FindRecordById("users", user)
	if err != nil {
		return err
	}
	if r.Kind == KindProfile {
		record.Set("name", r.Data["name"])
	} else {
		pin, _ := r.Data["pin"].(string)
		record.Set("pin", pin)
	}
	return tx.Save(record)
}

// store writes one record onto its row — made if it was never kept — or
// makes the row a tombstone.
func store(tx core.App, user string, r Record, seq int64) error {
	collection := Collections[r.Kind]
	row, err := tx.FindFirstRecordByFilter(collection, "user = {:user} && key = {:key}", dbx.Params{"user": user, "key": r.Key})
	if err != nil {
		c, err := tx.FindCollectionByNameOrId(collection)
		if err != nil {
			return err
		}
		row = core.NewRecord(c)
		row.Set("user", user)
		row.Set("key", r.Key)
	}
	row.Set("deleted", r.Deleted)
	row.Set("seq", seq)
	if !r.Deleted {
		fieldsOf(r, row)
	}
	return tx.Save(row)
}

// fieldsOf lays a record's data on its row, in Foyer's spelling.
func fieldsOf(r Record, row *core.Record) {
	d := r.Data
	switch r.Kind {
	case KindPreference:
		row.Set("name", d["name"])
		row.Set("value", d["value"])
	case KindSubscription:
		row.Set("connection", d["connectionId"])
		row.Set("external_id", d["externalId"])
		row.Set("title", d["title"])
		row.Set("added_at", d["addedAt"])
	case KindPlaylist:
		row.Set("title", d["title"])
		row.Set("description", d["description"])
		row.Set("items", d["items"])
		row.Set("source", d["source"])
		row.Set("created_at", d["createdAt"])
		row.Set("updated_at", d["updatedAt"])
	case KindFavoriteChannel:
		row.Set("connection", d["connectionId"])
		row.Set("external_id", d["externalId"])
		row.Set("name", d["name"])
		row.Set("number", d["number"])
		row.Set("logo", d["logo"])
		row.Set("added_at", d["addedAt"])
	}
}

// recordOf is a row as the app's record, or its tombstone.
func recordOf(kind string, user string, row *core.Record) map[string]any {
	out := map[string]any{"kind": kind, "key": row.GetString("key"), "deleted": row.GetBool("deleted")}
	if row.GetBool("deleted") {
		return out
	}
	data := map[string]any{"userId": user}
	text := func(name, field string) {
		data[name] = row.GetString(field)
	}
	optional := func(name, field string) {
		if v := row.GetString(field); v != "" {
			data[name] = v
		}
	}
	switch kind {
	case KindPreference:
		data["name"] = row.GetString("name")
		var value any
		_ = row.UnmarshalJSONField("value", &value)
		data["value"] = value
	case KindSubscription:
		data["subscriptionId"] = row.GetString("key")
		text("connectionId", "connection")
		text("externalId", "external_id")
		text("title", "title")
		text("addedAt", "added_at")
	case KindPlaylist:
		data["playlistId"] = row.GetString("key")
		text("title", "title")
		optional("description", "description")
		items := []any{}
		_ = row.UnmarshalJSONField("items", &items)
		data["items"] = items
		var source map[string]any
		if row.UnmarshalJSONField("source", &source) == nil && source != nil {
			data["source"] = source
		}
		text("createdAt", "created_at")
		text("updatedAt", "updated_at")
	case KindFavoriteChannel:
		data["favoriteId"] = row.GetString("key")
		text("connectionId", "connection")
		text("externalId", "external_id")
		text("name", "name")
		if n := row.GetInt("number"); n > 0 {
			data["number"] = n
		}
		optional("logo", "logo")
		text("addedAt", "added_at")
	}
	out["data"] = data
	return out
}

// Page is what changed in a user's state since a cursor.
type Page struct {
	Records []map[string]any `json:"records"`
	Seq     int64            `json:"seq"`
	More    bool             `json:"more,omitzero"`
	Reset   bool             `json:"reset,omitzero"`
}

// ErrNoSeq is the seq table missing: the migrations have not run.
var ErrNoSeq = errors.New("the seq table is missing")

func seqOf(app core.App, name string) (int64, error) {
	var value int64
	if err := app.DB().NewQuery("SELECT [[value]] FROM {{" + seqTable + "}} WHERE [[name]] = {:name}").Bind(dbx.Params{"name": name}).Row(&value); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrNoSeq, err)
	}
	return value, nil
}

// Changes is a page of what changed in a user's state after a seq, in the
// order it was stored, tombstones too — or a reset, where deletes since were
// let go of. After 0 is everything Foyer keeps.
func Changes(app core.App, user string, after int64, limit int) (Page, error) {
	// Read first: a write committing after it has a seq past it, and comes with the next read.
	now, err := seqOf(app, "state")
	if err != nil {
		return Page{}, err
	}
	pruned, err := seqOf(app, "pruned")
	if err != nil {
		return Page{}, err
	}
	// Deletes since were let go of — or this Foyer went back to an older copy of itself, and lost what it
	// had stored since: either way, the device reads everything again.
	if after > 0 && (after < pruned || after > now) {
		return Page{Records: []map[string]any{}, Reset: true}, nil
	}
	type row struct {
		kind string
		rec  *core.Record
	}
	var rows []row
	for _, kind := range stateKinds {
		found, err := app.FindRecordsByFilter(Collections[kind], "user = {:user} && seq > {:after} && seq <= {:now}", "seq", limit+1, 0,
			dbx.Params{"user": user, "after": after, "now": now})
		if err != nil {
			return Page{}, err
		}
		for _, r := range found {
			rows = append(rows, row{kind, r})
		}
	}
	slices.SortFunc(rows, func(a, b row) int { return a.rec.GetInt("seq") - b.rec.GetInt("seq") })
	page := Page{Records: []map[string]any{}, Seq: now}
	if len(rows) > limit {
		rows = rows[:limit]
		page.More = true
		page.Seq = int64(rows[len(rows)-1].rec.GetInt("seq"))
	}
	for _, r := range rows {
		page.Records = append(page.Records, recordOf(r.kind, user, r.rec))
	}
	return page, nil
}

// Prune lets go of tombstones older than before: a device whose cursor is
// older than the newest of them reads everything again.
func Prune(app core.App, before time.Time) (int, error) {
	removed := 0
	err := app.RunInTransaction(func(tx core.App) error {
		var newest int64
		for _, kind := range stateKinds {
			old, err := tx.FindRecordsByFilter(Collections[kind], "deleted = true && updated < {:before}", "", 0, 0,
				dbx.Params{"before": before.UTC().Format(types.DefaultDateLayout)})
			if err != nil {
				return err
			}
			for _, r := range old {
				newest = max(newest, int64(r.GetInt("seq")))
				if err := tx.Delete(r); err != nil {
					return err
				}
				removed++
			}
		}
		if removed == 0 {
			return nil
		}
		_, err := tx.DB().NewQuery("UPDATE {{" + seqTable + "}} SET [[value]] = MAX([[value]], {:seq}) WHERE [[name]] = 'pruned'").
			Bind(dbx.Params{"seq": newest}).Execute()
		return err
	})
	return removed, err
}
