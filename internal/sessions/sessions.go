// Package sessions keeps what is being played now, and what was. Every
// address Foyer hands out belongs to a playing — a film or an episode
// watched, a copy downloaded — and a superuser can stop one: its streams end
// at once, and its addresses are refused for as long as they could have
// lasted, across a restart too. A playing stays alive while its bytes flow or
// its player reports: a phone that sleeps through AirPlay or picture in
// picture sends nothing and still pulls bytes, so quiet is only shown, and a
// playing long quiet with nothing open is let go — never refused: its next
// request brings it back.
package sessions

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/pocketbase/pocketbase/tools/types"

	"foyer/internal/household"
)

// The kinds of playing.
const (
	KindPlay     = "play"
	KindDownload = "download"
)

// Collection is the history of playings that ended, and of those stopped.
const Collection = "play_history"

const (
	// QuietAfter is how long a playing goes without a byte or a report before
	// it is shown as quiet: paused on a phone that sleeps, or gone.
	QuietAfter = 2 * time.Minute
	// GoneAfter is how long a playing with no stream open stays quiet before
	// it is let go: its player closed without a word, or paused a long while.
	GoneAfter = 30 * time.Minute
	// downloadDone is how long after its last byte a download has ended.
	downloadDone = 2 * time.Minute
	// rateWindow is how far back a playing's bitrate is measured, in seconds.
	rateWindow = 10
)

// Device is where a playing plays, as the device said.
type Device struct {
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	AppVersion string `json:"appVersion,omitempty"`
}

// DeviceOf is the device a request says it came from: none where it says
// nothing — a player's own requests never do.
func DeviceOf(r *http.Request) Device {
	d, ok := household.DeviceOf(r)
	if !ok {
		return Device{}
	}
	return Device{Name: d.Name, Platform: d.Platform, AppVersion: d.AppVersion}
}

// Item is what a playing plays, as the dashboard shows it.
type Item struct {
	Collection string `json:"collection"`
	ID         string `json:"id"`
	Title      string `json:"title"`
	// An episode's numbers and name, or the series an extra came with.
	Subtitle string `json:"subtitle,omitempty"`
	// The poster's address on this server: a series' for its episodes.
	Poster string `json:"poster,omitempty"`
}

// Session is one playing.
type Session struct {
	ID       string
	User     string
	UserName string
	Device   Device
	Kind     string
	Item     Item
	File     string
	// How it is served: direct, as the file is.
	Mode string
	// What asks for its bytes: the player's engine, by its User-Agent.
	Client     string
	Started    time.Time
	LastSeen   time.Time
	Expires    time.Time
	PositionMs int64
	DurationMs int64
	Paused     bool
	Bytes      int64

	running map[int]context.CancelFunc
	nextRun int
	// Its player said it stopped while a stream was still open: it ends with
	// the last one, unless it is heard from again first.
	stopping bool
	// Its row in the history: the same row however often it ends and comes
	// back.
	history string
	// Bytes sent in each of the last seconds, for the bitrate.
	seconds [rateWindow]struct {
		at    int64
		bytes int64
	}
}

// Registry is every playing now, and the ones stopped whose addresses could
// still be asked for.
type Registry struct {
	app core.App
	// The collection files are kept in: what an address names.
	files   string
	now     func() time.Time
	mu      sync.Mutex
	live    map[string]*Session
	revoked map[string]time.Time
	// Playings that ended of themselves and could still be asked for: a
	// request under one brings it back as it was.
	ended map[string]*Session
}

// New keeps the playings of app, whose files are in the collection files.
func New(app core.App, files string) *Registry {
	return &Registry{app: app, files: files, now: time.Now, live: map[string]*Session{}, revoked: map[string]time.Time{}, ended: map[string]*Session{}}
}

// SetClock is for tests.
func (r *Registry) SetClock(now func() time.Time) { r.now = now }

// NewID is a playing's id: what its addresses carry.
func NewID() string {
	return security.RandomStringWithAlphabet(15, "abcdefghijklmnopqrstuvwxyz0123456789")
}

