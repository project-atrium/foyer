package youtube

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"foyer/internal/stream"
)

// Prefix is where a video's relayed streams live: under the stream prefix,
// so none of their addresses reaches the log.
const Prefix = stream.Prefix + "yt/"

// agent is who Foyer says it is to the site's streams.
const agent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

// Relay hands a device the site's streams through Foyer: each link sealed in
// an address of Foyer's own, under the playing it belongs to.
type Relay struct {
	sign     func(label string, payload []byte) ([]byte, error)
	derive   func(label string) ([]byte, error)
	playings stream.Playings
	// Client fetches from the site: over IPv4, as yt-dlp asked, since the
	// site's links are bound to the address that asked for them.
	Client *http.Client
	// warm holds what a player asks for first, fetched while it reads the master.
	warm *warm
}

// NewRelay relays under keys drawn by derive and sign, each address opened
// under its playing.
func NewRelay(sign func(string, []byte) ([]byte, error), derive func(string) ([]byte, error), playings stream.Playings) *Relay {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	r := &Relay{sign: sign, derive: derive, playings: playings, Client: &http.Client{
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, "tcp4", addr)
			},
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || req.URL.Scheme != "https" || !siteHost(req.URL.Hostname()) {
				return errors.New("the site sent the stream somewhere else")
			}
			return nil
		},
	}}
	r.warm = newWarm(r.Client)
	return r
}

// playlistOf is a playlist of the site's: fetched ahead where it was, else
// asked for now — and whether it had been fetched ahead.
func (r *Relay) playlistOf(ctx context.Context, link *url.URL) ([]byte, bool, error) {
	if body, _, ok := r.warm.take(ctx, link.String()); ok {
		return body, true, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.String(), nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", agent)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, false, errors.New("The site did not answer.")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, errors.New("The site refused the playlist (" + strconv.Itoa(resp.StatusCode) + ").")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, false, errors.New("The site broke off.")
	}
	return body, false, nil
}

// ticket is what a relayed address carries in its path: the video, the
// user, the playing, and when it expires — signed.
type ticket struct {
	ID      string `json:"i"`
	User    string `json:"u"`
	Session string `json:"s"`
	Expires int64  `json:"e"`
	Fit     fit    `json:"f,omitzero"`
}

func (r *Relay) issue(t ticket) (string, error) {
	payload, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	mac, err := r.sign("youtube ticket v1", payload)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac[:16]), nil
}

func (r *Relay) read(raw string, now time.Time) (ticket, bool) {
	payload, sig, ok := strings.Cut(raw, ".")
	if !ok {
		return ticket{}, false
	}
	body, err1 := base64.RawURLEncoding.DecodeString(payload)
	given, err2 := base64.RawURLEncoding.DecodeString(sig)
	if err1 != nil || err2 != nil {
		return ticket{}, false
	}
	mac, err := r.sign("youtube ticket v1", body)
	if err != nil || !hmac.Equal(mac[:16], given) {
		return ticket{}, false
	}
	var t ticket
	if json.Unmarshal(body, &t) != nil || now.Unix() > t.Expires || t.Session == "" || !ValidID.MatchString(t.ID) {
		return ticket{}, false
	}
	return t, true
}

func (r *Relay) sealer() (*sealer, error) {
	key, err := r.derive("youtube relay v1")
	if err != nil {
		return nil, err
	}
	return newSealer(key)
}

// address is a link of the site's as an address of Foyer's: a playlist (p),
// or one of captions (s), to be rewritten in turn, or bytes (x) passed on —
// with the extension of what it is, since ffmpeg's HLS reader, mpv's too,
// refuses a segment whose address names none it knows, or one that is not
// what it finds inside.
func address(base, kind, token, ext string) string {
	return base + "/" + kind + "/" + token + ext
}

// known are the extensions a relayed address may end in: every kind of
// segment and caption the site serves, and what ffmpeg reads them as.
var known = []string{".ts", ".m4s", ".mp4", ".m4a", ".m4v", ".aac", ".webm", ".vtt", ".mp3"}

// extOf is the extension a relayed address of link takes: a playlist's is
// .m3u8; bytes keep the site's own where it is one ffmpeg knows, else what
// they are taken for.
func extOf(kind string, link *url.URL, otherwise string) string {
	if kind == "p" || kind == "s" {
		return ".m3u8"
	}
	if ext := strings.ToLower(path.Ext(link.Path)); slices.Contains(known, ext) {
		return ext
	}
	return otherwise
}

// tokenOf is the token in a relayed address, its extension let go of: a
// token is base64 with no dot in it.
func tokenOf(segment string) string {
	if i := strings.LastIndexByte(segment, '.'); i >= 0 {
		return segment[:i]
	}
	return segment
}

// Routes mounts the relayed addresses: playlists rewritten, bytes passed on
// by ranges.
func (r *Relay) Routes(rt *router.Router[*core.RequestEvent]) {
	rt.GET(Prefix+"{ticket}/p/{token}", r.playlist(false))
	rt.GET(Prefix+"{ticket}/s/{token}", r.playlist(true))
	rt.GET(Prefix+"{ticket}/x/{token}", r.bytes)
}

// opened is a relayed request's ticket and link, under its playing.
func (r *Relay) opened(e *core.RequestEvent) (context.Context, func(), *url.URL, string, ticket, error) {
	raw := e.Request.PathValue("ticket")
	t, ok := r.read(raw, time.Now())
	if !ok {
		return nil, nil, nil, "", t, e.ForbiddenError("This address has expired, or never was one.", nil)
	}
	s, err := r.sealer()
	if err != nil {
		return nil, nil, nil, "", t, e.InternalServerError("", err)
	}
	link, err := s.open(tokenOf(e.Request.PathValue("token")))
	if err != nil {
		return nil, nil, nil, "", t, e.NotFoundError("", nil)
	}
	ctx, done, allowed := r.playings.Open(e.Request, t.Session, t.User, "youtube:"+t.ID, "relay", time.Unix(t.Expires, 0))
	if !allowed {
		return nil, nil, nil, "", t, e.ForbiddenError("This playing was stopped.", nil)
	}
	return ctx, done, link, Prefix + raw, t, nil
}

