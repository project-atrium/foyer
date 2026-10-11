package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"foyer/internal/library"
	"foyer/internal/sessions"
)

// The 4K copy of Some Film: what these tests stream.
const film4K = "movies/Some.Film.2022.2160p.UHD.BluRay.x265.HDR-GRP/Some.Film.2022.2160p.UHD.BluRay.x265.HDR-GRP.mkv"

// callFrom is a call from a device that says which it is, as a served
// adapter's calls do.
func (h *harness) callFrom(device, method, path, token string, body any) answer {
	h.t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		h.t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", token)
	request.Header.Set("X-Foyer-Device", device)
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	result := answer{status: recorder.Code}
	_ = json.Unmarshal(recorder.Body.Bytes(), &result.body)
	return result
}

// playing is a stream a player holds open, through a real connection.
type playing struct {
	t      *testing.T
	body   io.ReadCloser
	mu     sync.Mutex
	read   int
	closed chan struct{}
}

// open starts reading an address as a player does, until it ends or is
// closed.
func open(t *testing.T, server *httptest.Server, address string) *playing {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+address, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("User-Agent", "libmpv/0.41")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("streaming: %d", response.StatusCode)
	}
	p := &playing{t: t, body: response.Body, closed: make(chan struct{})}
	go func() {
		defer close(p.closed)
		buf := make([]byte, 32<<10)
		for {
			n, err := response.Body.Read(buf)
			p.mu.Lock()
			p.read += n
			p.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = response.Body.Close() })
	return p
}

// flowing waits until bytes have come.
func (p *playing) flowing() {
	p.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		read := p.read
		p.mu.Unlock()
		if read > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.t.Fatal("no bytes came")
}

// ended says whether the stream ended within a while.
func (p *playing) ended(within time.Duration) bool {
	select {
	case <-p.closed:
		return true
	case <-time.After(within):
		return false
	}
}

func (p *playing) leave() {
	_ = p.body.Close()
	<-p.closed
}

// waitFor polls until cond holds, for what a stream's end does on its own.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (s *scanned) liveActivity() []map[string]any {
	s.t.Helper()
	got := s.call(http.MethodGet, "/api/foyer/admin/sessions", s.admin, nil)
	if got.status != http.StatusOK {
		s.t.Fatalf("sessions: %d %v", got.status, got.body)
	}
	var list []map[string]any
	for _, v := range got.body["sessions"].([]any) {
		list = append(list, v.(map[string]any))
	}
	return list
}

func (s *scanned) history() []map[string]any {
	s.t.Helper()
	got := s.call(http.MethodGet, "/api/foyer/admin/sessions/history", s.admin, nil)
	if got.status != http.StatusOK {
		s.t.Fatalf("history: %d %v", got.status, got.body)
	}
	var list []map[string]any
	for _, v := range got.body["items"].([]any) {
		list = append(list, v.(map[string]any))
	}
	return list
}

func (s *scanned) progress(u user, session, kind string, position int64, film string) {
	s.t.Helper()
	got := s.call(http.MethodPost, "/api/foyer/watch/progress", u.token, map[string]any{
		"collection": "movies", "id": film, "kind": kind, "positionMs": position, "durationMs": 7_200_000, "session": session,
	})
	if got.status != http.StatusNoContent {
		s.t.Fatalf("report: %d %v", got.status, got.body)
	}
}

