package iptv

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"foyer/internal/adapters"
	"foyer/internal/adapters/stalker"
	"foyer/internal/household"
	"foyer/internal/sessions"
	"foyer/internal/stream"
)

// Prefix is where a channel's relayed stream lives: under the stream
// prefix, so no address of it reaches the log.
const Prefix = stream.Prefix + "iptv/"

const (
	// mostGuideChannels is the most channels one guide question may name.
	mostGuideChannels = 2000
	// writeStall is how long a viewer may take a chunk before it is dropped:
	// a live stream never waits for one player.
	writeStall  = 30 * time.Second
	ticketLabel = "iptv ticket v1"
)

// Routes mounts what a user's device reads a portal by and plays it by, and
// the relay.
func (s *Service) Routes(r *router.Router[*core.RequestEvent]) {
	users := r.Group("/api/foyer/iptv/{conn}")
	users.Bind(apis.RequireAuth("users"))
	users.GET("/lineup", s.lineup)
	users.POST("/guide", s.guide)
	users.POST("/play", s.play)
	users.POST("/refresh", s.refresh)
	r.GET(Prefix+"{ticket}/live.ts", s.relay)
}

// answer is what Foyer says when a portal will not do what a device asked:
// never a 401, which is the user's own session's alone.
func answer(e *core.RequestEvent, status int, reason, message string, extra map[string]any) error {
	body := map[string]any{"status": status, "message": message, "data": map[string]any{}, "reason": reason}
	for k, v := range extra {
		body[k] = v
	}
	return e.JSON(status, body)
}

// failure answers a portal's failure in the terms a device acts on.
func failure(e *core.RequestEvent, err error) error {
	var full *Full
	var taken *TakenOver
	switch {
	case errors.As(err, &full):
		return answer(e, http.StatusConflict, "line-full", "Every line of this subscription is in use.", map[string]any{"streams": full.Streams, "canForce": full.CanForce})
	case errors.As(err, &taken):
		return answer(e, http.StatusConflict, "taken-over", "Someone else took this line.", map[string]any{"by": taken.By})
	case errors.Is(err, adapters.ErrNoConnection):
		return e.NotFoundError("This Foyer has no such portal, or it is switched off.", nil)
	case errors.Is(err, context.Canceled):
		return nil
	}
	pe, ok := stalker.ErrorOf(err)
	if !ok {
		return answer(e, http.StatusBadGateway, "unavailable", "The portal could not be reached.", nil)
	}
	switch {
	case pe.Reason == "limit":
		return answer(e, http.StatusConflict, "limit", pe.Message, nil)
	case pe.Code == "UNAUTHORIZED" || pe.Code == "INVALID_STATE":
		return answer(e, http.StatusConflict, "refused", pe.Message, nil)
	case pe.Code == "NOT_FOUND":
		return answer(e, http.StatusNotFound, "not-found", pe.Message, nil)
	case pe.Reason == "too-many-attempts":
		return answer(e, http.StatusServiceUnavailable, "throttled", pe.Message, nil)
	case pe.Retry == "never":
		return answer(e, http.StatusConflict, "unavailable", pe.Message, nil)
	}
	return answer(e, http.StatusServiceUnavailable, "unavailable", pe.Message, nil)
}

// ChannelView is a channel as a device reads it: never its command.
type ChannelView struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Number      *float64 `json:"number,omitempty"`
	GroupIDs    []string `json:"groupIds"`
	Logo        string   `json:"logo,omitempty"`
	CatchupDays int      `json:"catchupDays,omitzero"`
	Country     string   `json:"country,omitempty"`
}

// LineupView is a portal's lineup as a device reads it.
type LineupView struct {
	ReadAt   time.Time       `json:"readAt"`
	Groups   []stalker.Group `json:"groups"`
	Channels []ChannelView   `json:"channels"`
}

// read is a connection's lineup as kept — read from the portal now where it
// never was, once, for whoever asks first.
func (s *Service) read(e *core.RequestEvent, conn string) (*Lineup, error) {
	if _, err := s.Connection(conn); err != nil {
		return nil, err
	}
	l, err := s.Lineup(conn)
	if err != nil || l != nil && !l.ReadAt.IsZero() {
		return l, err
	}
	if err := s.Read(e.Request.Context(), conn); err != nil {
		return nil, err
	}
	if l, err = s.Lineup(conn); err == nil && l == nil {
		err = errors.New("the lineup read was not kept")
	}
	return l, err
}

