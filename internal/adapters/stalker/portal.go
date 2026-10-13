package stalker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A MAG box, as portals expect to see one. Some refuse anything else, and a
// portal's streams are asked for as the same box.
const (
	UserAgent  = "Mozilla/5.0 (QtEmbedded; U; Linux; C) AppleWebKit/533.3 (KHTML, like Gecko) MAG200 stbapp ver: 2 rev: 250 Safari/533.3"
	xUserAgent = "Model: MAG250; Link: WiFi"
	profileVer = "ImageDescription: 0.2.18-r23-250; ImageDate: Wed Aug 29 10:49:53 EEST 2018; PORTAL version: 5.6.2; API Version: JS API version: 343; STB API version: 146; Player Engine version: 0x58c"
)

const (
	callTimeout = 15 * time.Second
	// WholePortal is how long an answer about the whole portal — every
	// channel, every channel's guide — is given: far more than a page.
	WholePortal = 60 * time.Second
	// What a portal answers, as text, for a token it no longer takes.
	authorizationFailed = "Authorization failed."
	// A portal that asks for fewer requests without saying for how long is
	// left alone this long — longer each time it asks again, to ten minutes.
	pauseFirst   = time.Minute
	pauseLongest = 10 * time.Minute
	// The longest a portal's own Retry-After is taken at its word.
	askedLongest = time.Hour
	// The most an answer may be: the whole guide of a portal of thousands of
	// channels runs to tens of megabytes.
	answerMost = 256 << 20
)

var (
	macPattern = regexp.MustCompile(`^[0-9A-F]{2}(:[0-9A-F]{2}){5}$`)
	// A portal's plain link command for a channel, `ffmpeg http://localhost/ch/101_`:
	// nothing in it but the channel's own id, which the portal resolves itself.
	plainLink = regexp.MustCompile(`^(\S+) http://localhost/ch/([^\s/]+?)(_?)$`)
	// The same, remembered as a form: `{id}` where the channel's id goes.
	linkForm      = regexp.MustCompile(`^\S+ http://localhost/ch/\{id\}_?$`)
	schemePattern = regexp.MustCompile(`(?i)^[a-z]+://`)
	phpPattern    = regexp.MustCompile(`(?i)\.php$`)
	cPattern      = regexp.MustCompile(`(?i)/c$`)
	stalkerSuffix = regexp.MustCompile(`(?i)/stalker_portal$`)
	scriptSuffix  = regexp.MustCompile(`(?i)/(server/load|portal)\.php$`)
	digitsOnly    = regexp.MustCompile(`^\d+$`)
)

// Error is a portal's failure in the app's terms: a code, whether to try
// again, and why.
type Error struct {
	// UNAUTHORIZED, NOT_FOUND, PROVIDER_UNAVAILABLE, TIMEOUT or INVALID_STATE.
	Code    string
	Message string
	// backoff, never, or none said.
	Retry string
	// too-many-attempts where the portal asked for fewer.
	Reason string
	// answer says the portal was reached, and said this.
	answer bool
}

func (e *Error) Error() string { return e.Message }

// IsAnswer says whether an error is what the portal answered — it was
// reached, and said no, or said something that is no answer at all — as
// against failing to reach it.
func IsAnswer(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.answer
}

// ErrorOf is err as a portal's Error, where it is one.
func ErrorOf(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

func throttled() *Error {
	return &Error{Code: "PROVIDER_UNAVAILABLE", Message: "The portal asked for fewer requests, and is being left alone for a while.", Retry: "backoff", Reason: "too-many-attempts"}
}

func statusError(status int) *Error {
	switch {
	case status == http.StatusNotFound:
		return &Error{Code: "NOT_FOUND", Message: "The portal has no such thing.", answer: true}
	case status >= 500:
		return &Error{Code: "PROVIDER_UNAVAILABLE", Message: "The portal ran into a problem.", Retry: "backoff"}
	}
	return &Error{Code: "PROVIDER_UNAVAILABLE", Message: fmt.Sprintf("The portal refused the request (%d).", status), Retry: "never", answer: true}
}

// Unreadable is an answer that could not be read.
func Unreadable() *Error {
	return &Error{Code: "PROVIDER_UNAVAILABLE", Message: "The portal sent an answer that could not be read.", Retry: "backoff", answer: true}
}

// Identity is the box a portal knows: its MAC address, which is what signs
// in, and the ids some portals check beside it.
type Identity struct {
	MAC, SerialNumber, DeviceID, Signature string
}

func (i Identity) mac() (string, error) {
	mac := strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(i.MAC)), "-", ":")
	if !macPattern.MatchString(mac) {
		return "", &Error{Code: "INVALID_STATE", Message: "The MAC address should look like 00:1A:79:12:34:56.", Retry: "never"}
	}
	return mac, nil
}

