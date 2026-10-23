// Package iptv is a household's IPTV through Foyer, which becomes its
// portals' only client: each portal reached as the one box its MAC address
// is, its lineup read once in a while and kept, its guide held in memory,
// and its streams relayed — one upstream per channel, however many devices
// watch it, within the lines the subscription allows.
package iptv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/adapters"
	"foyer/internal/adapters/stalker"
	"foyer/internal/sessions"
	"foyer/internal/stream"
)

const (
	// Key is the Stalker adapter's key in the registry.
	Key = "stalker"
	// LineupsCollection holds each connection's lineup.
	LineupsCollection = "iptv_lineups"
	// sessionSecret is where a connection keeps its portal session.
	sessionSecret = "portal_session"
	// castBytes is how much of a stream is kept for its viewers: a few
	// seconds of a channel in 4K, so one slow viewer does not hold the rest.
	castBytes = 16 << 20
	// joinLead is how far back from the newest bytes a viewer joining a
	// stream already running starts: a picture to begin on sooner.
	joinLead = 1 << 20
	// quietUpstream is how long a portal's stream may send nothing before
	// it is taken to have stopped.
	quietUpstream = 20 * time.Second
	// refreshAfter is how soon after a read a user's refresh reads again.
	refreshAfter = 10 * time.Minute
	// retryAfter is how soon a read that failed is tried again.
	retryAfter = time.Hour
)

// Service is IPTV on one server.
type Service struct {
	app      core.App
	registry *adapters.Registry
	streams  *stream.Streamer
	playings *sessions.Registry
	Lines    *Lines
	// HTTP asks a portal; Upstream fetches its streams.
	HTTP     *http.Client
	Upstream *http.Client

	mu        sync.Mutex
	providers map[string]*held
	lineups   map[string]*Lineup
	reading   map[string]*read
}

type held struct {
	identity string
	provider *stalker.Provider
}

type read struct {
	done chan struct{}
	err  error
}

// New is IPTV on app, its connections in registry, its streams signed by
// streams and played under playings.
func New(app core.App, registry *adapters.Registry, streams *stream.Streamer, playings *sessions.Registry) *Service {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := func() *http.Transport {
		return &http.Transport{Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second, MaxIdleConnsPerHost: 4, IdleConnTimeout: 90 * time.Second}
	}
	s := &Service{
		app: app, registry: registry, streams: streams, playings: playings,
		HTTP: &http.Client{Transport: transport()}, Upstream: &http.Client{Transport: transport()},
		providers: map[string]*held{}, lineups: map[string]*Lineup{}, reading: map[string]*read{},
	}
	s.Lines = NewLines(s.open)
	s.Lines.Left = playings.Left
	registry.OnChange(s.changed)
	return s
}

// Connection is an IPTV connection that is on, by its id: ErrNoConnection
// for one that is not.
func (s *Service) Connection(id string) (adapters.Connection, error) {
	c, err := s.registry.Connection(id)
	if err != nil || c.Adapter != Key || !c.Enabled {
		return adapters.Connection{}, adapters.ErrNoConnection
	}
	return c, nil
}

// Served are the connections a household's devices are served: each portal
// that is on.
func (s *Service) Served() []map[string]any {
	connections, err := s.registry.Connections(Key)
	if err != nil {
		return nil
	}
	out := []map[string]any{}
	for _, c := range connections {
		if c.Enabled {
			out = append(out, map[string]any{"key": "iptv-" + c.ID, "type": "iptv", "name": c.Name})
		}
	}
	return out
}

