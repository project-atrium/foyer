package trakt

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/watch"
)

// Key is the Trakt adapter's key in the registry, and the provider's name
// among a user's integrations.
const Key = "trakt"

// Collection holds each user's connection to a tracker.
const Collection = "integrations"

// States a connection is in.
const (
	Connecting = "connecting"
	Connected  = "connected"
	// Refused is a connection Trakt no longer takes: the user connects again.
	Refused = "refused"
	// Failed is a code denied, or never entered in time.
	Failed = "failed"
)

// every is how often a connected user's history is read: where nothing
// changed on either side, Trakt is asked one question.
const every = 10 * time.Minute

// sendChunk is how many plays one post to Trakt carries: Trakt takes a post
// a second.
const sendChunk = 1000

// Service is Trakt for the household: each user's connection, connected by
// a code, and kept in step.
type Service struct {
	app      core.App
	settings func() (id, secret string, ok bool)
	// Site is Trakt's address: a pretend Trakt's in a test.
	Site string
	now  func() time.Time
	// mu keeps one sync of a user at a time.
	mu   sync.Mutex
	wake chan struct{}
}

// NewService is Trakt for app, with the household's app as the adapter's
// settings say — none while it is off.
func NewService(app core.App, settings func() (id, secret string, ok bool)) *Service {
	return &Service{app: app, settings: settings, Site: Site, now: time.Now, wake: make(chan struct{}, 1)}
}

// client is Trakt for the household's app, while it is set up.
func (s *Service) client() (*Client, bool) {
	id, secret, ok := s.settings()
	if !ok || id == "" || secret == "" {
		return nil, false
	}
	c := New(id, secret)
	c.Site = s.Site
	return c, true
}

func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run asks Trakt whether codes were entered and keeps connected users in
// step until ctx ends.
func (s *Service) Run(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.wake:
		}
	}
}

// Tick is one round: codes asked about whose time has come, then each
// connected user whose sync is due.
func (s *Service) Tick(ctx context.Context) {
	c, ok := s.client()
	if !ok {
		return
	}
	now := s.now()
	connecting, err := s.app.FindRecordsByFilter(Collection, "provider = {:p} && state = {:s}", "", 0, 0, dbx.Params{"p": Key, "s": Connecting})
	if err == nil {
		for _, r := range connecting {
			if ctx.Err() != nil {
				return
			}
			if r.GetDateTime("next_poll").Time().After(now) {
				continue
			}
			s.poll(ctx, c, r)
		}
	}
	connected, err := s.app.FindRecordsByFilter(Collection, "provider = {:p} && state = {:s} && mode != {:off}", "", 0, 0,
		dbx.Params{"p": Key, "s": Connected, "off": Off})
	if err != nil {
		return
	}
	for _, r := range connected {
		if ctx.Err() != nil {
			return
		}
		last := r.GetDateTime("last_sync").Time()
		if !last.IsZero() && now.Sub(last) < every {
			continue
		}
		if err := s.Sync(ctx, c, r); err != nil && ctx.Err() == nil {
			s.app.Logger().Warn("A user's Trakt could not be kept in step", "user", r.GetString("user"), "error", err.Error())
		}
	}
}

// poll asks whether a user's code was entered.
func (s *Service) poll(ctx context.Context, c *Client, r *core.Record) {
	now := s.now()
	if r.GetDateTime("code_expires_at").Time().Before(now) {
		s.fail(r, "The code was not entered in time.")
		return
	}
	tokens, state, err := c.Poll(ctx, r.GetString("device_code"))
	interval := max(r.GetInt("interval"), 5)
	switch {
	case errors.Is(err, ErrRefused):
		s.fail(r, "Trakt does not know this Foyer's app: see Settings → Adapters → Trakt.")
		return
	case err != nil, state == Pending:
	case state == Slower:
		interval += 5
	case state == Denied:
		s.fail(r, "Trakt was told no.")
		return
	case state == Expired, state == Gone:
		s.fail(r, "The code was not entered in time.")
		return
	default:
		account, _ := c.Account(ctx, tokens.Access)
		r.Set("state", Connected)
		r.Set("access_token", tokens.Access)
		r.Set("refresh_token", tokens.Refresh)
		r.Set("expires_at", tokens.Expires(now))
		r.Set("account", account)
		for _, field := range []string{"device_code", "user_code", "verification_url", "error"} {
			r.Set(field, "")
		}
		r.Set("code_expires_at", nil)
		r.Set("last_sync", nil)
		r.Set("cursors", nil)
		if err := s.app.Save(r); err != nil {
			s.app.Logger().Warn("A Trakt connection could not be kept", "error", err.Error())
		}
		return
	}
	r.Set("interval", interval)
	r.Set("next_poll", now.Add(time.Duration(interval)*time.Second))
	_ = s.app.Save(r)
}