// SessionStore keeps a portal's session — where its API is, the token, and
// the form its links take — so that a restart signs in no more than it must.
// Load answers "" for nothing; Save("") clears it.
type SessionStore interface {
	Load() string
	Save(string)
}

// MemoryStore is a session kept in memory alone.
type MemoryStore struct {
	mu    sync.Mutex
	saved string
}

func (m *MemoryStore) Load() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saved
}

func (m *MemoryStore) Save(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saved = s
}

type session struct {
	Endpoint string `json:"endpoint,omitempty"`
	Token    string `json:"token,omitempty"`
	Link     string `json:"link,omitempty"`
}

// Options are a portal's: its address, the box, how many requests may be on
// their way at once, and where its session is kept.
type Options struct {
	URL      string
	Identity Identity
	// AtOnce is how many requests may be out at once: one unless the
	// connection says more.
	AtOnce int
	Store  SessionStore
	HTTP   *http.Client
	Clock  Clock
}

// Portal is a Stalker portal, reached as the one box its MAC address is.
//
// A portal keeps one token per MAC address, so every handshake ends the
// token before it. Calls that start together therefore share everything: one
// read of the saved session, one sign-in, and one renewal when the token runs
// out — a call that finds the token already renewed takes the new one,
// rather than shaking hands again and ending it for the others. A portal that
// refuses the MAC address is remembered, and never asked again by this
// Portal; nor is a handshake repeated more than once for one call.
type Portal struct {
	o          Options
	candidates []string
	gate       *Gate

	mu      sync.Mutex
	loaded  bool
	session *session
	// The form every channel's link command takes, where they all take one.
	link    string
	flight  *signing
	refused *Error
	// Until when the portal asked to be left alone, and how long the next
	// pause it does not time itself runs.
	pausedUntil time.Time
	nextPause   time.Duration
}

type signing struct {
	done    chan struct{}
	session session
	err     error
}

// New is the portal at o.URL. Nothing is asked of it until the first call.
func New(o Options) *Portal {
	if o.Store == nil {
		o.Store = &MemoryStore{}
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{}
	}
	if o.Clock == nil {
		o.Clock = wallClock{}
	}
	return &Portal{o: o, candidates: EndpointsFor(o.URL), gate: NewGate(o.AtOnce, o.Clock), nextPause: pauseFirst}
}

// EndpointsFor is where a portal's API may be, for the address a provider
// hands out: `…/c/`, `…/stalker_portal/c/`, or an address that names the
// script itself.
func EndpointsFor(portalURL string) []string {
	u := strings.TrimSpace(portalURL)
	if !schemePattern.MatchString(u) {
		u = "http://" + u
	}
	for _, marker := range []string{"#", "?"} {
		if at := strings.Index(u, marker); at >= 0 {
			u = u[:at]
		}
	}
	if phpPattern.MatchString(u) {
		return []string{u}
	}
	base := strings.TrimRight(cPattern.ReplaceAllString(strings.TrimRight(u, "/"), ""), "/")
	out := []string{base + "/server/load.php"}
	if !stalkerSuffix.MatchString(base) {
		out = append(out, base+"/stalker_portal/server/load.php")
	}
	return append(out, base+"/portal.php")
}

func rootOf(endpoint string) string {
	return stalkerSuffix.ReplaceAllString(scriptSuffix.ReplaceAllString(endpoint, ""), "")
}

// Root is the portal's root, for a logo it names by path: known once a call
// has found its endpoint.
func (p *Portal) Root() string {
	p.mu.Lock()
	endpoint := p.candidates[0]
	if p.session != nil {
		endpoint = p.session.Endpoint
	}
	p.mu.Unlock()
	return rootOf(endpoint)
}

// Refused is the portal's refusal of this MAC address, where it refused it.
func (p *Portal) Refused() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refused == nil {
		return nil
	}
	return p.refused
}