func identityOf(settings adapters.Settings) string {
	parts := []string{settings.String("portalUrl"), settings.String("mac"), settings.String("serialNumber"), settings.String("deviceId"), settings.String("signature")}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func optionsOf(settings adapters.Settings) stalker.ProviderOptions {
	return stalker.ProviderOptions{
		Options: stalker.Options{
			URL:      settings.String("portalUrl"),
			Identity: stalker.Identity{MAC: settings.String("mac"), SerialNumber: settings.String("serialNumber"), DeviceID: settings.String("deviceId"), Signature: settings.String("signature")},
			AtOnce:   requestsAtOnce(settings),
		},
		Zone:   strings.TrimSpace(settings.String("timeZone")),
		AsSent: !settings.Bool("guideByCountry"),
	}
}

// requestsAtOnce is how many requests may be on their way to the portal: the
// connection's whole number, held to 1–4. One kept as text from when it was a
// choice ("2") was not migrated, and Settings reads it as the default, 1.
func requestsAtOnce(settings adapters.Settings) int {
	return min(4, max(1, settings.Int("requestsAtOnce")))
}

// provider is a connection's portal: one per connection, kept while its
// settings stand, so every call shares its sign-in and its gate.
func (s *Service) provider(c adapters.Connection) *stalker.Provider {
	identity := identityOf(c.Settings)
	key := identity + "|" + strconv.Itoa(requestsAtOnce(c.Settings)) + "|" + c.String("timeZone") + "|" + strconv.FormatBool(c.Bool("guideByCountry"))
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.providers[c.ID]; ok && h.identity == key {
		return h.provider
	}
	o := optionsOf(c.Settings)
	o.HTTP = s.HTTP
	o.Store = &store{registry: s.registry, connection: c.ID, identity: identity}
	p := stalker.NewProvider(o)
	s.providers[c.ID] = &held{identity: key, provider: p}
	return p
}

// store keeps a connection's portal session among its secrets: for the box
// it signed in as, so a MAC address changed starts afresh.
type store struct {
	registry   *adapters.Registry
	connection string
	identity   string
}

func (st *store) Load() string {
	raw, err := st.registry.Secret(st.connection, sessionSecret)
	if err != nil || raw == "" {
		return ""
	}
	var kept struct {
		For     string `json:"for"`
		Session string `json:"session"`
	}
	if json.Unmarshal([]byte(raw), &kept) != nil || kept.For != st.identity {
		return ""
	}
	return kept.Session
}

func (st *store) Save(session string) {
	raw := ""
	if session != "" {
		b, _ := json.Marshal(map[string]string{"for": st.identity, "session": session})
		raw = string(b)
	}
	_ = st.registry.SetSecret(st.connection, sessionSecret, raw)
}

// changed is a connection made, saved or removed: its portal and its lines
// start again with what it is now, and a new portal's lineup is read.
func (s *Service) changed(c adapters.Connection) {
	if c.Adapter != Key {
		return
	}
	s.mu.Lock()
	h := s.providers[c.ID]
	s.mu.Unlock()
	current, err := s.registry.Connection(c.ID)
	if err != nil || !current.Enabled || h == nil || !strings.HasPrefix(h.identity, identityOf(current.Settings)) {
		// Gone, off, or another portal or box: nothing of the old one goes on.
		s.Lines.Close(c.ID)
	}
	s.mu.Lock()
	delete(s.providers, c.ID)
	if err != nil {
		delete(s.lineups, c.ID)
	}
	s.mu.Unlock()
}

// Check signs in with a connection's settings: its test. A connection's own
// portal is used where the settings are its; a refused one is asked again,
// since a test is someone asking.
func (s *Service) Check(ctx context.Context, settings adapters.Settings) adapters.Result {
	identity := identityOf(settings)
	var p *stalker.Provider
	s.mu.Lock()
	for _, h := range s.providers {
		if strings.HasPrefix(h.identity, identity) && h.provider.Portal.Refused() == nil {
			p = h.provider
		}
	}
	s.mu.Unlock()
	if p == nil {
		o := optionsOf(settings)
		o.HTTP = s.HTTP
		p = stalker.NewProvider(o)
	}
	if err := p.Check(ctx); err != nil {
		return adapters.Result{Message: err.Error()}
	}
	return adapters.Result{OK: true, Message: "Signed in as the box its MAC address is, and the portal took it."}
}

// open starts a line's upstream: the portal's link made, as a channel
// starting does — before anything waiting — and its stream asked for as the
// box, kept coming for every viewer until the line closes.
func (s *Service) open(ctx context.Context, conn, cmd string) (*broadcast, error) {
	c, err := s.Connection(conn)
	if err != nil {
		return nil, err
	}
	source, err := s.provider(c).Link(ctx, "itv", cmd, "", true)
	if err != nil {
		return nil, err
	}
	if source.Protocol == "hls" {
		return nil, errHLS
	}
	upstream, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(upstream, http.MethodGet, source.URI, nil)
	if err != nil {
		cancel()
		return nil, errUpstream
	}
	req.Header.Set("User-Agent", stalker.UserAgent)
	resp, err := s.Upstream.Do(req)
	if err != nil {
		cancel()
		// Never the error itself: it names the stream's address.
		return nil, errUpstream
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, &stalker.Error{Code: "PROVIDER_UNAVAILABLE", Message: fmt.Sprintf("The portal's stream refused (%d). Another device may be using the line.", resp.StatusCode), Retry: "backoff"}
	}
	cast := newBroadcast(castBytes)
	go pump(resp.Body, cast, cancel)
	return cast, nil
}

var (
	errHLS      = &stalker.Error{Code: "PROVIDER_UNAVAILABLE", Message: "This channel streams as HLS, which Foyer does not relay yet.", Retry: "never"}
	errUpstream = &stalker.Error{Code: "PROVIDER_UNAVAILABLE", Message: "The portal's stream did not answer.", Retry: "backoff"}
)

// pump passes a stream's bytes to its viewers until it ends, or sends
// nothing for a while.
func pump(body io.ReadCloser, cast *broadcast, cancel context.CancelFunc) {
	defer cancel()
	defer body.Close()
	quiet := time.AfterFunc(quietUpstream, cancel)
	defer quiet.Stop()
	buf := make([]byte, 64<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			quiet.Reset(quietUpstream)
			cast.write(buf[:n])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			cast.end(err)
			return
		}
	}
}

