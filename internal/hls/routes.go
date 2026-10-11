package hls

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"foyer/internal/stream"
)

// Prefix is where a remux's addresses live: among the stream addresses, so
// none of them is ever logged, and their limit is theirs.
const Prefix = stream.Prefix + "hls/"

// Address is the master playlist's address for a ticket: a path on this
// server, as a stream address is.
func (m *Manager) Address(t Ticket) (string, error) {
	sealed, err := t.seal(m.sign)
	if err != nil {
		return "", err
	}
	return Prefix + sealed + "/main.m3u8", nil
}

// Routes mounts a remux's addresses: its playlists, its segments and their
// initialization, its subtitles. The ticket in the path is all the
// authority they need, its playing what may stop them.
func (m *Manager) Routes(r *router.Router[*core.RequestEvent]) {
	r.GET(Prefix+"{ticket}/{name}", func(e *core.RequestEvent) error {
		t, ok := openTicket(e.Request.PathValue("ticket"), m.sign, time.Now())
		if !ok {
			return e.ForbiddenError("This address has expired, or never was one.", nil)
		}
		ctx, done, allowed := m.playings.Open(e.Request, t.Session, t.User, t.File, Remux, time.Unix(t.Expires, 0))
		if !allowed {
			return e.ForbiddenError("This playing was stopped.", nil)
		}
		defer done()
		w := stream.Counted(e.Response, func(n int) { m.playings.Sent(t.Session, n) })
		err := m.serve(w, e.Request.WithContext(ctx), t, e.Request.PathValue("name"))
		switch {
		case err == nil, ctx.Err() != nil:
			return nil
		case errors.Is(err, errUnknown):
			return e.NotFoundError("", nil)
		case errors.Is(err, ErrNotYet):
			return e.Error(http.StatusServiceUnavailable, "The segment is not ready yet.", nil)
		}
		m.log("A remux failed", "error", err.Error())
		return e.Error(http.StatusBadGateway, "The file could not be remuxed.", nil)
	})
}

var errUnknown = errors.New("no such part of a remux")

// serve answers one of a remux's addresses.
func (m *Manager) serve(w http.ResponseWriter, r *http.Request, t Ticket, name string) error {
	j, err := m.jobOf(r.Context(), t)
	if err != nil {
		return err
	}
	stream.Lift(w)
	h := w.Header()
	h.Set("Cache-Control", "private")
	switch {
	case name == "main.m3u8":
		return playlist(w, masterPlaylist(j.index, j.version, t.Plan))
	case name == "v.m3u8":
		return playlist(w, mediaPlaylist(j.segments, func(n int) string { return fmt.Sprintf("v%d.m4s", n) }, "v.mp4"))
	case name == "v.mp4":
		return j.serveInit(w, r, "v")
	}
	if k, rest, ok := part(name, "a", len(t.Plan.Audio)); ok {
		switch {
		case rest == ".m3u8":
			return playlist(w, mediaPlaylist(j.segments, func(n int) string { return fmt.Sprintf("a%d-%d.m4s", k, n) }, fmt.Sprintf("a%d.mp4", k)))
		case rest == ".mp4":
			return j.serveInit(w, r, "a"+strconv.Itoa(k))
		}
		if n, ok := numbered(rest, ".m4s", len(j.segments)); ok {
			return j.serveSegment(w, r, "a"+strconv.Itoa(k), n)
		}
		return errUnknown
	}
	if k, rest, ok := part(name, "s", len(t.Plan.Subtitles)); ok {
		if rest == ".m3u8" {
			return playlist(w, mediaPlaylist(j.segments, func(n int) string { return fmt.Sprintf("s%d-%d.vtt", k, n) }, ""))
		}
		if n, ok := numbered(rest, ".vtt", len(j.segments)); ok {
			return j.serveSubtitles(w, r, k, n)
		}
		return errUnknown
	}
	if n, ok := numbered(strings.TrimPrefix(name, "v"), ".m4s", len(j.segments)); ok && strings.HasPrefix(name, "v") {
		return j.serveSegment(w, r, "v", n)
	}
	return errUnknown
}

// part reads `a2.m3u8` or `a2-14.m4s`: the rendition, and what follows.
func part(name, kind string, count int) (int, string, bool) {
	rest, ok := strings.CutPrefix(name, kind)
	if !ok {
		return 0, "", false
	}
	end := strings.IndexAny(rest, ".-")
	if end <= 0 {
		return 0, "", false
	}
	k, err := strconv.Atoi(rest[:end])
	if err != nil || k < 0 || k >= count {
		return 0, "", false
	}
	return k, strings.TrimPrefix(rest[end:], "-"), true
}