// persistLocked writes what the session keeps — the endpoint, the token and
// the link form — whole, from memory: a sign-in and a form learned never
// overwrite each other's.
func (p *Portal) persistLocked() {
	kept := session{}
	if p.session != nil {
		kept = *p.session
	}
	kept.Link = p.link
	if kept == (session{}) {
		p.o.Store.Save("")
		return
	}
	if b, err := json.Marshal(kept); err == nil {
		p.o.Store.Save(string(b))
	}
}

// storedLocked is the session an earlier run saved, read once however many
// calls start together: a second read racing the first would see no session,
// shake hands, and end the saved token the first was using.
func (p *Portal) storedLocked() *session {
	if p.loaded {
		return p.session
	}
	p.loaded = true
	saved := record(parse([]byte(p.o.Store.Load())))
	endpoint, token := text(saved["endpoint"]), text(saved["token"])
	// A sign-in that finished meanwhile is newer than anything saved.
	if p.session == nil && endpoint != "" && token != "" && slices.Contains(p.candidates, endpoint) {
		p.session = &session{Endpoint: endpoint, Token: token}
	}
	// Read apart: a form that cannot be read never loses the token beside it.
	if link := text(saved["link"]); p.link == "" && link != "" && linkForm.MatchString(link) {
		p.link = link
	}
	return p.session
}

// pauseFor leaves the portal alone once it asked for fewer requests — for
// as long as it said, or for a while — once for the pause, however many
// answers in flight said so.
func (p *Portal) pauseFor(header http.Header) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.o.Clock.Now()
	if now.Before(p.pausedUntil) {
		return
	}
	if asked, ok := RetryAfter(header.Values("Retry-After"), now); ok {
		p.pausedUntil = now.Add(asked)
		return
	}
	p.pausedUntil = now.Add(p.nextPause)
	p.nextPause = min(pauseLongest, p.nextPause*2)
}

func (p *Portal) paused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.o.Clock.Now().Before(p.pausedUntil)
}

// RetryAfter is how long a Retry-After asks for — seconds, or a date —
// within reason; false where there is none, or it says nothing that can be
// read.
func RetryAfter(values []string, now time.Time) (time.Duration, bool) {
	if len(values) == 0 {
		return 0, false
	}
	value := strings.TrimSpace(values[0])
	var asked time.Duration
	if digitsOnly.MatchString(value) {
		seconds, _ := strconv.ParseFloat(value, 64)
		asked = time.Duration(min(seconds, askedLongest.Seconds()) * float64(time.Second))
	} else {
		at, err := http.ParseTime(value)
		if err != nil {
			return 0, false
		}
		asked = at.Sub(now)
	}
	return min(askedLongest, max(0, asked)), true
}

type response struct {
	status int
	body   []byte
	header http.Header
}

func (r response) expired() bool {
	return r.status == http.StatusUnauthorized || strings.TrimSpace(string(r.body)) == authorizationFailed
}

// asksForFewer is a portal asking for fewer requests: 429, or a 503 that
// says when to come back.
func (r response) asksForFewer() bool {
	return r.status == http.StatusTooManyRequests || r.status == http.StatusServiceUnavailable && len(r.header.Values("Retry-After")) > 0
}

// Params are a call's query, in the order they are sent.
type Params [][2]string

func query(params Params) string {
	parts := make([]string, 0, len(params))
	for _, p := range params {
		parts = append(parts, EncodeURIComponent(p[0])+"="+EncodeURIComponent(p[1]))
	}
	return strings.Join(parts, "&")
}

