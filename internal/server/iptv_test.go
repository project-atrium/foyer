package server_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"foyer/internal/adapters/stalker"
	"foyer/internal/adapters/stalker/stalkertest"
)

// tsStreams is a portal's stream server: an endless stream for each channel,
// counting who has one open.
type tsStreams struct {
	*httptest.Server
	mu    sync.Mutex
	open  map[string]int
	asked map[string]int
}

func newStreams(t *testing.T) *tsStreams {
	s := &tsStreams{open: map[string]int{}, asked: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		channel := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/live/"), ".ts")
		if r.UserAgent() != stalker.UserAgent {
			http.Error(w, "not a box", http.StatusForbidden)
			return
		}
		s.mu.Lock()
		s.open[channel]++
		s.asked[channel]++
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			s.open[channel]--
			s.mu.Unlock()
		}()
		w.Header().Set("Content-Type", "video/mp2t")
		packet := bytes.Repeat([]byte{0x47}, 188*7)
		for {
			if _, err := w.Write(packet); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *tsStreams) openNow(channel string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open[channel]
}

func (s *tsStreams) askedFor(channel string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asked[channel]
}

type tv struct {
	*harness
	admin   string
	portal  *stalkertest.Portal
	streams *tsStreams
	conn    string
	server  *httptest.Server
}

// startTV is Foyer with a Stalker portal set up, its streams on a server of
// their own.
func startTV(t *testing.T, values map[string]any, o stalkertest.Options) *tv {
	t.Helper()
	h := start(t, options{})
	// Requests are logged, so a test can find what never belongs there.
	h.app.Settings().Logs.MaxDays = 7
	streams := newStreams(t)
	if o.Link == nil {
		o.Link = func(r stalkertest.Request) stalkertest.Reply {
			id := strings.TrimSuffix(strings.TrimPrefix(r.Query["cmd"], "ffmpeg http://localhost/ch/"), "_")
			id = strings.TrimSuffix(strings.TrimPrefix(id, "ffrt http://localhost/ch/"), "_")
			return stalkertest.Reply{Status: http.StatusOK, JSON: map[string]any{"js": map[string]any{"cmd": "ffmpeg " + streams.URL + "/live/" + id + ".ts", "error": ""}}}
		}
	}
	portal := stalkertest.New(o)
	t.Cleanup(portal.Close)
	admin := h.superuser()
	sent := map[string]any{"portalUrl": portal.Address(), "mac": "00:1A:79:12:34:56"}
	for k, v := range values {
		sent[k] = v
	}
	made := h.call(http.MethodPost, "/api/foyer/admin/connections", admin, map[string]any{"adapter": "stalker", "name": "Home portal", "enabled": true, "values": sent})
	if made.status != http.StatusOK {
		t.Fatalf("setting the portal up: %d %v", made.status, made.body)
	}
	h.server.IPTV.Lines.StayAfter = 50 * time.Millisecond
	h.server.IPTV.Lines.FirstOpen = 2 * time.Second
	server := httptest.NewServer(h.mux)
	t.Cleanup(server.Close)
	return &tv{harness: h, admin: admin, portal: portal, streams: streams, conn: made.body["id"].(string), server: server}
}

func (v *tv) path(rest string) string { return "/api/foyer/iptv/" + v.conn + rest }

func (v *tv) play(device string, u user, channel string, extra map[string]any) answer {
	v.t.Helper()
	body := map[string]any{"channel": channel}
	for k, x := range extra {
		body[k] = x
	}
	return v.callFrom(device, http.MethodPost, v.path("/play"), u.token, body)
}

func (v *tv) watch(t *testing.T, played answer) *playing {
	t.Helper()
	if played.status != http.StatusOK {
		t.Fatalf("play: %d %v", played.status, played.body)
	}
	sources := played.body["sources"].([]any)
	address := sources[0].(map[string]any)["url"].(string)
	p := open(t, v.server, address)
	p.flowing()
	return p
}

func TestAPortalsLineupComesThroughFoyerWithoutItsCommands(t *testing.T) {
	v := startTV(t, nil, stalkertest.Options{})
	alex := v.makeUser("alex", nil)
	got := v.call(http.MethodGet, v.path("/lineup"), alex.token, nil)
	if got.status != http.StatusOK {
		t.Fatalf("lineup: %d %v", got.status, got.body)
	}
	var keys []string
	for _, raw := range got.body["channels"].([]any) {
		c := raw.(map[string]any)
		keys = append(keys, c["key"].(string))
		if _, leaked := c["cmd"]; leaked {
			t.Errorf("a command went to a device: %v", c)
		}
	}
	if !slices.Equal(keys, []string{"ch:101", "ch:102", "ch:103"}) || len(got.body["groups"].([]any)) != 2 {
		t.Fatalf("lineup %v", got.body)
	}
	// As the app reads it, key for key.
	fits(t, "lineup", got.body, fixtureAnswer(t, "routes/iptv-lineup.json"))
	first := got.body["channels"].([]any)[0].(map[string]any)
	if first["name"] != "Morning Wire" || first["number"] != float64(1) || first["catchupDays"] != float64(7) || !strings.HasSuffix(first["logo"].(string), "/stalker_portal/misc/logos/320/101.png") {
		t.Errorf("first %v", first)
	}
	// Read once, then from what Foyer keeps — a restart too.
	again := v.call(http.MethodGet, v.path("/lineup"), alex.token, nil)
	if again.status != http.StatusOK || len(v.portal.Asked("get_all_channels")) != 1 {
		t.Fatalf("asked the portal again: %v", v.portal.Actions())
	}
	// Served to every device of the household, by its connection.
	household := v.call(http.MethodGet, "/api/foyer/household", alex.token, nil)
	found := false
	for _, raw := range household.body["served"].([]any) {
		s := raw.(map[string]any)
		found = found || s["type"] == "iptv" && s["key"] == "iptv-"+v.conn && s["name"] == "Home portal"
	}
	if !found {
		t.Errorf("served %v", household.body["served"])
	}
}

func TestTheGuideComesThroughFoyerEachChannelReadByItsCountry(t *testing.T) {
	v := startTV(t, nil, stalkertest.Options{Countries: true})
	alex := v.makeUser("alex", nil)
	if got := v.call(http.MethodGet, v.path("/lineup"), alex.token, nil); got.status != http.StatusOK {
		t.Fatalf("lineup: %d %v", got.status, got.body)
	}
	got := v.call(http.MethodPost, v.path("/guide"), alex.token, map[string]any{
		"from": "2026-10-04T00:00:00Z", "to": "2026-10-05T00:00:00Z", "channels": []string{"ch:12", "ch:534", "ch:24"},
	})
	if got.status != http.StatusOK {
		t.Fatalf("guide: %d %v", got.status, got.body)
	}
	var starts []string
	for _, raw := range got.body["channels"].([]any) {
		c := raw.(map[string]any)
		for _, p := range c["programmes"].([]any) {
			starts = append(starts, c["channel"].(string)+" "+p.(map[string]any)["startsAt"].(string))
		}
	}
	// Show TV's and Kanal 7's guides, written in UTC and taken for a Berlin clock, put right.
	if want := []string{"ch:12 2026-10-04T15:25:00Z", "ch:534 2026-10-04T18:00:00Z", "ch:24 2026-10-04T15:00:00Z"}; !slices.Equal(starts, want) {
		t.Errorf("guide %v", starts)
	}
	fits(t, "guide", got.body, fixtureAnswer(t, "routes/iptv-guide.json"))
	if got := v.call(http.MethodPost, v.path("/guide"), alex.token, map[string]any{"from": "2026-10-04T00:00:00Z", "to": "2026-10-09T00:00:00Z", "channels": []string{"ch:12"}}); got.status != http.StatusBadRequest {
		t.Errorf("five days of guide: %d", got.status)
	}
}

func TestOneChannelOnTwoDevicesIsOneStream(t *testing.T) {
	v := startTV(t, nil, stalkertest.Options{})
	alex, kids := v.makeUser("alex", nil), v.makeUser("kids", nil)
	phone, telly := deviceHeader(t, "alex-phone-0001", "Alex’s phone"), deviceHeader(t, "living-room-tv-0001", "Living room")
	first := v.play(phone, alex, "ch:101", nil)
	second := v.play(telly, kids, "ch:101", nil)
	fits(t, "play", first.body, fixtureAnswer(t, "routes/iptv-play.json"))
	a, b := v.watch(t, first), v.watch(t, second)
	if first.body["line"] != second.body["line"] || v.streams.askedFor("101") != 1 || v.streams.openNow("101") != 1 {
		t.Fatalf("lines %v %v, asked %d", first.body["line"], second.body["line"], v.streams.askedFor("101"))
	}
	if links := v.portal.Asked("create_link"); len(links) != 1 || links[0].Query["type"] != "itv" {
		t.Fatalf("links %v", links)
	}
	activity := v.call(http.MethodGet, "/api/foyer/admin/sessions", v.admin, nil).body["sessions"].([]any)
	if len(activity) != 2 {
		t.Fatalf("live activity %v", activity)
	}
	// One leaving leaves the stream to the other; the last one leaving closes it.
	a.leave()
	time.Sleep(100 * time.Millisecond)
	if b.ended(50*time.Millisecond) || v.streams.openNow("101") != 1 {
		t.Fatal("the stream went with the first viewer")
	}
	b.leave()
	waitFor(t, "the portal's stream closed", func() bool { return v.streams.openNow("101") == 0 })
	noneInTheLog(t, v.harness, "/api/foyer/stream/", v.streams.URL, "localhost/ch/", "00:1A:79")
}

// noneInTheLog waits for PocketBase's log to hold a request made after
// everything else, then finds none of what never belongs in it.
func noneInTheLog(t *testing.T, h *harness, never ...string) {
	t.Helper()
	h.call(http.MethodGet, "/api/foyer/info?control=1", "", nil)
	deadline := time.Now().Add(8 * time.Second)
	for {
		var rows []struct {
			Data string `db:"data"`
		}
		if err := h.app.LogQuery().Select("data").All(&rows); err != nil {
			t.Fatal(err)
		}
		control := false
		for _, r := range rows {
			for _, each := range never {
				if strings.Contains(r.Data, each) {
					t.Fatalf("%q in the log: %s", each, r.Data)
				}
			}
			control = control || strings.Contains(r.Data, "control=1")
		}
		if control {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("PocketBase wrote no log in time to compare against")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestAFullSubscriptionSaysWhoWatchesAndALineIsTakenOver(t *testing.T) {
	v := startTV(t, map[string]any{"streams": 1}, stalkertest.Options{})
	alex, kids := v.makeUser("alex", nil), v.makeUser("kids", nil)
	phone, telly := deviceHeader(t, "alex-phone-0001", "Alex’s phone"), deviceHeader(t, "living-room-tv-0001", "Living room")
	held := v.watch(t, v.play(phone, alex, "ch:101", nil))
	full := v.play(telly, kids, "ch:102", nil)
	if full.status != http.StatusConflict || full.body["reason"] != "line-full" || full.body["canForce"] != false {
		t.Fatalf("full: %d %v", full.status, full.body)
	}
	fits(t, "line full", full.body, fixtureAnswer(t, "routes/iptv-line-full.json"))
	streams := full.body["streams"].([]any)
	stream := streams[0].(map[string]any)
	viewers := stream["viewers"].([]any)
	if len(streams) != 1 || stream["channel"] != "ch:101" || stream["name"] != "Morning Wire" || len(viewers) != 1 ||
		viewers[0].(map[string]any)["user"] != "alex" || viewers[0].(map[string]any)["device"] != "Alex’s phone" {
		t.Fatalf("streams %v", streams)
	}
	took := v.watch(t, v.play(telly, kids, "ch:102", map[string]any{"takeOver": stream["id"]}))
	if !held.ended(2 * time.Second) {
		t.Fatal("the line taken over went on")
	}
	waitFor(t, "channel 101's stream closed", func() bool { return v.streams.openNow("101") == 0 })
	// Asking again, Alex's phone is told who took it — once — then that the line is full.
	again := v.play(phone, alex, "ch:101", nil)
	by, _ := again.body["by"].(map[string]any)
	if again.status != http.StatusConflict || again.body["reason"] != "taken-over" || by["user"] != "kids" || by["device"] != "Living room" {
		t.Fatalf("taken over: %d %v", again.status, again.body)
	}
	fits(t, "taken over", again.body, fixtureAnswer(t, "routes/iptv-taken-over.json"))
	if after := v.play(phone, alex, "ch:101", nil); after.status != http.StatusConflict || after.body["reason"] != "line-full" {
		t.Fatalf("after: %d %v", after.status, after.body)
	}
	// Alex's playing was stopped, not let go: its address is refused.
	history := v.call(http.MethodGet, "/api/foyer/admin/sessions/history", v.admin, nil)
	if !strings.Contains(fmt.Sprint(history.body), "revoked:true") {
		t.Errorf("history %v", history.body)
	}
	took.leave()
}

func TestAForcedLineIsForOnePlayingAZapFromItAsksAgain(t *testing.T) {
	v := startTV(t, map[string]any{"streams": 1, "forced": true}, stalkertest.Options{})
	alex, kids := v.makeUser("alex", nil), v.makeUser("kids", nil)
	phone, telly := deviceHeader(t, "alex-phone-0001", "Alex’s phone"), deviceHeader(t, "living-room-tv-0001", "Living room")
	v.watch(t, v.play(phone, alex, "ch:101", nil))
	if full := v.play(telly, kids, "ch:102", nil); full.status != http.StatusConflict || full.body["canForce"] != true {
		t.Fatalf("full: %d %v", full.status, full.body)
	}
	forced := v.play(telly, kids, "ch:102", map[string]any{"force": true})
	if forced.status != http.StatusOK || forced.body["forced"] != true {
		t.Fatalf("forced: %d %v", forced.status, forced.body)
	}
	v.watch(t, forced)
	if v.streams.openNow("101") != 1 || v.streams.openNow("102") != 1 {
		t.Fatal("two lines are not open")
	}
	// The forced line is the kids' playing's alone: a zap asks again.
	if zap := v.play(telly, kids, "ch:103", nil); zap.status != http.StatusConflict || zap.body["reason"] != "line-full" {
		t.Fatalf("zap: %d %v", zap.status, zap.body)
	}
	waitFor(t, "the forced line closed", func() bool { return v.streams.openNow("102") == 0 })
}

func TestADeviceThatZapsHoldsOneLine(t *testing.T) {
	v := startTV(t, map[string]any{"streams": 1}, stalkertest.Options{})
	alex := v.makeUser("alex", nil)
	phone := deviceHeader(t, "alex-phone-0001", "Alex’s phone")
	first := v.watch(t, v.play(phone, alex, "ch:101", nil))
	second := v.watch(t, v.play(phone, alex, "ch:102", nil))
	if !first.ended(2 * time.Second) {
		t.Fatal("the channel before went on")
	}
	waitFor(t, "channel 101's stream closed", func() bool { return v.streams.openNow("101") == 0 })
	second.leave()
}

func TestAViewerWhoLeavesLetsItsLineGoAndAStopFreesOne(t *testing.T) {
	v := startTV(t, map[string]any{"streams": 1}, stalkertest.Options{})
	alex, kids := v.makeUser("alex", nil), v.makeUser("kids", nil)
	phone, telly := deviceHeader(t, "alex-phone-0001", "Alex’s phone"), deviceHeader(t, "living-room-tv-0001", "Living room")
	v.watch(t, v.play(phone, alex, "ch:101", nil)).leave()
	// A moment for a player that reconnects; then the line is free.
	waitFor(t, "the line let go", func() bool { return v.streams.openNow("101") == 0 })
	watching := v.play(telly, kids, "ch:102", nil)
	p := v.watch(t, watching)
	// A superuser's Stop frees it for someone else.
	if got := v.call(http.MethodPost, "/api/foyer/admin/sessions/"+watching.body["session"].(string)+"/stop", v.admin, nil); got.status != http.StatusOK && got.status != http.StatusNoContent {
		t.Fatalf("stop: %d %v", got.status, got.body)
	}
	if !p.ended(2 * time.Second) {
		t.Fatal("the stopped playing went on")
	}
	if got := v.play(phone, alex, "ch:103", nil); got.status != http.StatusOK {
		t.Fatalf("after a stop: %d %v", got.status, got.body)
	}
}

func TestAPortalThatRefusesFoyersBoxIsNeverA401(t *testing.T) {
	v := startTV(t, nil, stalkertest.Options{Profile: map[string]any{"status": 1}})
	alex := v.makeUser("alex", nil)
	got := v.call(http.MethodGet, v.path("/lineup"), alex.token, nil)
	if got.status != http.StatusConflict || got.body["reason"] != "refused" {
		t.Fatalf("lineup: %d %v", got.status, got.body)
	}
	// Refused once is refused: nothing more is asked of the portal.
	asked := len(v.portal.Requests())
	v.call(http.MethodGet, v.path("/lineup"), alex.token, nil)
	if len(v.portal.Requests()) != asked {
		t.Error("asked the portal again")
	}
	// The dashboard's test says why, and asks once more: someone pressed it.
	tested := v.call(http.MethodPost, "/api/foyer/admin/connections/"+v.conn+"/test", v.admin, map[string]any{})
	if tested.status != http.StatusOK || tested.body["ok"] != false || !strings.Contains(tested.body["message"].(string), "MAC") {
		t.Fatalf("test: %d %v", tested.status, tested.body)
	}
	if v.portal.Handshakes() != 2 {
		t.Errorf("%d handshakes", v.portal.Handshakes())
	}
}