// numbered reads `14.m4s`: a segment's number, within the timeline.
func numbered(s, ext string, count int) (int, bool) {
	number, ok := strings.CutSuffix(s, ext)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(number)
	return n, err == nil && n >= 0 && n < count && strconv.Itoa(n) == number
}

func playlist(w http.ResponseWriter, body string) error {
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, err := io.WriteString(w, body)
	return err
}

func contentType(kind string) string {
	if strings.HasPrefix(kind, "a") {
		return "audio/mp4"
	}
	return "video/mp4"
}

// serveInit answers a rendition's initialization segment: what precedes the
// first fragment of any segment of it — the same for all — made first where
// none is.
func (j *job) serveInit(w http.ResponseWriter, r *http.Request, kind string) error {
	var init []byte
	err := j.await(r.Context(), func() (bool, error) {
		if init = j.inits[kind]; init != nil {
			return true, nil
		}
		for _, path := range j.done[kind] {
			if b, err := readInit(path); err == nil {
				j.inits[kind], init = b, b
				return true, nil
			}
		}
		return false, j.need(kind, j.asked)
	})
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", contentType(kind))
	w.Header().Set("Content-Length", strconv.Itoa(len(init)))
	_, err = w.Write(init)
	return err
}

// serveSegment answers a segment of a rendition: its fragments, without
// the initialization before them, waiting for it to be made.
func (j *job) serveSegment(w http.ResponseWriter, r *http.Request, kind string, n int) error {
	var file *os.File
	err := j.await(r.Context(), func() (bool, error) {
		path := j.done[kind][n]
		if path == "" {
			return false, j.need(kind, n)
		}
		f, err := os.Open(path)
		if err != nil {
			// Let go of just now: made again.
			delete(j.done[kind], n)
			return false, j.need(kind, n)
		}
		if kind == "v" {
			j.asked = n
		}
		file = f
		return true, nil
	})
	if err != nil {
		return err
	}
	defer file.Close()
	at, err := firstFragment(file)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", contentType(kind))
	http.ServeContent(w, r, "", time.Time{}, io.NewSectionReader(file, at, info.Size()-at))
	return nil
}

// serveSubtitles answers a segment of a subtitle rendition once a run has
// got through it — or with what there is, once waiting is over: a cue missed
// is better than a player stuck on its subtitles.
func (j *job) serveSubtitles(w http.ResponseWriter, r *http.Request, k, n int) error {
	segment := j.segments[n]
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	err := j.await(ctx, func() (bool, error) {
		for _, s := range j.covered {
			if s.from <= segment.StartMs && s.to >= segment.EndMs {
				return true, nil
			}
		}
		// A player asks for subtitles where it is: with no run going, one
		// starts there; a run elsewhere is the picture's to move.
		if j.run == nil || j.run.over() {
			return false, j.need("v", n)
		}
		return false, nil
	})
	if err != nil && r.Context().Err() != nil {
		return err
	}
	j.mu.Lock()
	body := vttSegment(j.cues[k], segment)
	j.mu.Unlock()
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, err = w.Write(body)
	return err
}

// readInit is a segment file's initialization: everything before its first
// fragment.
func readInit(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	at, err := firstFragment(f)
	if err != nil {
		return nil, err
	}
	b := make([]byte, at)
	_, err = io.ReadFull(f, b)
	return b, err
}

// firstFragment is where a segment file's first fragment begins, and leaves
// the file there.
func firstFragment(f *os.File) (int64, error) {
	at := int64(0)
	header := make([]byte, 16)
	for range 16 {
		if _, err := f.ReadAt(header[:8], at); err != nil {
			return 0, err
		}
		size := int64(binary.BigEndian.Uint32(header))
		if size == 1 {
			if _, err := f.ReadAt(header, at); err != nil {
				return 0, err
			}
			size = int64(binary.BigEndian.Uint64(header[8:]))
		}
		if string(header[4:8]) == "moof" {
			_, err := f.Seek(0, io.SeekStart)
			return at, err
		}
		if size < 8 {
			break
		}
		at += size
	}
	return 0, errors.New("a segment without a fragment")
}