// EncodeURIComponent is JavaScript's, byte for byte: a portal's ids hold
// colons, and are encoded exactly as the app encodes them.
func EncodeURIComponent(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// send asks one endpoint as the box, through the gate.
func (p *Portal) send(ctx context.Context, endpoint string, params Params, token string, timeout time.Duration, lane Lane) (response, error) {
	mac, err := p.o.Identity.mac()
	if err != nil {
		return response{}, err
	}
	if p.paused() {
		return response{}, throttled()
	}
	address := endpoint + "?" + query(append(slices.Clone(params), [2]string{"JsHttpRequest", "1-xml"}))
	answer, err := p.gate.Run(ctx, lane, func(ctx context.Context) (any, error) {
		// Looked at again as its turn comes: the portal may have refused this
		// MAC address, or asked to be left alone, while it waited.
		if err := p.Refused(); err != nil {
			return nil, err
		}
		if p.paused() {
			return nil, throttled()
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", UserAgent)
		req.Header.Set("X-User-Agent", xUserAgent)
		req.Header.Set("Referer", rootOf(endpoint)+"/c/")
		req.Header.Set("Cookie", "mac="+mac+"; stb_lang=en; timezone=UTC")
		req.Header.Set("Accept", "*/*")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := p.o.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, answerMost))
		if err != nil {
			return nil, err
		}
		return response{status: res.StatusCode, body: body, header: res.Header}, nil
	})
	if err != nil {
		var e *Error
		switch {
		case errors.As(err, &e):
			return response{}, err
		case ctx.Err() != nil:
			return response{}, ctx.Err()
		case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
			// Never the error itself: it names the address, and the MAC address with it.
			return response{}, &Error{Code: "TIMEOUT", Message: "The portal took too long to answer.", Retry: "backoff"}
		}
		return response{}, &Error{Code: "PROVIDER_UNAVAILABLE", Message: "The portal cannot be reached.", Retry: "backoff"}
	}
	r := answer.(response)
	// Asked for fewer requests: no verdict on anything — not "no portal
	// here", not "no series" — and nothing more asked for a while.
	if r.asksForFewer() {
		p.pauseFor(r.header)
		return response{}, throttled()
	}
	if r.status < 400 {
		p.mu.Lock()
		p.nextPause = pauseFirst
		p.mu.Unlock()
	}
	return r, nil
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func (p *Portal) handshake(ctx context.Context, endpoint string) (string, error) {
	r, err := p.send(ctx, endpoint, Params{{"type", "stb"}, {"action", "handshake"}, {"token", ""}}, "", callTimeout, LanePlay)
	if err != nil || r.status != http.StatusOK {
		return "", err
	}
	return text(record(record(parse(r.body))["js"])["token"]), nil
}

// readProfile says whether this MAC address may use the portal: anything but
// yes is a refusal, remembered.
func (p *Portal) readProfile(ctx context.Context, s session) error {
	id := p.o.Identity
	r, err := p.send(ctx, s.Endpoint, Params{
		{"type", "stb"}, {"action", "get_profile"}, {"hd", "1"}, {"ver", profileVer}, {"num_banks", "2"},
		{"sn", id.SerialNumber}, {"stb_type", "MAG250"}, {"image_version", "218"}, {"video_out", "hdmi"},
		{"device_id", id.DeviceID}, {"device_id2", id.DeviceID}, {"signature", id.Signature},
		{"auth_second_step", "1"}, {"hw_version", "1.7-BD-00"}, {"not_valid_token", "0"}, {"api_signature", "262"},
	}, s.Token, callTimeout, LanePlay)
	if err != nil {
		return err
	}
	var profile map[string]any
	if r.status == http.StatusOK {
		profile = record(record(parse(r.body))["js"])
	}
	if r.status == http.StatusUnauthorized || r.status == http.StatusForbidden || strings.TrimSpace(string(r.body)) == authorizationFailed {
		return p.refuse("The portal did not accept this MAC address.")
	}
	if r.status >= 400 {
		return statusError(r.status)
	}
	if blocked, ok := profile["blocked"]; ok && blocked != nil && jsString(blocked) == "1" {
		return p.refuse("This subscription is blocked.")
	}
	if status, ok := profile["status"]; ok && status != nil && jsNumber(status) != 0 {
		return p.refuse("The portal did not accept this MAC address.")
	}
	return nil
}

func (p *Portal) refuse(message string) *Error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refused = &Error{Code: "UNAUTHORIZED", Message: message, Retry: "never"}
	return p.refused
}

// signIn is one sign-in at a time, shared by every caller: the endpoint the
// address answers on — tried in order, once — a token, and the profile. It
// runs on no caller's context, so one caller giving up fails nobody else;
// each stops waiting on its own.
func (p *Portal) signIn(ctx context.Context) (session, error) {
	p.mu.Lock()
	if p.refused != nil {
		defer p.mu.Unlock()
		return session{}, p.refused
	}
	f := p.flight
	if f == nil {
		f = &signing{done: make(chan struct{})}
		p.flight = f
		known := ""
		if p.session != nil {
			known = p.session.Endpoint
		}
		go func() {
			f.session, f.err = p.runSignIn(context.Background(), known)
			p.mu.Lock()
			p.flight = nil
			p.mu.Unlock()
			close(f.done)
		}()
	}
	p.mu.Unlock()
	select {
	case <-f.done:
		return f.session, f.err
	case <-ctx.Done():
		return session{}, ctx.Err()
	}
}