// Channel is one channel of a lineup as kept: the portal's own id, and the
// command its link is made from, which no device is ever sent.
type Channel struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Number  *float64 `json:"number,omitempty"`
	Group   string   `json:"group,omitempty"`
	Logo    string   `json:"logo,omitempty"`
	Catchup int      `json:"catchup,omitempty"`
	Country string   `json:"country,omitempty"`
	Cmd     string   `json:"cmd,omitempty"`
	// Missing is when a read of the whole list first lacked it: the next one
	// that lacks it too takes it away.
	Missing *time.Time `json:"missing,omitempty"`
}

// Key is the key a device knows the channel by: the app's own adapter's,
// so a channel is the same on Foyer and on a device that reaches the portal.
func (c Channel) Key() string { return stalker.ChannelKey(c.ID) }

// Lineup is a portal's groups and channels, in its channel order.
type Lineup struct {
	ReadAt   time.Time
	TriedAt  time.Time
	Error    string
	Groups   []stalker.Group
	Channels []Channel
	byKey    map[string]int
}

func (l *Lineup) index() {
	l.byKey = make(map[string]int, len(l.Channels))
	for i, c := range l.Channels {
		l.byKey[c.Key()] = i
	}
}

// Channel is a channel by its key.
func (l *Lineup) Channel(key string) (Channel, bool) {
	i, ok := l.byKey[key]
	if !ok {
		return Channel{}, false
	}
	return l.Channels[i], true
}