func (s *Service) lineup(e *core.RequestEvent) error {
	l, err := s.read(e, e.Request.PathValue("conn"))
	if err != nil {
		return failure(e, err)
	}
	etag := `"` + l.ReadAt.UTC().Format(time.RFC3339Nano) + `"`
	if e.Request.Header.Get("If-None-Match") == etag {
		return e.NoContent(http.StatusNotModified)
	}
	view := LineupView{ReadAt: l.ReadAt.UTC(), Groups: l.Groups, Channels: make([]ChannelView, 0, len(l.Channels))}
	for _, c := range l.Channels {
		groups := []string{}
		if c.Group != "" {
			groups = append(groups, c.Group)
		}
		view.Channels = append(view.Channels, ChannelView{Key: c.Key(), Name: c.Name, Number: c.Number, GroupIDs: groups, Logo: c.Logo, CatchupDays: c.Catchup, Country: c.Country})
	}
	e.Response.Header().Set("ETag", etag)
	e.Response.Header().Set("Cache-Control", "private, no-cache")
	return e.JSON(http.StatusOK, view)
}

func (s *Service) refresh(e *core.RequestEvent) error {
	conn := e.Request.PathValue("conn")
	if _, err := s.Connection(conn); err != nil {
		return failure(e, err)
	}
	if err := s.Refresh(e.Request.Context(), conn); err != nil {
		return failure(e, err)
	}
	return s.lineup(e)
}

// ProgrammeView is a programme as a device reads it.
type ProgrammeView struct {
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	StartsAt    time.Time `json:"startsAt"`
	EndsAt      time.Time `json:"endsAt"`
}

// GuideView is one channel's programmes.
type GuideView struct {
	Channel    string          `json:"channel"`
	Programmes []ProgrammeView `json:"programmes"`
}

func (s *Service) guide(e *core.RequestEvent) error {
	var body struct {
		From     time.Time `json:"from"`
		To       time.Time `json:"to"`
		Channels []string  `json:"channels"`
	}
	if err := e.BindBody(&body); err != nil || body.From.IsZero() || !body.To.After(body.From) {
		return e.BadRequestError("A guide is asked for channels between two times.", err)
	}
	if len(body.Channels) > mostGuideChannels || body.To.Sub(body.From) > stalker.LongestGuide*time.Hour {
		return e.BadRequestError("That is more of the guide than one question may ask.", nil)
	}
	got, keys, err := s.GuideFor(e.Request.Context(), e.Request.PathValue("conn"), body.Channels, body.From, body.To)
	if err != nil {
		return failure(e, err)
	}
	out := make([]GuideView, 0, len(got))
	for i, c := range got {
		view := GuideView{Channel: keys[i], Programmes: make([]ProgrammeView, 0, len(c.Programmes))}
		for _, p := range c.Programmes {
			view.Programmes = append(view.Programmes, ProgrammeView{Title: p.Title, Description: p.Description, StartsAt: p.Start, EndsAt: p.End})
		}
		out = append(out, view)
	}
	return e.JSON(http.StatusOK, map[string]any{"channels": out})
}

// Source is one way to play a channel, as the app is handed it.
type Source struct {
	URL       string `json:"url"`
	Protocol  string `json:"protocol"`
	Container string `json:"container,omitempty"`
}

// Playback is what play answers: a playing, and the channel's stream
// through Foyer.
type Playback struct {
	Session   string    `json:"session"`
	ExpiresAt time.Time `json:"expiresAt"`
	Live      bool      `json:"live"`
	Sources   []Source  `json:"sources"`
	// Line is the line it is on, and Forced whether that is one beyond the
	// subscription's.
	Line   string `json:"line"`
	Forced bool   `json:"forced,omitzero"`
}

