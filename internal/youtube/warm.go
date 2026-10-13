package youtube

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// warmFor is how long what was fetched ahead is kept for the player to
	// ask for.
	warmFor = time.Minute
	// warmMost is the most it holds at once, and warmEach the largest
	// segment it keeps.
	warmMost = 48 << 20
	warmEach = 8 << 20
	// warmAtOnce is how many it asks the site for at once.
	warmAtOnce = 6
)

// warm is what a player asks for first, fetched from the site while it reads
// a master: every playlist the master names, and each one's map and first
// segment. ffmpeg's HLS reader — mpv's — asks for every one of them in turn
// before it starts, each through Foyer to the site and back; fetched ahead,
// a few at once, each is here when it asks. Only a video that is over — a
// playlist with its end — is fetched ahead: a live one's moves on.
type warm struct {
	client *http.Client

	mu      sync.Mutex
	entries map[string]*warmed
	held    int
}

type warmed struct {
	done  chan struct{}
	body  []byte
	ctype string
	at    time.Time
}

func newWarm(client *http.Client) *warm {
	return &warm{client: client, entries: map[string]*warmed{}}
}

// take is what was fetched ahead of link, waiting for it while it comes:
// false where nothing was, or it could not be had.
func (w *warm) take(ctx context.Context, link string) ([]byte, string, bool) {
	w.mu.Lock()
	e := w.entries[link]
	w.mu.Unlock()
	if e == nil {
		return nil, "", false
	}
	select {
	case <-e.done:
	case <-ctx.Done():
		return nil, "", false
	}
	if e.body == nil || time.Since(e.at) > warmFor {
		return nil, "", false
	}
	return e.body, e.ctype, true
}

// claim makes a link's entry, false where it has one already.
func (w *warm) claim(link string) (*warmed, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	for k, e := range w.entries {
		select {
		case <-e.done:
			if now.Sub(e.at) > warmFor {
				w.held -= len(e.body)
				delete(w.entries, k)
			}
		default:
		}
	}
	if _, ok := w.entries[link]; ok {
		return nil, false
	}
	e := &warmed{done: make(chan struct{}), at: now}
	w.entries[link] = e
	return e, true
}

func (w *warm) keep(e *warmed, body []byte, ctype string) {
	w.mu.Lock()
	if body != nil && w.held+len(body) <= warmMost {
		e.body, e.ctype = body, ctype
		w.held += len(body)
	}
	e.at = time.Now()
	w.mu.Unlock()
	close(e.done)
}

// get fetches a link from the site whole, within limit: nothing where it is
// not there to be had, or larger.
func (w *warm) get(ctx context.Context, link string, limit int64) ([]byte, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, ""
	}
	req.Header.Set("User-Agent", agent)
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > limit {
		return nil, ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, ""
	}
	return body, resp.Header.Get("Content-Type")
}

// ahead fetches every playlist a master names, a few at once, and each
// one's map and first segment after it — on no request's context: the
// master's answer is gone long before.
func (w *warm) ahead(master []byte, from *url.URL) {
	links := playlistsOf(master, from)
	if len(links) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	slots := make(chan struct{}, warmAtOnce)
	var wg sync.WaitGroup
	for _, link := range links {
		e, ok := w.claim(link.String())
		if !ok {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			body, ctype := w.get(ctx, link.String(), 4<<20)
			<-slots
			// A live stream's playlist moves on: never kept, nor its segments fetched.
			if body == nil || !strings.Contains(string(body), "#EXT-X-ENDLIST") {
				w.keep(e, nil, "")
				return
			}
			w.keep(e, body, ctype)
			for _, first := range firstOf(body, link) {
				s, ok := w.claim(first.String())
				if !ok {
					continue
				}
				slots <- struct{}{}
				segment, ctype := w.get(ctx, first.String(), warmEach)
				<-slots
				w.keep(s, segment, ctype)
			}
		}()
	}
	go func() {
		wg.Wait()
		cancel()
	}()
}

// playlistsOf are the playlists a master names: its pictures', and its
// sounds' and captions'.
func playlistsOf(master []byte, from *url.URL) []*url.URL {
	var out []*url.URL
	add := func(ref string) {
		if u, err := from.Parse(ref); err == nil && u.Scheme == "https" && siteHost(u.Hostname()) {
			out = append(out, u)
		}
	}
	for _, line := range strings.Split(string(master), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXT-X-MEDIA:"):
			if m := uriAttribute.FindStringSubmatch(line); m != nil {
				add(m[1])
			}
		case !strings.HasPrefix(line, "#"):
			add(line)
		}
	}
	return out
}

// firstOf is what a reader asks of a media playlist first: its map, and its
// first segment.
func firstOf(playlist []byte, from *url.URL) []*url.URL {
	var out []*url.URL
	for _, line := range strings.Split(string(playlist), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			if m := uriAttribute.FindStringSubmatch(line); m != nil {
				if u, err := from.Parse(m[1]); err == nil {
					out = append(out, u)
				}
			}
		case line != "" && !strings.HasPrefix(line, "#"):
			if u, err := from.Parse(line); err == nil {
				out = append(out, u)
			}
			return out
		}
	}
	return out
}