// merge is a lineup read laid over the one kept. A read of the whole list
// takes away what an earlier whole read lacked too — never on a read that
// came back mostly empty — and a read of part of it only adds and renews.
func merge(kept *Lineup, read stalker.Lineup, now time.Time) *Lineup {
	out := &Lineup{ReadAt: now, TriedAt: now, Groups: read.Groups}
	whole := read.Whole && (kept == nil || len(read.Channels)*2 >= len(kept.Channels))
	seen := map[string]bool{}
	for _, c := range read.Channels {
		seen[c.ID] = true
		ch := Channel{ID: c.ID, Name: c.Name, Group: c.GroupID, Logo: c.Logo, Catchup: c.CatchupDays, Country: c.Country, Cmd: c.Cmd}
		if c.HasNumber {
			n := c.Number
			ch.Number = &n
		}
		out.Channels = append(out.Channels, ch)
	}
	if kept != nil {
		if !whole || len(out.Groups) == 0 {
			known := map[string]bool{}
			for _, g := range out.Groups {
				known[g.ID] = true
			}
			for _, g := range kept.Groups {
				if !known[g.ID] {
					out.Groups = append(out.Groups, g)
				}
			}
		}
		for _, c := range kept.Channels {
			switch {
			case seen[c.ID]:
			case !whole:
				out.Channels = append(out.Channels, c)
			case c.Missing == nil:
				c.Missing = &now
				out.Channels = append(out.Channels, c)
			}
		}
	}
	if out.Groups == nil {
		out.Groups = []stalker.Group{}
	}
	out.index()
	return out
}

// Lineup is a connection's lineup as kept: read from the database once, then
// from memory.
func (s *Service) Lineup(conn string) (*Lineup, error) {
	s.mu.Lock()
	l, ok := s.lineups[conn]
	s.mu.Unlock()
	if ok {
		return l, nil
	}
	record, err := s.app.FindFirstRecordByFilter(LineupsCollection, "connection = {:c}", dbx.Params{"c": conn})
	if err != nil {
		return nil, nil
	}
	l = &Lineup{ReadAt: record.GetDateTime("read_at").Time(), TriedAt: record.GetDateTime("tried_at").Time(), Error: record.GetString("error")}
	if err := record.UnmarshalJSONField("groups", &l.Groups); err != nil {
		l.Groups = []stalker.Group{}
	}
	if err := record.UnmarshalJSONField("channels", &l.Channels); err != nil {
		l.Channels = nil
	}
	l.index()
	s.mu.Lock()
	s.lineups[conn] = l
	s.mu.Unlock()
	return l, nil
}