func (p *Portal) runSignIn(ctx context.Context, known string) (session, error) {
	endpoints := p.candidates
	if known != "" {
		endpoints = []string{known}
	}
	for _, endpoint := range endpoints {
		token, err := p.handshake(ctx, endpoint)
		if err != nil {
			return session{}, err
		}
		if token == "" {
			continue
		}
		next := session{Endpoint: endpoint, Token: token}
		if err := p.readProfile(ctx, next); err != nil {
			return session{}, err
		}
		p.mu.Lock()
		p.session = &next
		p.persistLocked()
		p.mu.Unlock()
		return next, nil
	}
	return session{}, &Error{Code: "NOT_FOUND", Message: "No Stalker portal answers at this address.", Retry: "never"}
}

// CallOptions say how long an answer is given and where it waits its turn:
// a page's time and a list's lane where none is said.
type CallOptions struct {
	Timeout time.Duration
	Lane    Lane
}

// Call is one call — type and action as the portal names them — signed in
// first when needed. It answers the `js` part of what the portal said.
func (p *Portal) Call(ctx context.Context, typ, action string, params Params, o CallOptions) (any, error) {
	body, err := p.CallRaw(ctx, typ, action, params, o)
	if err != nil {
		return nil, err
	}
	js, ok := record(parse(body))["js"]
	if !ok {
		return nil, Unreadable()
	}
	return js, nil
}

// CallRaw is Call answering the body as the portal sent it, for an answer
// too large to be read loosely all at once — the whole guide.
func (p *Portal) CallRaw(ctx context.Context, typ, action string, params Params, o CallOptions) ([]byte, error) {
	if o.Timeout == 0 {
		o.Timeout = callTimeout
	}
	if o.Lane == 0 {
		o.Lane = LaneList
	}
	p.mu.Lock()
	if p.refused != nil {
		defer p.mu.Unlock()
		return nil, p.refused
	}
	stored := p.storedLocked()
	p.mu.Unlock()
	var first session
	if stored != nil {
		first = *stored
	} else {
		s, err := p.signIn(ctx)
		if err != nil {
			return nil, err
		}
		first = s
	}
	asked := append(Params{{"type", typ}, {"action", action}}, params...)
	r, err := p.send(ctx, first.Endpoint, asked, first.Token, o.Timeout, o.Lane)
	if err != nil {
		return nil, err
	}
	if r.expired() {
		// The token ran out: one more handshake for this call, and no more —
		// and none at all when another call has renewed it meanwhile.
		p.mu.Lock()
		current := p.session
		p.mu.Unlock()
		renewed := session{}
		if current != nil && current.Token != first.Token {
			renewed = *current
		} else if renewed, err = p.signIn(ctx); err != nil {
			return nil, err
		}
		if r, err = p.send(ctx, renewed.Endpoint, asked, renewed.Token, o.Timeout, o.Lane); err != nil {
			return nil, err
		}
		if r.expired() {
			// A token the portal handed out a moment ago, refused at once: the
			// session was ended from elsewhere — the same MAC address signing
			// in on another box — rather than this one refused. So it is not
			// latched, and nothing loops: this call fails, and the next
			// sign-in waits for the next call.
			p.mu.Lock()
			if p.session != nil && p.session.Token == renewed.Token {
				p.session = nil
				p.persistLocked()
			}
			p.mu.Unlock()
			return nil, &Error{Code: "PROVIDER_UNAVAILABLE", Message: "The portal ended this device’s session. If this MAC address is in use somewhere else, close it there.", Retry: "never"}
		}
	}
	if r.status >= 400 {
		return nil, statusError(r.status)
	}
	return r.body, nil
}

// Check finds the portal behind the address, signs in and reads the
// profile: a connection's test.
func (p *Portal) Check(ctx context.Context) error {
	if _, err := p.o.Identity.mac(); err != nil {
		return err
	}
	_, err := p.signIn(ctx)
	return err
}

