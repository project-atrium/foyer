package copies

import (
	"crypto/hmac"
	"encoding/base64"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"foyer/internal/stream"
)

// Prefix is where made copies are fetched from: under the stream prefix,
// so no address of one reaches the log.
const Prefix = stream.Prefix + "copy/"

// Signer signs what a copy's address carries, with a key of its own.
type Signer func(payload []byte) ([]byte, error)

// SignLabel is what a copy's addresses are signed under: a key of their own.
const SignLabel = "copy address v1"

func (m *Manager) sign(key, user, session string, expires int64) (string, error) {
	mac, err := m.o.Sign([]byte("copy v1|" + key + "|" + user + "|" + session + "|" + strconv.FormatInt(expires, 10)))
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(mac[:16]), nil
}

// Address is a made copy's address for a user's playing, good until
// expires: a download that stopped asks for another, and carries on.
func (m *Manager) Address(key, user, session string, expires time.Time) (string, error) {
	sig, err := m.sign(key, user, session, expires.Unix())
	if err != nil {
		return "", err
	}
	q := url.Values{"u": {user}, "s": {session}, "exp": {strconv.FormatInt(expires.Unix(), 10)}, "sig": {sig}}
	return Prefix + key + "?" + q.Encode(), nil
}

// Routes mounts the made copies' addresses — GET and HEAD, by ranges — each
// under the playing it belongs to, which a superuser's Stop ends; and the
// dashboard's view of the copies.
func (m *Manager) Routes(r *router.Router[*core.RequestEvent], admin *router.RouterGroup[*core.RequestEvent], playings stream.Playings) {
	r.GET(Prefix+"{key}", func(e *core.RequestEvent) error {
		key, q := e.Request.PathValue("key"), e.Request.URL.Query()
		expires, err := strconv.ParseInt(q.Get("exp"), 10, 64)
		if err != nil || time.Now().Unix() > expires || q.Get("s") == "" {
			return e.ForbiddenError("This address has expired, or never was one.", nil)
		}
		want, err := m.sign(key, q.Get("u"), q.Get("s"), expires)
		if err != nil || !hmac.Equal([]byte(want), []byte(q.Get("sig"))) {
			return e.ForbiddenError("This address has expired, or never was one.", nil)
		}
		path, made, ok := m.Path(key)
		if !ok {
			return e.NotFoundError("", nil)
		}
		w, req := http.ResponseWriter(e.Response), e.Request
		if playings != nil {
			session := q.Get("s")
			ctx, done, allowed := playings.Open(req, session, q.Get("u"), made.File, "copy", time.Unix(expires, 0))
			if !allowed {
				return e.ForbiddenError("This playing was stopped.", nil)
			}
			defer done()
			req = req.WithContext(ctx)
			w = stream.Counted(w, func(n int) { playings.Sent(session, n) })
		}
		f, err := os.Open(path)
		if err != nil {
			return e.NotFoundError("", nil)
		}
		defer f.Close()
		rc := stream.Lift(w)
		w.Header().Set("Content-Type", contentType(made.Encode.Container))
		w.Header().Set("Cache-Control", "private")
		http.ServeContent(&stalling{ResponseWriter: w, rc: rc}, req, made.Name, made.Made, f)
		return nil
	})

	admin.GET("/copies", func(e *core.RequestEvent) error {
		return e.JSON(http.StatusOK, map[string]any{"making": m.Making(), "kept": m.Kept()})
	}).Bind(apis.SkipSuccessActivityLog())
	admin.POST("/copies/{key}/stop", func(e *core.RequestEvent) error {
		if !m.Stop(e.Request.PathValue("key")) {
			return e.NotFoundError("Nothing of that is being made.", nil)
		}
		return e.NoContent(http.StatusNoContent)
	})
	admin.DELETE("/copies/{key}", func(e *core.RequestEvent) error {
		if !m.Remove(e.Request.PathValue("key")) {
			return e.NotFoundError("No such copy is kept.", nil)
		}
		return e.NoContent(http.StatusNoContent)
	})
}

// stalling is a response whose writes may take as long as a player reads,
// and no longer than StallAfter each.
type stalling struct {
	http.ResponseWriter
	rc *http.ResponseController
}

func (s *stalling) Write(b []byte) (int, error) {
	_ = s.rc.SetWriteDeadline(time.Now().Add(stream.StallAfter))
	return s.ResponseWriter.Write(b)
}

func contentType(container string) string {
	if container == "mp4" {
		return "video/mp4"
	}
	return "video/x-matroska"
}