// Start is a new playing, under the id its addresses carry.
func (r *Registry) Start(s Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	s.Started, s.LastSeen = now, now
	if s.Mode == "" {
		s.Mode = "direct"
	}
	s.running, s.history = map[int]context.CancelFunc{}, core.GenerateDefaultRandomId()
	r.live[s.ID] = &s
}

// Open is a request for a playing's bytes begun. Its context ends when the
// playing is stopped, and done is called as it ends. False for a playing
// stopped, or one its address does not belong to. A playing Foyer does not
// know — it restarted, or let it go while quiet — goes on, as it was where
// the history has it.
func (r *Registry) Open(req *http.Request, session, user, file string, expires time.Time) (context.Context, func(), bool) {
	r.mu.Lock()
	_, stopped := r.revoked[session]
	_, known := r.live[session]
	if back, ended := r.ended[session]; !known && ended {
		delete(r.ended, session)
		r.live[session], known = back, true
	}
	r.mu.Unlock()
	if stopped {
		return nil, nil, false
	}
	// The history is read outside the lock: every stream waits on it.
	var remembered *Session
	if !known {
		remembered = r.remember(session, user, file, expires)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, stopped := r.revoked[session]; stopped {
		return nil, nil, false
	}
	s, ok := r.live[session]
	if !ok {
		if remembered == nil {
			return nil, nil, false
		}
		s = remembered
		r.live[session] = s
	}
	if s.User != user {
		return nil, nil, false
	}
	if expires.After(s.Expires) {
		s.Expires = expires
	}
	if agent := req.UserAgent(); agent != "" {
		s.Client = clip(agent, 200)
	}
	s.LastSeen, s.stopping = r.now(), false
	running, cancel := context.WithCancel(req.Context())
	id := s.nextRun
	s.nextRun++
	s.running[id] = cancel
	return running, func() {
		r.mu.Lock()
		delete(s.running, id)
		s.LastSeen = r.now()
		cancel()
		var ended *Session
		if s.stopping && len(s.running) == 0 && r.live[s.ID] == s {
			ended = r.endLocked(s, false)
		}
		r.mu.Unlock()
		r.keep(ended, false)
	}, true
}

// remember makes a playing Foyer knew before it restarted, from its history
// where it has one, else from what its address says.
func (r *Registry) remember(session, user, file string, expires time.Time) *Session {
	now := r.now()
	s := &Session{
		ID: session, User: user, Kind: KindPlay, File: file, Mode: "direct", Started: now, LastSeen: now, Expires: expires,
		running: map[int]context.CancelFunc{}, history: core.GenerateDefaultRandomId(),
	}
	if u, err := r.app.FindRecordById("users", user); err == nil {
		s.UserName = u.GetString("name")
	}
	if f, err := r.app.FindRecordById(r.files, file); err == nil {
		s.Item = ItemOf(r.app, f.GetString("item_collection"), f.GetString("item_id"))
	}
	before, err := r.app.FindFirstRecordByFilter(Collection, "session = {:session} && user = {:user}", dbx.Params{"session": session, "user": user})
	if err != nil {
		return s
	}
	s.history = before.Id
	s.Kind, s.Mode, s.Client = before.GetString("kind"), before.GetString("mode"), before.GetString("client")
	s.Device = Device{Name: before.GetString("device_name"), Platform: before.GetString("device_platform"), AppVersion: before.GetString("app_version")}
	if started := before.GetDateTime("started"); !started.IsZero() {
		s.Started = started.Time()
	}
	if title := before.GetString("title"); title != "" {
		s.Item.Title, s.Item.Subtitle = title, before.GetString("subtitle")
	}
	s.PositionMs, s.DurationMs, s.Bytes = int64(before.GetInt("position_ms")), int64(before.GetInt("duration_ms")), int64(before.GetInt("bytes"))
	return s
}

// Sent is bytes of a playing on their way.
func (r *Registry) Sent(session string, n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.live[session]
	if !ok {
		return
	}
	now := r.now()
	s.Bytes += int64(n)
	s.LastSeen = now
	slot := &s.seconds[now.Unix()%rateWindow]
	if slot.at != now.Unix() {
		slot.at, slot.bytes = now.Unix(), 0
	}
	slot.bytes += int64(n)
}

// Heard is what a player said of a playing: where it got to, and from which
// device. Its stop ends the playing once no stream of it is open — never its
// addresses: a player that carries on, AirPlay's, is let on; and a stream
// still open is a playing going on, should an older one's stop come under
// its id.
func (r *Registry) Heard(req *http.Request, session, user, kind string, positionMs, durationMs int64, paused bool) {
	r.mu.Lock()
	s, ok := r.live[session]
	if !ok || s.User != user {
		r.mu.Unlock()
		return
	}
	s.LastSeen = r.now()
	if s.Device.Name == "" {
		s.Device = DeviceOf(req)
	}
	if positionMs > 0 {
		s.PositionMs = positionMs
	}
	if durationMs > 0 {
		s.DurationMs = durationMs
	}
	s.Paused = paused
	var ended *Session
	switch {
	case kind != "stopped" && kind != "end":
		s.stopping = false
	case len(s.running) == 0:
		ended = r.endLocked(s, false)
	default:
		s.stopping = true
	}
	r.mu.Unlock()
	r.keep(ended, false)
}

// Stop is a superuser's: the playing ends, its streams end with it, and its
// addresses are refused until they would have expired. False for a playing
// that is not on.
func (r *Registry) Stop(id string) bool {
	r.mu.Lock()
	s, ok := r.live[id]
	if !ok {
		r.mu.Unlock()
		return false
	}
	ended := r.endLocked(s, true)
	r.mu.Unlock()
	r.keep(ended, true)
	return true
}

// endLocked takes a playing off the live ones, stopping its streams if it was
// revoked, else keeping it to come back; the caller writes its history once
// the lock is let go.
func (r *Registry) endLocked(s *Session, revoked bool) *Session {
	delete(r.live, s.ID)
	if revoked {
		r.revoked[s.ID] = s.Expires
		for _, cancel := range s.running {
			cancel()
		}
	} else {
		s.stopping = false
		r.ended[s.ID] = s
	}
	ended := *s
	ended.running = nil
	return &ended
}

// keep writes a playing that ended into the history — over its row, where
// it ended before and came back: best effort, never holding anything up.
func (r *Registry) keep(s *Session, revoked bool) {
	if s == nil {
		return
	}
	record, err := r.app.FindRecordById(Collection, s.history)
	if err != nil {
		c, err := r.app.FindCollectionByNameOrId(Collection)
		if err != nil {
			return
		}
		record = core.NewRecord(c)
		record.Id = s.history
	}
	record.Set("session", s.ID)
	if s.User != "" {
		record.Set("user", s.User)
	}
	record.Set("device_name", s.Device.Name)
	record.Set("device_platform", s.Device.Platform)
	record.Set("app_version", s.Device.AppVersion)
	record.Set("kind", s.Kind)
	record.Set("item_collection", s.Item.Collection)
	record.Set("item_id", s.Item.ID)
	record.Set("title", clip(s.Item.Title, 500))
	record.Set("subtitle", clip(s.Item.Subtitle, 500))
	record.Set("file", s.File)
	record.Set("mode", s.Mode)
	record.Set("client", s.Client)
	record.Set("started", s.Started)
	record.Set("ended", r.now())
	record.Set("expires", s.Expires)
	record.Set("position_ms", s.PositionMs)
	record.Set("duration_ms", s.DurationMs)
	record.Set("bytes", s.Bytes)
	record.Set("revoked", revoked)
	if err := r.app.Save(record); err != nil {
		r.app.Logger().Warn("A playing's history could not be kept", "error", err.Error())
	}
}

// Sweep ends what is over: a playing whose addresses have all expired, one
// long quiet, a download whose bytes stopped — never while a stream of it is
// open. Revocations that could no longer be asked for are let go of.
func (r *Registry) Sweep() {
	r.mu.Lock()
	now := r.now()
	var ended []*Session
	for _, s := range r.live {
		if len(s.running) > 0 {
			continue
		}
		quiet := now.Sub(s.LastSeen)
		over := !s.Expires.IsZero() && now.After(s.Expires)
		gone := quiet > GoneAfter || s.Kind == KindDownload && quiet > downloadDone
		if over || gone {
			ended = append(ended, r.endLocked(s, false))
		}
	}
	for id, until := range r.revoked {
		if now.After(until) {
			delete(r.revoked, id)
		}
	}
	for id, s := range r.ended {
		if now.After(s.Expires) {
			delete(r.ended, id)
		}
	}
	r.mu.Unlock()
	for _, s := range ended {
		r.keep(s, false)
	}
}

// Load reads which playings were stopped and could still be asked for: at
// start, so a restart does not let a stopped one go on.
func (r *Registry) Load() error {
	stopped, err := r.app.FindRecordsByFilter(Collection, "revoked = true && expires > {:now}", "", 0, 0,
		dbx.Params{"now": types.NowDateTime().String()})
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, record := range stopped {
		r.revoked[record.GetString("session")] = record.GetDateTime("expires").Time()
	}
	return nil
}

// View is a playing as the dashboard's Live Activity shows it.
type View struct {
	ID         string    `json:"id"`
	User       ViewUser  `json:"user"`
	Device     Device    `json:"device"`
	Kind       string    `json:"kind"`
	Mode       string    `json:"mode"`
	Client     string    `json:"client,omitempty"`
	Item       Item      `json:"item"`
	Started    time.Time `json:"started"`
	LastSeen   time.Time `json:"lastSeen"`
	Quiet      bool      `json:"quiet"`
	PositionMs int64     `json:"positionMs"`
	DurationMs int64     `json:"durationMs"`
	Paused     bool      `json:"paused"`
	Bytes      int64     `json:"bytes"`
	// Bits a second over the last ten seconds.
	Bitrate int64 `json:"bitrate"`
	// Responses streaming now.
	Streams int `json:"streams"`
}

// ViewUser is whose a playing is.
type ViewUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// List is every playing now, the newest first.
func (r *Registry) List() []View {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	views := make([]View, 0, len(r.live))
	for _, s := range r.live {
		var sent int64
		for _, slot := range s.seconds {
			if now.Unix()-slot.at < rateWindow {
				sent += slot.bytes
			}
		}
		views = append(views, View{
			ID: s.ID, User: ViewUser{ID: s.User, Name: s.UserName}, Device: s.Device, Kind: s.Kind, Mode: s.Mode, Client: s.Client, Item: s.Item,
			Started: s.Started, LastSeen: s.LastSeen, Quiet: now.Sub(s.LastSeen) > QuietAfter,
			PositionMs: s.PositionMs, DurationMs: s.DurationMs, Paused: s.Paused, Bytes: s.Bytes,
			Bitrate: sent * 8 / rateWindow, Streams: len(s.running),
		})
	}
	slices.SortFunc(views, func(a, b View) int { return b.Started.Compare(a.Started) })
	return views
}

// ItemOf is a film, a series or an episode as a playing shows it: an
// episode by its series' title and poster, and its own numbers and name.
func ItemOf(app core.App, collection, id string) Item {
	item := Item{Collection: collection, ID: id}
	record, err := app.FindRecordById(collection, id)
	if err != nil {
		return item
	}
	item.Title = record.GetString("title")
	poster := record
	if shows, ok := strings.CutSuffix(collection, "_episodes"); ok {
		if show, err := app.FindRecordById(shows, record.GetString("show")); err == nil {
			item.Title = show.GetString("title")
			item.Subtitle = episodeLine(record)
			poster = show
		}
	}
	if name := poster.GetString("poster"); name != "" {
		item.Poster = "/api/files/" + poster.Collection().Name + "/" + poster.Id + "/" + name + "?thumb=185x278"
	}
	return item
}

// episodeLine is "S1 · E3 · The Next", or what of it the episode has.
func episodeLine(r *core.Record) string {
	parts := []string{}
	if season, episode := r.GetInt("season"), r.GetInt("episode"); episode > 0 {
		parts = append(parts, "S"+strconv.Itoa(season), "E"+strconv.Itoa(episode))
	}
	if title := r.GetString("title"); title != "" {
		parts = append(parts, title)
	}
	return strings.Join(parts, " · ")
}

// clip is text cut to n bytes at most, never inside a character.
func clip(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}