func (s *Service) play(e *core.RequestEvent) error {
	var body struct {
		Channel  string `json:"channel"`
		TakeOver string `json:"takeOver"`
		Force    bool   `json:"force"`
	}
	if err := e.BindBody(&body); err != nil || body.Channel == "" {
		return e.BadRequestError("A channel is asked for by its key.", err)
	}
	conn := e.Request.PathValue("conn")
	c, err := s.Connection(conn)
	if err != nil {
		return failure(e, err)
	}
	l, err := s.read(e, conn)
	if err != nil {
		return failure(e, err)
	}
	channel, ok := l.Channel(body.Channel)
	if !ok || channel.Cmd == "" {
		return answer(e, http.StatusNotFound, "not-found", "The portal no longer has this channel.", nil)
	}
	viewer := Viewer{Session: sessions.NewID(), User: e.Auth.Id, UserName: e.Auth.GetString("name")}
	device := sessions.DeviceOf(e.Request)
	viewer.DeviceName = device.Name
	if d, ok := household.DeviceOf(e.Request); ok {
		viewer.Installation = d.Installation
	}
	limit := max(1, c.Int("streams"))
	ln, stopped, err := s.Lines.Claim(conn, channel.Key(), channel.Name, channel.Cmd, viewer, limit, c.Bool("forced"), body.TakeOver, body.Force)
	if err != nil {
		return failure(e, err)
	}
	s.stopAll(stopped)
	if _, err := s.Lines.Start(e.Request.Context(), ln); err != nil {
		s.Lines.Drop(viewer.Session)
		return failure(e, err)
	}
	expires := time.Now().Add(stream.Lifetime).UTC().Truncate(time.Second)
	t, err := s.issue(ticket{Conn: conn, Key: channel.Key(), User: viewer.User, Session: viewer.Session, Expires: expires.Unix()})
	if err != nil {
		s.Lines.Drop(viewer.Session)
		return e.InternalServerError("", err)
	}
	mode := "relay"
	if ln.forced {
		mode = "forced"
	}
	s.playings.Start(sessions.Session{
		ID: viewer.Session, User: viewer.User, UserName: viewer.UserName, Device: device, Kind: sessions.KindPlay,
		Item: sessions.Item{Collection: "iptv", ID: channel.Key(), Title: channel.Name, Subtitle: c.Name}, File: "iptv:" + conn + ":" + channel.Key(), Mode: mode,
		Expires: expires,
	})
	return e.JSON(http.StatusOK, Playback{
		Session: viewer.Session, ExpiresAt: expires, Live: true,
		Sources: []Source{{URL: Prefix + t + "/live.ts", Protocol: "mpegts", Container: "ts"}},
		Line:    ln.id, Forced: ln.forced,
	})
}

// ticket is what a relayed address carries in its path: the channel, the
// user, the playing, and when it expires — signed.
type ticket struct {
	Conn    string `json:"c"`
	Key     string `json:"k"`
	User    string `json:"u"`
	Session string `json:"s"`
	Expires int64  `json:"e"`
}

func (s *Service) issue(t ticket) (string, error) {
	payload, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	mac, err := s.streams.Sign(ticketLabel, payload)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac[:16]), nil
}

func (s *Service) readTicket(raw string, now time.Time) (ticket, bool) {
	payload, sig, ok := strings.Cut(raw, ".")
	if !ok {
		return ticket{}, false
	}
	body, err1 := base64.RawURLEncoding.DecodeString(payload)
	given, err2 := base64.RawURLEncoding.DecodeString(sig)
	if err1 != nil || err2 != nil {
		return ticket{}, false
	}
	mac, err := s.streams.Sign(ticketLabel, body)
	if err != nil || !hmac.Equal(mac[:16], given) {
		return ticket{}, false
	}
	var t ticket
	if json.Unmarshal(body, &t) != nil || now.Unix() > t.Expires || t.Session == "" {
		return ticket{}, false
	}
	return t, true
}