// playlist answers a playlist rewritten: a master or a rendition's, or —
// captions — one whose segments are WebVTT.
func (r *Relay) playlist(captions bool) func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error { return r.rewritten(e, captions) }
}

func (r *Relay) rewritten(e *core.RequestEvent, captions bool) error {
	ctx, done, link, base, t, err := r.opened(e)
	if err != nil {
		return err
	}
	defer done()
	body, ok, err := r.playlistOf(ctx, link)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return e.Error(http.StatusBadGateway, err.Error(), nil)
	}
	s, err := r.sealer()
	if err != nil {
		return e.InternalServerError("", err)
	}
	body = fitted(body, t.Fit)
	if !ok && strings.Contains(string(body), "#EXT-X-STREAM-INF") {
		// What the player asks for next, fetched while it reads this.
		r.warm.ahead(body, link)
	}
	out, err := rewrite(body, link, base, s, captions)
	if err != nil {
		return e.InternalServerError("", err)
	}
	h := e.Response.Header()
	h.Set("Content-Type", "application/vnd.apple.mpegurl")
	// A live stream's playlist moves on: never kept.
	h.Set("Cache-Control", "no-cache")
	return e.Blob(http.StatusOK, "application/vnd.apple.mpegurl", out)
}

var uriAttribute = regexp.MustCompile(`URI="([^"]*)"`)

// rewrite is a playlist with every link in it Foyer's own: in a master, the
// playlists it names; in a media playlist, its segments, its map and its
// key. A playlist of captions holds WebVTT.
func rewrite(body []byte, from *url.URL, base string, s *sealer, captions bool) ([]byte, error) {
	text := string(body)
	master := strings.Contains(text, "#EXT-X-STREAM-INF") || strings.Contains(text, "#EXT-X-MEDIA:")
	kind := "x"
	if master {
		kind = "p"
	}
	// A media playlist's segments are MPEG-TS, unless it maps an init
	// segment: then fragments of MP4; or captions.
	segment := ".ts"
	switch {
	case captions:
		segment = ".vtt"
	case strings.Contains(text, "#EXT-X-MAP:"):
		segment = ".m4s"
	}
	var failed error
	relay := func(ref, kind, otherwise string) string {
		target, err := from.Parse(ref)
		if err != nil {
			failed = err
			return ref
		}
		token, err := s.seal(target.String())
		if err != nil {
			failed = err
			return ref
		}
		return address(base, kind, token, extOf(kind, target, otherwise))
	}
	var out strings.Builder
	out.Grow(len(text) + len(text)/2)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == "":
		case strings.HasPrefix(line, "#"):
			// A map is its fragments' MP4 header; a key, bytes; a master's
			// rendition of captions, a playlist of WebVTT.
			otherwise, of := ".mp4", kind
			switch {
			case strings.HasPrefix(line, "#EXT-X-KEY"):
				otherwise = ".key"
			case master && strings.HasPrefix(line, "#EXT-X-MEDIA:") && strings.Contains(line, "TYPE=SUBTITLES"):
				of = "s"
			}
			line = uriAttribute.ReplaceAllStringFunc(line, func(attr string) string {
				return `URI="` + relay(uriAttribute.FindStringSubmatch(attr)[1], of, otherwise) + `"`
			})
		default:
			line = relay(line, kind, segment)
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	if failed != nil {
		return nil, failed
	}
	return []byte(strings.TrimRight(out.String(), "\n") + "\n"), nil
}

// bytes passes the site's bytes on: a segment, a caption, a muxed file by
// ranges — the deadlines lifted, as a stream's are.
func (r *Relay) bytes(e *core.RequestEvent) error {
	ctx, done, link, _, t, err := r.opened(e)
	if err != nil {
		return err
	}
	defer done()
	w := stream.Counted(e.Response, func(n int) { r.playings.Sent(t.Session, n) })
	rc := stream.Lift(w)
	// A segment fetched ahead, asked for whole.
	if rng := e.Request.Header.Get("Range"); e.Request.Method == http.MethodGet && (rng == "" || rng == "bytes=0-") {
		if body, ctype, ok := r.warm.take(ctx, link.String()); ok {
			h := w.Header()
			if ctype != "" {
				h.Set("Content-Type", ctype)
			}
			h.Set("Content-Length", strconv.Itoa(len(body)))
			h.Set("Cache-Control", "private")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, e.Request.Method, link.String(), nil)
	if err != nil {
		return e.InternalServerError("", err)
	}
	req.Header.Set("User-Agent", agent)
	if rng := e.Request.Header.Get("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return e.Error(http.StatusBadGateway, "The site did not answer.", nil)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent, http.StatusRequestedRangeNotSatisfiable:
	default:
		return e.Error(http.StatusBadGateway, "The site refused the stream ("+strconv.Itoa(resp.StatusCode)+").", nil)
	}
	h := w.Header()
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	h.Set("Cache-Control", "private")
	w.WriteHeader(resp.StatusCode)
	if e.Request.Method == http.MethodHead {
		return nil
	}
	buf := make([]byte, 256<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			_ = rc.SetWriteDeadline(time.Now().Add(stream.StallAfter))
			if _, err := w.Write(buf[:n]); err != nil {
				return nil
			}
			_ = rc.Flush()
		}
		if readErr != nil {
			return nil
		}
	}
}
