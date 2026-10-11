// Package household is the household as a device meets it: its name, its
// members — the users, each a profile — the devices they are signed in on,
// and the codes the superuser makes so a device can sign several of them in
// at once.
//
// A user reads only itself: `users`' rules stay `id = @request.auth.id`.
// What a device may know of the others — a name, a picture, whether a PIN
// locks them — comes through this package's route, never the record API.
package household

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

const (
	Collection        = "household"
	DevicesCollection = "devices"
	// codeLifetime is how long a pairing code lasts: long enough to walk to
	// the television and type it.
	codeLifetime = 10 * time.Minute
	// Crockford's base32: no I, L, O or U, so a code read off one screen and
	// typed into another survives.
	alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	// maxUsers is how many users one code signs in: a device holds ten
	// profiles at most.
	maxUsers = 10
)

// ErrUnusable covers every reason alike — unknown, used, expired — so the
// answer tells nobody which codes exist.
var ErrUnusable = errors.New("that code will not do: it may be mistyped, used already, or older than ten minutes")

// Household is the household's own record: its id, which a device keys what
// Foyer serves by, and its name.
type Household struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Get is the household. The migration that made the collection made its one
// record.
func Get(app core.App) (Household, error) {
	records, err := app.FindRecordsByFilter(Collection, "", "created", 1, 0)
	if err != nil {
		return Household{}, err
	}
	if len(records) == 0 {
		return Household{}, errors.New("the household's record is missing")
	}
	return Household{ID: records[0].Id, Name: records[0].GetString("name")}, nil
}

// Member is a user as the rest of the household may see them: never a PIN,
// an email or anything of their own beyond their name and picture.
type Member struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	// Avatar is the address of their picture on this server, or empty.
	Avatar string `json:"avatar,omitempty"`
	HasPin bool   `json:"hasPin"`
}

func memberOf(r *core.Record) Member {
	m := Member{ID: r.Id, Username: r.GetString("username"), Name: r.GetString("name"), HasPin: r.GetString("pin") != ""}
	if file := r.GetString("avatar"); file != "" {
		m.Avatar = "/api/files/" + r.Collection().Id + "/" + r.Id + "/" + file + "?thumb=256x256"
	}
	return m
}

// Members are every user of the household, by name.
func Members(app core.App) ([]Member, error) {
	records, err := app.FindRecordsByFilter("users", "", "name,username", 0, 0)
	if err != nil {
		return nil, err
	}
	members := make([]Member, 0, len(records))
	for _, r := range records {
		members = append(members, memberOf(r))
	}
	return members, nil
}

// Device is what a device says of itself to Foyer.
type Device struct {
	// Installation is its id here: a hash of its own key, the same for every
	// user and adapter on it, and nothing anyone could sign in with.
	Installation string `json:"installation"`
	Name         string `json:"name"`
	Platform     string `json:"platform"`
	AppVersion   string `json:"appVersion"`
}

var installationPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,100}$`)

func (d Device) valid() bool { return installationPattern.MatchString(d.Installation) }

func (d Device) clean() Device {
	clip := func(s string, n int) string {
		s = strings.TrimSpace(s)
		if len(s) > n {
			s = s[:n]
		}
		return s
	}
	return Device{Installation: d.Installation, Name: clip(d.Name, 100), Platform: clip(d.Platform, 20), AppVersion: clip(d.AppVersion, 40)}
}

// DeviceHeader carries a device's own description on the calls that sign
// in: base64url of its JSON, so a name in any script survives a header.
const DeviceHeader = "X-Foyer-Device"

// deviceOf reads the device a request says it came from.
func deviceOf(r *http.Request) (Device, bool) {
	raw := r.Header.Get(DeviceHeader)
	if raw == "" || len(raw) > 2048 {
		return Device{}, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(raw, "="))
	if err != nil {
		return Device{}, false
	}
	var d Device
	if err := json.Unmarshal(decoded, &d); err != nil || !d.valid() {
		return Device{}, false
	}
	return d.clean(), true
}

// remember keeps a device, and a user on it, and when it was last seen.
func remember(app core.App, d Device, user string, paired bool, now time.Time) error {
	record, err := app.FindFirstRecordByData(DevicesCollection, "installation", d.Installation)
	if err != nil {
		collection, err := app.FindCollectionByNameOrId(DevicesCollection)
		if err != nil {
			return err
		}
		record = core.NewRecord(collection)
		record.Set("installation", d.Installation)
	}
	record.Set("name", d.Name)
	record.Set("platform", d.Platform)
	record.Set("app_version", d.AppVersion)
	if users := record.GetStringSlice("users"); !slices.Contains(users, user) {
		record.Set("users", append(users, user))
	}
	if paired {
		record.Set("paired", true)
	}
	record.Set("last_seen", now.UTC())
	return app.Save(record)
}

// Codes keeps the pairing codes the superuser makes, in memory: one lasts
// minutes, and a server that starts again simply has none.
type Codes struct {
	now   func() time.Time
	mu    sync.Mutex
	codes map[string]pending
}

type pending struct {
	users   []string
	expires time.Time
}

func NewCodes() *Codes {
	return &Codes{now: time.Now, codes: map[string]pending{}}
}

// SetClock sets what the codes take for now: a test's clock.
func (c *Codes) SetClock(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// Make is a code that signs these users in on one device, once, within ten
// minutes. Only its hash is kept.
func (c *Codes) Make(users []string) (string, time.Time, error) {
	var raw [5]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", time.Time{}, err
	}
	var code strings.Builder
	var buffer, bits uint
	for _, b := range raw {
		buffer = buffer<<8 | uint(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			code.WriteByte(alphabet[(buffer>>bits)&31])
			if code.Len() == 4 {
				code.WriteByte('-')
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, p := range c.codes {
		if !now.Before(p.expires) {
			delete(c.codes, k)
		}
	}
	expires := now.Add(codeLifetime).UTC().Truncate(time.Second)
	c.codes[hash(code.String())] = pending{users: slices.Clone(users), expires: expires}
	return code.String(), expires, nil
}

// redeem is the users a code signs in — once: a code is spent the moment it
// is used, whatever comes after.
func (c *Codes) redeem(code string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := hash(code)
	p, ok := c.codes[key]
	if !ok {
		return nil, ErrUnusable
	}
	delete(c.codes, key)
	if !c.now().Before(p.expires) {
		return nil, ErrUnusable
	}
	return p.users, nil
}

func hash(code string) string {
	sum := sha256.Sum256([]byte(Normalize(code)))
	return hex.EncodeToString(sum[:])
}

// Normalize reads a code however it was typed: case, dashes and spaces do not
// matter, and O, I and L read as 0, 1 and 1.
func Normalize(code string) string {
	var out strings.Builder
	for _, r := range strings.ToUpper(code) {
		switch {
		case r == 'O':
			out.WriteByte('0')
		case r == 'I', r == 'L':
			out.WriteByte('1')
		case strings.ContainsRune(alphabet, r):
			out.WriteRune(r)
		}
	}
	return out.String()
}

// usersByID are the users of these ids, every one of them, or an error.
func usersByID(app core.App, ids []string) ([]*core.Record, error) {
	if len(ids) == 0 || len(ids) > maxUsers {
		return nil, errors.New("a code signs in one to ten users")
	}
	records, err := app.FindRecordsByIds("users", ids)
	if err != nil {
		return nil, err
	}
	if len(records) != len(slices.Compact(slices.Sorted(slices.Values(ids)))) {
		return nil, errors.New("one of those users does not exist")
	}
	return records, nil
}

// devicesView is the devices as the dashboard lists them, the latest seen
// first.
func devicesView(app core.App) ([]map[string]any, error) {
	records, err := app.FindRecordsByFilter(DevicesCollection, "", "-last_seen", 0, 0)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(records))
	for _, r := range records {
		names := []string{}
		if users, err := app.FindRecordsByIds("users", r.GetStringSlice("users")); err == nil {
			for _, u := range users {
				names = append(names, u.GetString("name"))
			}
			slices.Sort(names)
		}
		out = append(out, map[string]any{
			"id": r.Id, "name": r.GetString("name"), "platform": r.GetString("platform"), "appVersion": r.GetString("app_version"),
			"users": names, "paired": r.GetBool("paired"), "lastSeen": r.GetDateTime("last_seen").Time(),
		})
	}
	return out, nil
}