func (s *Service) fail(r *core.Record, why string) {
	r.Set("state", Failed)
	r.Set("error", why)
	r.Set("device_code", "")
	_ = s.app.Save(r)
}

// cursors is what Trakt last said had changed.
type cursors struct {
	All time.Time `json:"all"`
}

// access is a user's token, refreshed where it is near its end; a refresh
// Trakt refuses parks the connection.
func (s *Service) access(ctx context.Context, c *Client, r *core.Record, stale bool) (string, error) {
	if !stale && r.GetDateTime("expires_at").Time().After(s.now().Add(time.Hour)) {
		return r.GetString("access_token"), nil
	}
	tokens, err := c.Refresh(ctx, r.GetString("refresh_token"))
	if errors.Is(err, ErrRefused) {
		r.Set("state", Refused)
		r.Set("error", "Trakt no longer takes this connection: connect again.")
		_ = s.app.Save(r)
		return "", err
	}
	if err != nil {
		return "", err
	}
	r.Set("access_token", tokens.Access)
	r.Set("refresh_token", tokens.Refresh)
	r.Set("expires_at", tokens.Expires(s.now()))
	if err := s.app.Save(r); err != nil {
		return "", err
	}
	return tokens.Access, nil
}

// Sync keeps one user in step: where Trakt says nothing changed and nothing
// changed here, that is all; else both histories are read, each thing
// settled, Trakt told what it lacks, and Foyer written — in chunks, after
// every request is done.
func (s *Service) Sync(ctx context.Context, c *Client, r *core.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, mode := r.GetString("user"), r.GetString("mode")
	token, err := s.access(ctx, c, r, false)
	if err != nil {
		return err
	}
	activity, err := c.Activities(ctx, token)
	if errors.Is(err, ErrRefused) {
		// Refused before it was due: renewed once, never again for this round.
		if token, err = s.access(ctx, c, r, true); err != nil {
			return err
		}
		activity, err = c.Activities(ctx, token)
	}
	if err != nil {
		return s.later(r, err)
	}
	var seen cursors
	_ = r.UnmarshalJSONField("cursors", &seen)
	last := r.GetDateTime("last_sync").Time()
	tracked, err := watch.TrackedBy(s.app, user, Key)
	if err != nil {
		return err
	}
	changedHere := last.IsZero()
	for _, t := range tracked {
		if t.Updated.After(last) {
			changedHere = true
			break
		}
	}
	if !changedHere && !seen.All.IsZero() && !activity.After(seen.All) {
		r.Set("last_sync", s.now())
		return s.app.Save(r)
	}
	movies, err := c.WatchedMovies(ctx, token)
	if err != nil {
		return s.later(r, err)
	}
	episodes, err := c.WatchedEpisodes(ctx, token)
	if err != nil {
		return s.later(r, err)
	}

	type settling struct {
		thing thing
		o     outcome
		at    time.Time
	}
	var all []settling
	settle := func(th thing, played Watched) {
		t, kept := tracked[th.identity()]
		if o, ok := reconcile(played.Plays, played.Last, t.State, kept, t.Agreed, mode); ok {
			all = append(all, settling{thing: th, o: o, at: t.State.LastPlayedAt})
		}
	}
	for id, played := range movies {
		settle(thing{movie: id}, played)
	}
	for e, played := range episodes {
		settle(thing{episode: e}, played)
	}
	for identity := range tracked {
		th, ok := thingOf(identity)
		if !ok {
			continue
		}
		if th.movie > 0 {
			if _, ok := movies[th.movie]; ok {
				continue
			}
		} else if _, ok := episodes[th.episode]; ok {
			continue
		}
		settle(th, Watched{})
	}
	// Each play at when Foyer heard of it; a few at once, a minute apart before it.
	var adds, removes History
	for _, st := range all {
		at := st.at
		if at.IsZero() {
			at = s.now()
		}
		for i := range st.o.add {
			addTo(&adds, st.thing, at.Add(-time.Duration(i)*time.Minute))
		}
		if st.o.remove {
			addTo(&removes, st.thing, time.Time{})
		}
	}
	notFound := NotFound{Movies: map[int64]bool{}, Episodes: map[Episode]bool{}}
	for _, chunk := range chunks(adds) {
		nf, err := c.Add(ctx, token, chunk)
		if err != nil {
			return s.later(r, err)
		}
		for id := range nf.Movies {
			notFound.Movies[id] = true
		}
		for e := range nf.Episodes {
			notFound.Episodes[e] = true
		}
		s.pause(ctx)
	}
	for _, chunk := range chunks(removes) {
		if err := c.Remove(ctx, token, chunk); err != nil {
			return s.later(r, err)
		}
		s.pause(ctx)
	}
	settled := make([]watch.Settled, 0, len(all))
	for _, st := range all {
		agreed := st.o.agreed
		if st.o.add > 0 && notFound.has(st.thing) {
			// Trakt does not know it: not sent again, nor counted there.
			agreed.Plays -= st.o.add
		}
		settled = append(settled, watch.Settled{Identity: st.thing.identity(), State: st.o.state, Agreed: agreed})
	}
	sort.Slice(settled, func(a, b int) bool { return settled[a].Identity < settled[b].Identity })
	if err := watch.Settle(s.app, user, Key, settled); err != nil {
		return err
	}
	// What was written there moves Trakt's activity: read again, it settles nothing.
	r.Set("cursors", cursors{All: activity})
	r.Set("last_sync", s.now())
	r.Set("error", "")
	return s.app.Save(r)
}