// relay passes a channel's stream on to one viewer, from its line's
// upstream: from a little before the newest bytes, at the viewer's own pace,
// until it leaves, falls behind, or the line is taken.
func (s *Service) relay(e *core.RequestEvent) error {
	t, ok := s.readTicket(e.Request.PathValue("ticket"), time.Now())
	if !ok {
		return e.ForbiddenError("This address has expired, or never was one.", nil)
	}
	ctx, done, allowed := s.playings.Open(e.Request, t.Session, t.User, "iptv:"+t.Conn+":"+t.Key, "", time.Unix(t.Expires, 0))
	if !allowed {
		return e.ForbiddenError("This playing was stopped.", nil)
	}
	defer done()
	l, detach, err := s.Lines.Attach(t.Session)
	if err != nil {
		return answer(e, http.StatusGone, "not-watching", "This channel's line was let go of, or taken.", nil)
	}
	defer detach()
	cast, err := s.Lines.Start(ctx, l)
	if err != nil {
		return failure(e, err)
	}
	w := stream.Counted(e.Response, func(n int) { s.playings.Sent(t.Session, n) })
	rc := stream.Lift(w)
	h := w.Header()
	h.Set("Content-Type", "video/mp2t")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if e.Request.Method == http.MethodHead {
		return nil
	}
	_ = rc.Flush()
	for at, first := cast.join(joinLead), true; ; at++ {
		chunk, err := cast.next(ctx, at)
		if err != nil {
			return nil
		}
		// A viewer joining a stream already running starts where an upstream
		// read began, mid-packet as likely as not: from the first packet on.
		if first {
			chunk, first = packets(chunk), false
		}
		_ = rc.SetWriteDeadline(time.Now().Add(writeStall))
		if _, err := w.Write(chunk); err != nil {
			return nil
		}
		_ = rc.Flush()
	}
}

// packets is an MPEG-TS chunk from its first packet on: where three sync
// bytes stand a packet apart. A chunk too short to tell is left as it is.
func packets(chunk []byte) []byte {
	const size = 188
	for i := 0; i+2*size < len(chunk) && i < size; i++ {
		if chunk[i] == 0x47 && chunk[i+size] == 0x47 && chunk[i+2*size] == 0x47 {
			return chunk[i:]
		}
	}
	return chunk
}

// Status is a portal's lineup as the dashboard shows it: how much of it
// Foyer keeps, when it was read, why the last read failed, and the lines in
// use.
type Status struct {
	ReadAt   *time.Time `json:"readAt,omitempty"`
	TriedAt  *time.Time `json:"triedAt,omitempty"`
	Error    string     `json:"error,omitempty"`
	Groups   int        `json:"groups"`
	Channels int        `json:"channels"`
	// Streams is how many the subscription allows at once.
	Streams int      `json:"streams"`
	Lines   []Stream `json:"lines"`
}

// AdminRoutes mount what the dashboard shows of each portal, and its read
// now.
func (s *Service) AdminRoutes(admin *router.RouterGroup[*core.RequestEvent]) {
	admin.GET("/iptv/{conn}", s.adminStatus)
	admin.POST("/iptv/{conn}/read", s.adminRead)
}

func (s *Service) status(conn string) (Status, error) {
	c, err := s.registry.Connection(conn)
	if err != nil || c.Adapter != Key {
		return Status{}, adapters.ErrNoConnection
	}
	out := Status{Streams: max(1, c.Int("streams")), Lines: s.Watching(conn)}
	l, err := s.Lineup(conn)
	if err != nil {
		return Status{}, err
	}
	if l != nil {
		if !l.ReadAt.IsZero() {
			at := l.ReadAt.UTC()
			out.ReadAt = &at
		}
		if !l.TriedAt.IsZero() {
			at := l.TriedAt.UTC()
			out.TriedAt = &at
		}
		out.Error, out.Groups, out.Channels = l.Error, len(l.Groups), len(l.Channels)
	}
	return out, nil
}

func (s *Service) adminStatus(e *core.RequestEvent) error {
	out, err := s.status(e.Request.PathValue("conn"))
	if errors.Is(err, adapters.ErrNoConnection) {
		return e.NotFoundError("There is no such portal.", nil)
	}
	if err != nil {
		return e.InternalServerError("", err)
	}
	return e.JSON(http.StatusOK, out)
}

// adminRead reads a portal's lineup now, as a superuser asks. A portal's
// refusal is said in the status, never as a 401, which would sign the
// dashboard out.
func (s *Service) adminRead(e *core.RequestEvent) error {
	conn := e.Request.PathValue("conn")
	if _, err := s.Connection(conn); err != nil {
		return e.BadRequestError("Only a portal that is switched on is read.", nil)
	}
	_ = s.Read(e.Request.Context(), conn)
	return s.adminStatus(e)
}
