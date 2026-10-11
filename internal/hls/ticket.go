package hls

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// Ticket is what a remux's address carries, in its path — AVPlayer resolves
// a segment's relative address without the playlist's query — signed: the
// file, whose playing it is, until when, and the plan. Any Foyer that holds
// the signing key makes the same remux of it, this one after a restart too.
type Ticket struct {
	File    string `json:"f"`
	User    string `json:"u"`
	Session string `json:"s"`
	Expires int64  `json:"e"`
	Plan    Plan   `json:"p"`
}

// seal is the ticket as a path segment: its JSON and its signature.
func (t Ticket) seal(sign func([]byte) ([]byte, error)) (string, error) {
	payload, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	mac, err := sign(payload)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac[:16]), nil
}

// openTicket reads a sealed ticket, if it is one and has not expired.
func openTicket(sealed string, sign func([]byte) ([]byte, error), now time.Time) (Ticket, bool) {
	encoded, sig, ok := strings.Cut(sealed, ".")
	if !ok {
		return Ticket{}, false
	}
	payload, err1 := base64.RawURLEncoding.DecodeString(encoded)
	given, err2 := base64.RawURLEncoding.DecodeString(sig)
	if err1 != nil || err2 != nil || len(given) != 16 {
		return Ticket{}, false
	}
	want, err := sign(payload)
	if err != nil || !hmac.Equal(want[:16], given) {
		return Ticket{}, false
	}
	var t Ticket
	if err := json.Unmarshal(payload, &t); err != nil || t.File == "" || t.Session == "" || now.Unix() > t.Expires {
		return Ticket{}, false
	}
	return t, true
}
