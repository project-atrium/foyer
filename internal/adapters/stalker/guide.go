package stalker

import (
	"bytes"
	"encoding/json"
	"time"
)

// Guide is a portal's whole guide — `get_epg_info` for every channel — kept
// as small as it can be: a portal of thousands of channels sends hundreds of
// thousands of programmes, which Foyer holds in memory for every device.
type Guide struct {
	// At is when it was asked for, and Period how many hours it covers.
	At       time.Time
	Period   int
	channels map[string][]entry
}

// Programmes are one channel's, by the portal's id, read as r says.
func (g *Guide) Programmes(channelID string, r Reading) []Programme {
	var out []Programme
	for _, e := range g.channels[channelID] {
		if p, ok := e.programme(r); ok {
			out = append(out, p)
		}
	}
	return out
}

// Size is how many programmes it holds.
func (g *Guide) Size() int {
	n := 0
	for _, each := range g.channels {
		n += len(each)
	}
	return n
}

// ReadGuide reads `get_epg_info`'s answer a programme at a time, keeping of
// each only what is read: as the app reads it, the `data` of `js`, by
// channel — anything else in it, nothing.
func ReadGuide(body []byte) (map[string][]entry, error) {
	d := json.NewDecoder(bytes.NewReader(body))
	channels := map[string][]entry{}
	found := false
	err := eachKey(d, func(key string) error {
		if key != "js" {
			return skip(d)
		}
		found = true
		clear(channels)
		return eachKey(d, func(key string) error {
			if key != "data" {
				return skip(d)
			}
			clear(channels)
			return eachKey(d, func(id string) error {
				entries, err := entries(d)
				if err == nil {
					channels[id] = entries
				}
				return err
			})
		})
	})
	if err != nil || !found {
		return nil, Unreadable()
	}
	return channels, nil
}

// eachKey calls each with every key of the object that comes next, which
// reads its value; anything but an object is read past.
func eachKey(d *json.Decoder, each func(key string) error) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	if t != json.Delim('{') {
		return past(d, t)
	}
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return err
		}
		if err := each(t.(string)); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func entries(d *json.Decoder) ([]entry, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	if t != json.Delim('[') {
		return nil, past(d, t)
	}
	var out []entry
	for d.More() {
		var v any
		if err := d.Decode(&v); err != nil {
			return nil, err
		}
		if e, ok := entryOf(v); ok {
			out = append(out, e)
		}
	}
	_, err = d.Token()
	return out, err
}

func skip(d *json.Decoder) error {
	var v json.RawMessage
	return d.Decode(&v)
}

// past reads past the rest of a value whose first token was read already.
func past(d *json.Decoder, t json.Token) error {
	if t != json.Delim('{') && t != json.Delim('[') {
		return nil
	}
	for depth := 1; depth > 0; {
		t, err := d.Token()
		if err != nil {
			return err
		}
		switch t {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
	}
	return nil
}
