// Package stream hands a file's bytes on as its source answers them: the
// part a player asks for, through Foyer, so the share's address and password
// never leave the server. An address is signed — a player sends no headers — and
// good for hours, not days; and it never lands in a log, since it opens the
// file to whoever holds it.
package stream

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/pocketbase/pocketbase/tools/router"
)

// Prefix is where stream addresses live.
const Prefix = "/api/foyer/stream/"

// Lifetime is how long an address is good: a long film and its pauses.
const Lifetime = 6 * time.Hour

var (
	// ErrGone is a file its source no longer has.
	ErrGone = errors.New("the file is gone")
	// ErrSource is a source failing to answer as it should.
	ErrSource = errors.New("the file's source did not answer")
)

// Opener opens a file's bytes, from the file's record: a Range passed on, a
// HEAD asked for as one.
type Opener func(ctx context.Context, file *core.Record, header http.Header, head bool) (*http.Response, error)

// Playings are what every address belongs to: a playing, which a superuser
// may stop.
type Playings interface {
	// Open is a request for a playing's bytes begun, served in mode: its
	// context ends when the playing is stopped, and done is called as it
	// ends. False for a playing stopped, or one the address does not belong
	// to.
	Open(r *http.Request, session, user, file, mode string, expires time.Time) (context.Context, func(), bool)
	// Sent is bytes of a playing on their way.
	Sent(session string, n int)
}

// Streamer signs addresses and serves them.
type Streamer struct {
	app        core.App
	collection string
	open       Opener
	playings   Playings
	twin       *twin
}

// SetPlayings says what each address's playing is allowed.
func (s *Streamer) SetPlayings(p Playings) { s.playings = p }

// New serves files of a collection — `media_files` — opened by open.
func New(app core.App, collection string, open Opener) *Streamer {
	return &Streamer{app: app, collection: collection, open: open}
}

// key is the signing key, derived from the users collection's file-token
// secret under a label of its own: regenerating that secret in the dashboard
// ends every address at once, and signs nobody out.
func (s *Streamer) key() ([]byte, error) {
	users, err := s.app.FindCachedCollectionByNameOrId("users")
	if err != nil {
		return nil, err
	}
	return hkdf.Key(sha256.New, []byte(users.FileToken.Secret), nil, "foyer stream address v1", 32)
}

// Sign signs what else hands out addresses — a remux's tickets — with a key
// of its own, drawn from the same secret: regenerating it ends those too.
func (s *Streamer) Sign(label string, payload []byte) ([]byte, error) {
	users, err := s.app.FindCachedCollectionByNameOrId("users")
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Key(sha256.New, []byte(users.FileToken.Secret), nil, "foyer "+label, 32)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return mac.Sum(nil), nil
}

// sign is an address's signature: over its file, its user, the playing it
// belongs to and when it expires.
func (s *Streamer) sign(file, user, session string, expires int64) (string, error) {
	key, err := s.key()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("v2|" + file + "|" + user + "|" + session + "|" + strconv.FormatInt(expires, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16]), nil
}

// Address is a file's address for a user, good until expires: a path on this
// server, so a proxy's own base path keeps working.
func (s *Streamer) Address(file, user, session string, expires time.Time) (string, error) {
	sig, err := s.sign(file, user, session, expires.Unix())
	if err != nil {
		return "", err
	}
	q := url.Values{"u": {user}, "s": {session}, "exp": {strconv.FormatInt(expires.Unix(), 10)}, "sig": {sig}}
	return Prefix + url.PathEscape(file) + "?" + q.Encode(), nil
}

// valid reads an address's signature: when it expires, if it is one.
func (s *Streamer) valid(file string, q url.Values, now time.Time) (time.Time, bool) {
	expires, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil || now.Unix() > expires || q.Get("s") == "" {
		return time.Time{}, false
	}
	want, err := s.sign(file, q.Get("u"), q.Get("s"), expires)
	return time.Unix(expires, 0), err == nil && hmac.Equal([]byte(want), []byte(q.Get("sig")))
}

// Routes mounts the stream addresses: GET and HEAD. The signature is all the
// authority they need.
func (s *Streamer) Routes(r *router.Router[*core.RequestEvent]) {
	r.GET(Prefix+"{file}", func(e *core.RequestEvent) error {
		file, q := e.Request.PathValue("file"), e.Request.URL.Query()
		expires, ok := s.valid(file, q, time.Now())
		if !ok {
			return e.ForbiddenError("This address has expired, or never was one.", nil)
		}
		record, err := s.app.FindRecordById(s.collection, file)
		if err != nil {
			return e.NotFoundError("", nil)
		}
		w, req := http.ResponseWriter(e.Response), e.Request
		if s.playings != nil {
			session := q.Get("s")
			ctx, done, allowed := s.playings.Open(req, session, q.Get("u"), file, "direct", expires)
			if !allowed {
				return e.ForbiddenError("This playing was stopped.", nil)
			}
			defer done()
			req = req.WithContext(ctx)
			w = Counted(w, func(n int) { s.playings.Sent(session, n) })
		}
		return Proxy(w, req, record.GetString("name"), func(ctx context.Context, header http.Header, head bool) (*http.Response, error) {
			return s.open(ctx, record, header, head)
		})
	})
}