// A playing is on Live Activity — who, where, what, by which engine — and
// a superuser's Stop ends its stream at once and refuses its address from
// then on, across a restart too.
func TestASuperuserStopsAPlaying(t *testing.T) {
	s := scanLibrary(t)
	s.zurg.Endless(film4K)
	alex := s.makeUser("alex", map[string]any{"name": "Alex"})
	film := s.title("movies", "Some Film")
	server := httptest.NewServer(s.mux)
	t.Cleanup(server.Close)

	device := deviceHeader(t, "device-hash-1", "Alex’s phone")
	play := s.callFrom(device, http.MethodPost, "/api/foyer/play", alex.token, map[string]any{"collection": "movies", "id": film.Id})
	if play.status != http.StatusOK {
		t.Fatalf("play: %d %v", play.status, play.body)
	}
	address, id := play.body["url"].(string), play.body["session"].(string)
	stream := open(t, server, address)
	stream.flowing()

	list := s.liveActivity()
	if len(list) != 1 {
		t.Fatalf("live activity %v", list)
	}
	now := list[0]
	item, user, on := now["item"].(map[string]any), now["user"].(map[string]any), now["device"].(map[string]any)
	if now["id"] != id || user["name"] != "Alex" || on["name"] != "Alex’s phone" || on["platform"] != "android" ||
		item["title"] != "Some Film" || !strings.HasPrefix(item["poster"].(string), "/api/files/movies/"+film.Id+"/") ||
		now["kind"] != "play" || now["mode"] != "direct" || now["client"] != "libmpv/0.41" || now["streams"] != 1.0 {
		t.Fatalf("the playing %v", now)
	}
	// A user sees none of it.
	if got := s.call(http.MethodGet, "/api/foyer/admin/sessions", alex.token, nil); got.status == http.StatusOK {
		t.Errorf("a user read Live Activity: %d", got.status)
	}

	if got := s.call(http.MethodPost, "/api/foyer/admin/sessions/"+id+"/stop", s.admin, nil); got.status != http.StatusNoContent {
		t.Fatalf("stop: %d %v", got.status, got.body)
	}
	if !stream.ended(5 * time.Second) {
		t.Fatal("the stream went on after Stop")
	}
	got := s.call(http.MethodGet, address, "", nil)
	if got.status != http.StatusForbidden || !strings.Contains(got.body["message"].(string), "stopped") {
		t.Errorf("the address after Stop: %d %v", got.status, got.body)
	}
	if list := s.liveActivity(); len(list) != 0 {
		t.Errorf("still on Live Activity: %v", list)
	}
	if got := s.call(http.MethodPost, "/api/foyer/admin/sessions/"+id+"/stop", s.admin, nil); got.status != http.StatusNotFound {
		t.Errorf("stopping it twice: %d", got.status)
	}
	history := s.history()
	if len(history) != 1 || history[0]["session"] != id || history[0]["revoked"] != true ||
		history[0]["user"].(map[string]any)["name"] != "Alex" || history[0]["device"].(map[string]any)["name"] != "Alex’s phone" ||
		history[0]["item"].(map[string]any)["title"] != "Some Film" || history[0]["client"] != "libmpv/0.41" {
		t.Fatalf("history %v", history)
	}

	// After a restart, what was stopped stays stopped.
	restarted := sessions.New(s.app, library.FilesCollection)
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, address, nil)
	if _, _, ok := restarted.Open(request, id, alex.id, play.body["version"].(map[string]any)["file"].(string), time.Now().Add(time.Hour)); ok {
		t.Error("a restart let a stopped playing go on")
	}
	// A new playing of the same film plays.
	again := s.call(http.MethodPost, "/api/foyer/play", alex.token, map[string]any{"collection": "movies", "id": film.Id})
	open(t, server, again.body["url"].(string)).flowing()
}