// later keeps why a sync could not finish, for the user to see, and tries
// again at the next round — a 429's wait honoured.
func (s *Service) later(r *core.Record, err error) error {
	var busy *Busy
	if errors.As(err, &busy) {
		r.Set("last_sync", s.now().Add(busy.After-every))
	}
	if errors.Is(err, ErrRefused) {
		r.Set("state", Refused)
		r.Set("error", "Trakt no longer takes this connection: connect again.")
	} else {
		r.Set("error", "Trakt could not be kept in step: "+err.Error())
	}
	_ = s.app.Save(r)
	return err
}

// pause keeps to Trakt's post a second.
func (s *Service) pause(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
	}
}

func addTo(h *History, th thing, at time.Time) {
	stamp := ""
	if !at.IsZero() {
		stamp = at.UTC().Format(time.RFC3339)
	}
	if th.movie > 0 {
		h.Movies = append(h.Movies, HistoryMovie{WatchedAt: stamp, IDs: IDs{TMDB: th.movie}})
		return
	}
	e := th.episode
	h.Shows = append(h.Shows, HistoryShow{IDs: IDs{TMDB: e.Show}, Seasons: []HistorySeason{{Number: e.Season, Episodes: []HistoryEpisode{{Number: e.Number, WatchedAt: stamp}}}}})
}

// chunks is a history in posts Trakt takes.
func chunks(h History) []History {
	var out []History
	for len(h.Movies) > 0 || len(h.Shows) > 0 {
		var c History
		take := min(len(h.Movies), sendChunk)
		c.Movies, h.Movies = h.Movies[:take], h.Movies[take:]
		take = min(len(h.Shows), sendChunk-len(c.Movies))
		c.Shows, h.Shows = h.Shows[:take], h.Shows[take:]
		out = append(out, c)
	}
	return out
}

// View is a user's connection as they see it: never a token.
type View struct {
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Available bool      `json:"available"`
	State     string    `json:"state,omitempty"`
	Mode      string    `json:"mode,omitempty"`
	Account   string    `json:"account,omitempty"`
	Code      *Code     `json:"code,omitempty"`
	LastSync  time.Time `json:"lastSync,omitzero"`
	Error     string    `json:"error,omitempty"`
}