// LinkCommand is one channel's id and the command its link is made from.
type LinkCommand struct{ ID, Cmd string }

// LearnLinks learns from a whole list of channels whether every one's link
// command is the plain form for its own id, and remembers that form with the
// session — or forgets it. The form holds nothing but where an id goes; no
// command is ever kept.
func (p *Portal) LearnLinks(commands []LinkCommand) {
	form := ""
	for _, c := range commands {
		m := plainLink.FindStringSubmatch(strings.TrimSpace(c.Cmd))
		each := ""
		if m != nil && m[2] == c.ID {
			each = m[1] + " http://localhost/ch/{id}" + m[3]
		}
		// One that is not plain, or plain another way, and there is no form.
		if each == "" || form != "" && each != form {
			form = ""
			break
		}
		form = each
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.storedLocked()
	if form == p.link {
		return
	}
	p.link = form
	p.persistLocked()
}

// LinkFor is a channel's link command made from its id, where the portal's
// are all the plain form; "" where they are not, or it is not known yet.
func (p *Portal) LinkFor(id string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.storedLocked()
	if p.link == "" {
		return ""
	}
	return strings.Replace(p.link, "{id}", id, 1)
}

// parse reads an answer as JavaScript's JSON.parse would: nothing where it
// cannot.
func parse(body []byte) any {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil
	}
	return v
}

// record is a JSON object, or none.
func record(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// list is a JSON array, or an empty one.
func list(v any) []any {
	l, _ := v.([]any)
	return l
}

// text is a value a portal sends as text: a string, or a number as
// JavaScript writes it; "" for anything else.
func text(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return jsNumberString(t)
	}
	return ""
}

// number is a value a portal sends as a number, or as text holding one.
func number(v any) (float64, bool) {
	var n float64
	switch t := v.(type) {
	case float64:
		n = t
	case string:
		if strings.TrimSpace(t) == "" {
			return 0, false
		}
		n = jsNumber(t)
	default:
		return 0, false
	}
	return n, !math.IsNaN(n) && !math.IsInf(n, 0)
}

// yes is a portal's yes: 1, "1" or true.
func yes(v any) bool {
	switch t := v.(type) {
	case float64:
		return t == 1
	case string:
		return t == "1"
	case bool:
		return t
	}
	return false
}

var (
	jsDecimal = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)
	jsRadix   = regexp.MustCompile(`^0([xXoObB])(\w+)$`)
)

// jsNumber is JavaScript's Number(v) for what JSON holds.
func jsNumber(v any) float64 {
	switch t := v.(type) {
	case nil:
		return 0
	case bool:
		if t {
			return 1
		}
		return 0
	case float64:
		return t
	case string:
		s := strings.TrimSpace(t)
		switch {
		case s == "":
			return 0
		case jsDecimal.MatchString(s):
			n, err := strconv.ParseFloat(s, 64)
			if err != nil && !errors.Is(err, strconv.ErrRange) {
				return math.NaN()
			}
			return n
		case s == "Infinity" || s == "+Infinity":
			return math.Inf(1)
		case s == "-Infinity":
			return math.Inf(-1)
		}
		if m := jsRadix.FindStringSubmatch(s); m != nil {
			base := map[string]int{"x": 16, "o": 8, "b": 2}[strings.ToLower(m[1])]
			if n, err := strconv.ParseUint(m[2], base, 64); err == nil {
				return float64(n)
			}
		}
	}
	return math.NaN()
}

// jsString is JavaScript's String(v) for what JSON holds, as far as a
// portal's answers need it.
func jsString(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return jsNumberString(t)
	case string:
		return t
	}
	return "[object Object]"
}

// jsNumberString writes a number as JavaScript does.
func jsNumberString(n float64) string {
	switch {
	case math.IsNaN(n):
		return "NaN"
	case math.IsInf(n, 1):
		return "Infinity"
	case math.IsInf(n, -1):
		return "-Infinity"
	case n == 0:
		return "0"
	}
	if a := math.Abs(n); a >= 1e-6 && a < 1e21 {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	mantissa, exponent, _ := strings.Cut(strconv.FormatFloat(n, 'e', -1, 64), "e")
	sign, digits := exponent[:1], strings.TrimLeft(exponent[1:], "0")
	return mantissa + "e" + sign + digits
}