// A player's stop ends its playing once nothing of it streams — never its
// addresses — and a stop sent under a playing's id while its stream runs,
// an older playing's sent late, waits for the stream to end. A playing long
// quiet is let go and comes back with its next request, as it was.
func TestAPlayerStopsItsOwnPlaying(t *testing.T) {
	s := scanLibrary(t)
	s.zurg.Endless(film4K)
	alex := s.makeUser("alex", map[string]any{"name": "Alex"})
	film := s.title("movies", "Some Film")
	server := httptest.NewServer(s.mux)
	t.Cleanup(server.Close)
	device := deviceHeader(t, "device-hash-1", "Alex’s phone")
	first := s.callFrom(device, http.MethodPost, "/api/foyer/play", alex.token, map[string]any{"collection": "movies", "id": film.Id})
	second := s.callFrom(device, http.MethodPost, "/api/foyer/play", alex.token, map[string]any{"collection": "movies", "id": film.Id})
	a, b := first.body["session"].(string), second.body["session"].(string)
	if a == b {
		t.Fatal("two playings under one id")
	}
	stream := open(t, server, second.body["url"].(string))
	stream.flowing()

	// The first, with nothing open, ends at its stop; the second plays on.
	s.progress(alex, a, "stopped", 60_000, film.Id)
	ids := func() []string {
		var out []string
		for _, v := range s.liveActivity() {
			out = append(out, v["id"].(string))
		}
		return out
	}
	if got := ids(); len(got) != 1 || got[0] != b {
		t.Fatalf("after the first's stop: %v", got)
	}
	// A stop under the second's id while its stream runs waits; a report
	// after it says the player goes on.
	s.progress(alex, b, "stopped", 30_000, film.Id)
	s.progress(alex, b, "progress", 40_000, film.Id)
	if stream.ended(200 * time.Millisecond) {
		t.Fatal("a player's stop ended its stream")
	}
	stream.leave()
	if got := ids(); len(got) != 1 || got[0] != b || s.liveActivity()[0]["positionMs"] != 40_000.0 {
		t.Fatalf("a playing heard from after a stop: %v", s.liveActivity())
	}
	// A stop with its stream still open ends it as the stream ends.
	stream = open(t, server, second.body["url"].(string))
	stream.flowing()
	s.progress(alex, b, "stopped", 50_000, film.Id)
	if len(ids()) != 1 {
		t.Fatal("ended while its stream was open")
	}
	stream.leave()
	waitFor(t, "the playing ended with its stream", func() bool { return len(ids()) == 0 })

	// Its address still opens — a player that carries on is let on — and
	// the playing is back, as it was.
	stream = open(t, server, second.body["url"].(string))
	stream.flowing()
	back := s.liveActivity()
	if len(back) != 1 || back[0]["id"] != b || back[0]["device"].(map[string]any)["name"] != "Alex’s phone" || back[0]["positionMs"] != 50_000.0 {
		t.Fatalf("back: %v", back)
	}
	stream.leave()
	waitFor(t, "its stream closed", func() bool {
		list := s.liveActivity()
		return len(list) == 1 && list[0]["streams"] == 0.0
	})

	// Quiet with nothing open, past GoneAfter: let go, its history kept once.
	clock := time.Now()
	s.server.Playings.SetClock(func() time.Time { return clock })
	s.server.Playings.Sweep()
	if len(ids()) != 1 {
		t.Fatal("let go before it was long quiet")
	}
	clock = clock.Add(sessions.GoneAfter + time.Minute)
	s.server.Playings.Sweep()
	if len(ids()) != 0 {
		t.Fatal("a playing long quiet was kept")
	}
	rows := 0
	for _, h := range s.history() {
		if h["session"] == b {
			rows++
			if h["revoked"] != false {
				t.Errorf("a quiet playing was refused: %v", h)
			}
		}
	}
	if rows != 1 {
		t.Errorf("%d rows of history for one playing", rows)
	}
}

// AirPlay, picture in picture, a phone asleep: a playing whose player
// reports nothing is kept while its bytes flow.
func TestAPlayingWithoutReportsPlaysOn(t *testing.T) {
	s := scanLibrary(t)
	s.zurg.Endless(film4K)
	alex := s.makeUser("alex", nil)
	film := s.title("movies", "Some Film")
	server := httptest.NewServer(s.mux)
	t.Cleanup(server.Close)
	play := s.call(http.MethodPost, "/api/foyer/play", alex.token, map[string]any{"collection": "movies", "id": film.Id})
	stream := open(t, server, play.body["url"].(string))
	stream.flowing()
	clock := time.Now().Add(sessions.GoneAfter + time.Hour)
	s.server.Playings.SetClock(func() time.Time { return clock })
	s.server.Playings.Sweep()
	list := s.liveActivity()
	if len(list) != 1 || stream.ended(100*time.Millisecond) {
		t.Fatalf("a playing that streams was let go: %v", list)
	}
	waitFor(t, "its bytes counted", func() bool {
		list := s.liveActivity()
		return len(list) == 1 && list[0]["bytes"].(float64) > 0 && list[0]["quiet"] == false && list[0]["bitrate"].(float64) > 0
	})
}

// A download is a playing too, with no player to report: it ends once its
// bytes stop.
func TestADownloadIsOnLiveActivityWhileItsBytesFlow(t *testing.T) {
	s := scanLibrary(t)
	alex := s.makeUser("alex", nil)
	film := s.title("movies", "Some Film")
	got := s.call(http.MethodPost, "/api/foyer/download", alex.token, map[string]any{"collection": "movies", "id": film.Id})
	if status, _, body := s.fetchRange(got.body["url"].(string), ""); status != http.StatusOK || body != "2160p" {
		t.Fatalf("download: %d %q", status, body)
	}
	list := s.liveActivity()
	if len(list) != 1 || list[0]["kind"] != "download" || list[0]["bytes"] != 5.0 {
		t.Fatalf("live activity %v", list)
	}
	clock := time.Now().Add(3 * time.Minute)
	s.server.Playings.SetClock(func() time.Time { return clock })
	s.server.Playings.Sweep()
	if list := s.liveActivity(); len(list) != 0 {
		t.Fatalf("a finished download stayed: %v", list)
	}
	if history := s.history(); len(history) != 1 || history[0]["kind"] != "download" || history[0]["bytes"] != 5.0 {
		t.Fatalf("history %v", history)
	}
}