// Code is what a user types on Trakt's site to connect, and where.
type Code struct {
	UserCode        string    `json:"userCode"`
	VerificationURL string    `json:"verificationUrl"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

func (s *Service) record(user string) (*core.Record, error) {
	return s.app.FindFirstRecordByFilter(Collection, "user = {:u} && provider = {:p}", dbx.Params{"u": user, "p": Key})
}

// ViewOf is a user's Trakt as they see it.
func (s *Service) ViewOf(user string) View {
	_, ok := s.client()
	v := View{Key: Key, Name: "Trakt", Available: ok}
	r, err := s.record(user)
	if err != nil {
		return v
	}
	v.State, v.Mode, v.Account, v.Error = r.GetString("state"), r.GetString("mode"), r.GetString("account"), r.GetString("error")
	v.LastSync = r.GetDateTime("last_sync").Time()
	if v.State == Connecting {
		v.Code = &Code{UserCode: r.GetString("user_code"), VerificationURL: r.GetString("verification_url"), ExpiresAt: r.GetDateTime("code_expires_at").Time()}
	}
	return v
}

// ErrNotSetUp is Trakt not set up on this Foyer.
var ErrNotSetUp = errors.New("Trakt is not set up on this Foyer")

// Connect begins connecting a user: a code to type on Trakt's site. One
// still good is handed out again rather than another asked for.
func (s *Service) Connect(ctx context.Context, user string) (View, error) {
	c, ok := s.client()
	if !ok {
		return View{}, ErrNotSetUp
	}
	now := s.now()
	r, err := s.record(user)
	if err == nil && r.GetString("state") == Connecting && r.GetDateTime("code_expires_at").Time().After(now.Add(time.Minute)) {
		return s.ViewOf(user), nil
	}
	code, err := c.Code(ctx)
	if err != nil {
		if errors.Is(err, ErrRefused) {
			return View{}, errors.New("Trakt does not know this Foyer's app: see Settings → Adapters → Trakt")
		}
		return View{}, err
	}
	if r == nil {
		col, err := s.app.FindCollectionByNameOrId(Collection)
		if err != nil {
			return View{}, err
		}
		r = core.NewRecord(col)
		r.Set("user", user)
		r.Set("provider", Key)
		r.Set("mode", TwoWay)
	}
	r.Set("state", Connecting)
	r.Set("device_code", code.DeviceCode)
	r.Set("user_code", code.UserCode)
	r.Set("verification_url", code.VerificationURL)
	r.Set("code_expires_at", now.Add(time.Duration(code.ExpiresIn)*time.Second))
	r.Set("interval", max(code.Interval, 1))
	r.Set("next_poll", now.Add(time.Duration(max(code.Interval, 1))*time.Second))
	r.Set("error", "")
	if err := s.app.Save(r); err != nil {
		return View{}, err
	}
	s.poke()
	return s.ViewOf(user), nil
}

// SetMode is how a user's connection runs from now: both ways, Foyer's way
// alone, or not at all.
func (s *Service) SetMode(user, mode string) (View, error) {
	if mode != TwoWay && mode != Send && mode != Off {
		return View{}, errors.New("that is no way to keep Trakt in step")
	}
	r, err := s.record(user)
	if err != nil {
		return View{}, err
	}
	r.Set("mode", mode)
	// Switched on again, read at once.
	r.Set("last_sync", nil)
	if err := s.app.Save(r); err != nil {
		return View{}, err
	}
	s.poke()
	return s.ViewOf(user), nil
}

// Disconnect ends a user's connection — its token revoked at Trakt, as far
// as Trakt answers — and what Foyer agreed with Trakt goes with it.
func (s *Service) Disconnect(ctx context.Context, user string) error {
	r, err := s.record(user)
	if err != nil {
		return nil
	}
	if c, ok := s.client(); ok && r.GetString("access_token") != "" {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_ = c.Revoke(ctx, r.GetString("access_token"))
	}
	if err := s.app.Delete(r); err != nil {
		return err
	}
	return watch.Forget(s.app, user, Key)
}

// Of is every user's connection, for the dashboard: whose, how it stands.
func (s *Service) Of() ([]map[string]any, error) {
	records, err := s.app.FindRecordsByFilter(Collection, "provider = {:p}", "created", 0, 0, dbx.Params{"p": Key})
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, r := range records {
		name := ""
		if u, err := s.app.FindRecordById("users", r.GetString("user")); err == nil {
			name = firstOf(u.GetString("name"), u.GetString("username"))
		}
		v := s.ViewOf(r.GetString("user"))
		out = append(out, map[string]any{"user": map[string]string{"id": r.GetString("user"), "name": name}, "provider": Key,
			"state": v.State, "mode": v.Mode, "account": v.Account, "lastSync": v.LastSync, "error": v.Error})
	}
	return out, nil
}

// firstOf is the first of values that says anything.
func firstOf(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
