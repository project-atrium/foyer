package userstate

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"unicode/utf16"
)

// The checks are @loge/api's isAccountRecord, ported for the kinds a user
// keeps here, and measured as it measures: in UTF-16 code units. The app's
// account-records.json holds both to the same answers.

const (
	maxRecordLength = 256 * 1024
	maxID           = 128
	maxText         = 200
	maxDepth        = 32
	maxList         = 2_000
	maxLogo         = 2_048
	// A user's name on Foyer: what their own record takes.
	maxName = 50
)

var (
	keyPattern = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
	pinPattern = regexp.MustCompile(`^[0-9]{4}$`)
)

func units(s string) int { return len(utf16.Encode([]rune(s))) }

// isID is an id: joined into keys with a slash, so it never holds one.
func isID(v any) bool {
	s, ok := v.(string)
	return ok && s != "" && units(s) <= maxID && !strings.Contains(s, "/")
}

func isText(v any) bool {
	s, ok := v.(string)
	return ok && units(s) <= maxText
}

func isKey(v any) bool {
	s, ok := v.(string)
	return ok && units(s) <= maxID && keyPattern.MatchString(s)
}

func isFilled(v any) bool {
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) != ""
}

func isJSON(v any, depth int) bool {
	if depth > maxDepth {
		return false
	}
	switch x := v.(type) {
	case nil, string, bool:
		return true
	case float64:
		return !math.IsInf(x, 0) && !math.IsNaN(x)
	case []any:
		for _, e := range x {
			if !isJSON(e, depth+1) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, e := range x {
			if !isJSON(e, depth+1) {
				return false
			}
		}
		return true
	}
	return false
}

func isMediaKey(v any) bool {
	m, ok := v.(map[string]any)
	return ok && isID(m["connectionId"]) && isID(m["externalId"])
}

func absentOr(v any, present bool, check func(any) bool) bool {
	return !present || check(v)
}

// keyOf is recordKey: the key a record of kind with this data has.
func keyOf(kind string, data map[string]any) string {
	str := func(name string) string { s, _ := data[name].(string); return s }
	switch kind {
	case KindProfile, KindPin:
		return str("userId")
	case KindPreference:
		return str("userId") + "/" + str("name")
	case KindSubscription:
		return str("subscriptionId")
	case KindPlaylist:
		return str("playlistId")
	case KindFavoriteChannel:
		return str("favoriteId")
	}
	return ""
}

// validData says whether data is a record of kind the app's contract allows.
func validData(kind string, data map[string]any) bool {
	has := func(name string) bool { _, ok := data[name]; return ok }
	switch kind {
	case KindProfile:
		name, _ := data["name"].(string)
		return isID(data["userId"]) && isText(data["name"]) && isFilled(data["name"]) && units(name) <= maxName
	case KindPin:
		pin, isString := data["pin"].(string)
		return isID(data["userId"]) && has("pin") && (data["pin"] == nil || isString && pinPattern.MatchString(pin))
	case KindPreference:
		return isID(data["userId"]) && isKey(data["name"]) && has("value") && isJSON(data["value"], 0)
	case KindSubscription:
		return isID(data["subscriptionId"]) && isID(data["userId"]) && isID(data["connectionId"]) && isID(data["externalId"]) &&
			isText(data["title"]) && isText(data["addedAt"])
	case KindPlaylist:
		items, isList := data["items"].([]any)
		if !isList || len(items) > maxList {
			return false
		}
		for _, item := range items {
			if !isMediaKey(item) {
				return false
			}
		}
		return isID(data["playlistId"]) && isID(data["userId"]) && isText(data["title"]) && isFilled(data["title"]) &&
			absentOr(data["description"], has("description"), isText) &&
			absentOr(data["source"], has("source"), isMediaKey) &&
			isText(data["createdAt"]) && isText(data["updatedAt"])
	case KindFavoriteChannel:
		number := func(v any) bool { n, ok := v.(float64); return ok && n == math.Trunc(n) && n > 0 }
		logo := func(v any) bool { s, ok := v.(string); return ok && s != "" && units(s) <= maxLogo }
		return isID(data["favoriteId"]) && isID(data["userId"]) && isID(data["connectionId"]) && isID(data["externalId"]) &&
			isText(data["name"]) && isFilled(data["name"]) &&
			absentOr(data["number"], has("number"), number) &&
			absentOr(data["logo"], has("logo"), logo) &&
			isText(data["addedAt"])
	}
	return false
}

// validKey says whether a tombstone's key has its kind's shape.
func validKey(kind, key string) bool {
	parts := strings.Split(key, "/")
	if kind == KindPreference {
		return len(parts) == 2 && isID(parts[0]) && isKey(parts[1])
	}
	return len(parts) == 1 && isID(parts[0])
}

// sent is a record as a device sent it.
type sent struct {
	Kind    string          `json:"kind"`
	Key     string          `json:"key"`
	Deleted *bool           `json:"deleted"`
	Data    json.RawMessage `json:"data"`
}

// Record is a record read and judged: the user's own, of a kind kept here.
type Record struct {
	Kind    string
	Key     string
	Deleted bool
	Data    map[string]any
}

// read judges one record a user sent: it must be one the app's contract
// allows, of a kind a household keeps, and the user's own — by its body, or
// by its key where a tombstone has none. Never a tombstone of themselves.
func read(user string, raw json.RawMessage) (Record, bool) {
	if units(string(raw)) > maxRecordLength {
		return Record{}, false
	}
	var s sent
	if err := json.Unmarshal(raw, &s); err != nil || s.Deleted == nil || !kept(s.Kind) {
		return Record{}, false
	}
	r := Record{Kind: s.Kind, Key: s.Key, Deleted: *s.Deleted}
	if r.Deleted {
		if r.Kind == KindProfile || r.Kind == KindPin || !validKey(r.Kind, r.Key) {
			return Record{}, false
		}
		// A preference names its user in its key; the rest are ids of the user's own rows.
		return r, r.Kind != KindPreference || strings.HasPrefix(r.Key, user+"/")
	}
	if err := json.Unmarshal(s.Data, &r.Data); err != nil || r.Data == nil {
		return Record{}, false
	}
	if !validData(r.Kind, r.Data) || keyOf(r.Kind, r.Data) != r.Key || r.Data["userId"] != user {
		return Record{}, false
	}
	return r, true
}