// Read reads a connection's lineup from its portal now — once, however many
// ask together — and keeps it.
func (s *Service) Read(ctx context.Context, conn string) error {
	s.mu.Lock()
	r := s.reading[conn]
	if r == nil {
		r = &read{done: make(chan struct{})}
		s.reading[conn] = r
		go func() {
			r.err = s.readNow(context.Background(), conn)
			s.mu.Lock()
			delete(s.reading, conn)
			s.mu.Unlock()
			close(r.done)
		}()
	}
	s.mu.Unlock()
	select {
	case <-r.done:
		return r.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) readNow(ctx context.Context, conn string) error {
	c, err := s.Connection(conn)
	if err != nil {
		return err
	}
	kept, err := s.Lineup(conn)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	read, err := s.provider(c).Lineup(ctx)
	if err != nil {
		s.failed(conn, kept, now, err)
		return err
	}
	l := merge(kept, read, now)
	if err := s.keep(conn, l); err != nil {
		return err
	}
	s.mu.Lock()
	s.lineups[conn] = l
	s.mu.Unlock()
	return nil
}

func (s *Service) record(conn string) (*core.Record, error) {
	record, err := s.app.FindFirstRecordByFilter(LineupsCollection, "connection = {:c}", dbx.Params{"c": conn})
	if err == nil {
		return record, nil
	}
	collection, err := s.app.FindCollectionByNameOrId(LineupsCollection)
	if err != nil {
		return nil, err
	}
	record = core.NewRecord(collection)
	record.Set("connection", conn)
	return record, nil
}

func (s *Service) keep(conn string, l *Lineup) error {
	record, err := s.record(conn)
	if err != nil {
		return err
	}
	record.Set("read_at", l.ReadAt)
	record.Set("tried_at", l.TriedAt)
	record.Set("error", "")
	record.Set("groups", l.Groups)
	record.Set("channels", l.Channels)
	return s.app.Save(record)
}

// failed keeps when a read failed, and why, beside the lineup it leaves as
// it was.
func (s *Service) failed(conn string, kept *Lineup, now time.Time, err error) {
	record, rerr := s.record(conn)
	if rerr != nil {
		return
	}
	message := "The portal could not be read."
	if e, ok := stalker.ErrorOf(err); ok {
		message = e.Message
	}
	record.Set("tried_at", now)
	record.Set("error", message)
	if kept == nil {
		record.Set("groups", []stalker.Group{})
		record.Set("channels", []Channel{})
	}
	if s.app.Save(record) != nil {
		return
	}
	s.mu.Lock()
	if kept != nil {
		updated := *kept
		updated.TriedAt, updated.Error = now, message
		s.lineups[conn] = &updated
	} else {
		delete(s.lineups, conn)
	}
	s.mu.Unlock()
}

// due says whether a connection's lineup should be read now: never read,
// read longer ago than its connection says, or a failed try an hour old.
func (s *Service) due(c adapters.Connection, now time.Time) bool {
	l, err := s.Lineup(c.ID)
	if err != nil {
		return false
	}
	if l == nil {
		return true
	}
	if l.Error != "" && now.Sub(l.TriedAt) < retryAfter {
		return false
	}
	days := c.Int("lineupDays")
	if days < 1 {
		days = 7
	}
	return l.ReadAt.IsZero() || now.Sub(l.ReadAt) >= time.Duration(days)*24*time.Hour
}

// Run reads each portal's lineup when it is due, until ctx ends: a new
// connection at once, then every few days as it says.
func (s *Service) Run(ctx context.Context) {
	tick := time.NewTimer(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		connections, err := s.registry.Connections(Key)
		if err == nil {
			for _, c := range connections {
				if !c.Enabled || !s.due(c, time.Now()) {
					continue
				}
				if err := s.Read(ctx, c.ID); err != nil && ctx.Err() == nil {
					s.app.Logger().Warn("A portal's channels could not be read", "connection", c.ID, "error", err.Error())
				}
			}
		}
		tick.Reset(5 * time.Minute)
	}
}

// Refresh reads a lineup again for a user who asks: at once, unless it was
// read only a little while ago.
func (s *Service) Refresh(ctx context.Context, conn string) error {
	l, err := s.Lineup(conn)
	if err != nil {
		return err
	}
	if l != nil && !l.ReadAt.IsZero() && time.Since(l.ReadAt) < refreshAfter {
		return nil
	}
	return s.Read(ctx, conn)
}

// GuideFor is the programmes of a connection's channels, by their keys, from
// from to to: each channel's read by the country its lineup says.
func (s *Service) GuideFor(ctx context.Context, conn string, keys []string, from, to time.Time) ([]stalker.ChannelProgrammes, []string, error) {
	c, err := s.Connection(conn)
	if err != nil {
		return nil, nil, err
	}
	l, err := s.Lineup(conn)
	if err != nil {
		return nil, nil, err
	}
	var asked []stalker.GuideChannel
	var known []string
	for _, key := range keys {
		ch, ok := Channel{}, false
		if l != nil {
			ch, ok = l.Channel(key)
		}
		if !ok {
			// A channel the lineup does not hold yet is read as the portal says.
			parsed, valid := stalker.ParseKey(key)
			if !valid || parsed.Kind != "channel" {
				continue
			}
			ch = Channel{ID: parsed.ID}
		}
		asked = append(asked, stalker.GuideChannel{ID: ch.ID, Country: ch.Country})
		known = append(known, key)
	}
	got, err := s.provider(c).Programmes(ctx, asked, from, to)
	return got, known, err
}

// Watching are a connection's lines in use.
func (s *Service) Watching(conn string) []Stream { return s.Lines.InUse(conn) }

// stopAll stops the playings a take-over ended.
func (s *Service) stopAll(sessions []string) {
	for _, id := range slices.Compact(sessions) {
		s.playings.Stop(id)
	}
}