// Quiet keeps stream addresses out of PocketBase's request log, whose every
// entry holds its address — and the signature with it. It runs inside the
// logger and outside everything else, so even a refusal, a limiter's 429 or
// a recovered panic is answered here and leaves nothing to log.
func Quiet() *hook.Handler[*core.RequestEvent] {
	return &hook.Handler[*core.RequestEvent]{
		Id:       "foyerQuietStream",
		Priority: apis.DefaultActivityLoggerMiddlewarePriority + 5,
		Func: func(e *core.RequestEvent) error {
			if !strings.HasPrefix(e.Request.URL.Path, Prefix) {
				return e.Next()
			}
			err := apis.SkipSuccessActivityLog().Func(e)
			if err == nil {
				return nil
			}
			apiErr := router.ToApiError(err)
			if !e.Written() {
				_ = e.JSON(apiErr.Status, map[string]any{"status": apiErr.Status, "message": apiErr.Message, "data": map[string]any{}})
			}
			return nil
		},
	}
}

// Counted is a response that says how much of it went, as it goes.
func Counted(w http.ResponseWriter, sent func(n int)) http.ResponseWriter {
	return &counting{ResponseWriter: w, sent: sent}
}

// counting is a response that says how much of it went.
type counting struct {
	http.ResponseWriter
	sent func(n int)
}

func (c *counting) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	if n > 0 {
		c.sent(n)
	}
	return n, err
}

// Unwrap lets a ResponseController reach the connection's deadlines.
func (c *counting) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// Lift lifts the five-minute deadlines PocketBase's server reads and writes
// with, which would end a film five minutes in: whoever streams keeps the
// write's moving instead (StallAfter).
func Lift(w http.ResponseWriter) *http.ResponseController {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	return rc
}

// StallAfter is how long a write may stall before a stream ends.
const StallAfter = 30 * time.Minute

// Proxy answers a request with what open answers: the headers that describe
// the bytes, then the bytes, for as long as the player reads — the
// deadlines lifted, and a write that stalls half an hour ending it.
func Proxy(w http.ResponseWriter, r *http.Request, name string, open func(ctx context.Context, header http.Header, head bool) (*http.Response, error)) error {
	rc := Lift(w)

	head := r.Method == http.MethodHead
	response, err := open(r.Context(), r.Header, head)
	if err != nil {
		if r.Context().Err() != nil {
			return nil
		}
		status := http.StatusBadGateway
		if errors.Is(err, ErrGone) {
			status = http.StatusNotFound
		}
		http.Error(w, http.StatusText(status), status)
		return nil
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusOK, http.StatusPartialContent, http.StatusNotModified, http.StatusRequestedRangeNotSatisfiable:
	default:
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		return nil
	}
	h := w.Header()
	for _, k := range []string{"Content-Range", "ETag"} {
		if v := response.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	// Zurg dates a file in ISO form, which no player reads as HTTP's.
	if v := response.Header.Get("Last-Modified"); v != "" {
		if _, err := http.ParseTime(v); err == nil {
			h.Set("Last-Modified", v)
		}
	}
	// Zurg sends every file in chunks, a part even where none was asked for,
	// and never says how long: a player that knows no length cannot seek.
	status, length := response.StatusCode, int64(-1)
	if n, err := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64); err == nil && n >= 0 {
		length = n
	}
	if first, last, total, ok := contentRange(h.Get("Content-Range")); ok && status == http.StatusPartialContent {
		if r.Header.Get("Range") == "" && first == 0 && last == total-1 {
			status, length = http.StatusOK, total
			h.Del("Content-Range")
		} else if length < 0 {
			length = last - first + 1
		}
	}
	if length >= 0 && status != http.StatusNotModified {
		h.Set("Content-Length", strconv.FormatInt(length, 10))
	}
	if status == http.StatusOK || status == http.StatusPartialContent {
		h.Set("Accept-Ranges", "bytes")
	}
	h.Set("Content-Type", contentType(name))
	h.Set("Cache-Control", "private")
	w.WriteHeader(status)
	if head || response.StatusCode == http.StatusNotModified {
		return nil
	}
	buf := make([]byte, 256<<10)
	for {
		n, readErr := response.Body.Read(buf)
		if n > 0 {
			_ = rc.SetWriteDeadline(time.Now().Add(StallAfter))
			if _, err := w.Write(buf[:n]); err != nil {
				// The player went: a seek, or the end of watching.
				return nil
			}
			// What a slow source sent goes on at once, not when the
			// server's buffer fills: a live stream trickles.
			_ = rc.Flush()
		}
		if readErr != nil {
			// The end, or the source broke off: there is nothing more to say
			// in a body already begun.
			return nil
		}
	}
}

// contentRange reads "bytes first-last/total".
func contentRange(value string) (first, last, total int64, ok bool) {
	spec, found := strings.CutPrefix(strings.TrimSpace(value), "bytes ")
	if !found {
		return 0, 0, 0, false
	}
	span, size, found := strings.Cut(spec, "/")
	from, to, found2 := strings.Cut(span, "-")
	if !found || !found2 {
		return 0, 0, 0, false
	}
	var err1, err2, err3 error
	first, err1 = strconv.ParseInt(from, 10, 64)
	last, err2 = strconv.ParseInt(to, 10, 64)
	total, err3 = strconv.ParseInt(size, 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || first < 0 || last < first || total <= last {
		return 0, 0, 0, false
	}
	return first, last, total, true
}

// contentType is a file's type by its extension: players read it.
func contentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".mkv":
		return "video/x-matroska"
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".avi":
		return "video/x-msvideo"
	case ".ts", ".m2ts", ".mts":
		return "video/mp2t"
	case ".mov":
		return "video/quicktime"
	case ".wmv":
		return "video/x-ms-wmv"
	case ".mpg", ".mpeg":
		return "video/mpeg"
	default:
		return "application/octet-stream"
	}
}
