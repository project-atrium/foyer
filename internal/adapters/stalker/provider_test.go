package stalker

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"foyer/internal/adapters/stalker/stalkertest"
)

type providerRig struct {
	provider *Provider
	fake     *stalkertest.Portal
	clock    *skipClock
	start    time.Time
}

func provide(t *testing.T, o stalkertest.Options, zone string, asSent bool) providerRig {
	t.Helper()
	clock := newSkipClock()
	start := clock.Now()
	if o.GuideStart.IsZero() {
		o.GuideStart = start
	}
	fake := stalkertest.New(o)
	t.Cleanup(fake.Close)
	p := NewProvider(ProviderOptions{Options: Options{URL: fake.Address(), Identity: Identity{MAC: mac}, Clock: clock}, Zone: zone, AsSent: asSent})
	return providerRig{provider: p, fake: fake, clock: clock, start: start}
}

func (r providerRig) programmes(t *testing.T, channels []GuideChannel, from, to time.Time) []ChannelProgrammes {
	t.Helper()
	got, err := r.provider.Programmes(context.Background(), channels, from, to)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func plain(ids ...string) []GuideChannel {
	var out []GuideChannel
	for _, id := range ids {
		out = append(out, GuideChannel{ID: id})
	}
	return out
}

type shown struct {
	id, title string
	start     time.Time
}

func flat(got []ChannelProgrammes) []shown {
	var out []shown
	for _, c := range got {
		for _, p := range c.Programmes {
			out = append(out, shown{c.ID, p.Title, p.Start})
		}
	}
	return out
}

func TestReadsTheGroupsAndEveryChannelInOneAnswerInTheirOrder(t *testing.T) {
	r := provide(t, stalkertest.Options{Countries: true}, "", false)
	l, err := r.provider.Lineup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got [][3]string
	for _, c := range l.Channels {
		got = append(got, [3]string{c.ID, c.Country, c.Cmd})
	}
	want := [][3]string{{"534", "DE", "ffmpeg http://localhost/ch/534_"}, {"12", "TR", "ffmpeg http://localhost/ch/12_"}, {"24", "TR", "ffmpeg http://localhost/ch/24_"}}
	if !l.Whole || len(l.Groups) != 2 || !slices.Equal(got, want) {
		t.Fatalf("%+v", l)
	}
	if actions := r.fake.Actions(); !slices.Equal(actions, []string{"handshake", "get_profile", "get_genres", "get_all_channels"}) {
		t.Errorf("asked %v", actions)
	}
	// Every command the plain form for its id: a link can be made from one.
	if got := r.provider.Portal.LinkFor("12"); got != "ffmpeg http://localhost/ch/12_" {
		t.Errorf("link %q", got)
	}
}

func TestReadsEveryChannelOnNoCallersContextOnceForEveryone(t *testing.T) {
	r := provide(t, stalkertest.Options{Hold: "get_all_channels"}, "", false)
	leaving, leave := context.WithCancel(context.Background())
	left := make(chan error, 1)
	go func() {
		_, err := r.provider.Lineup(leaving)
		left <- err
	}()
	staying := make(chan Lineup, 1)
	go func() {
		l, err := r.provider.Lineup(context.Background())
		if err != nil {
			t.Error(err)
		}
		staying <- l
	}()
	for r.fake.Held() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	leave()
	if err := <-left; !errors.Is(err, context.Canceled) {
		t.Fatalf("left with %v", err)
	}
	r.fake.Release()
	if l := <-staying; len(l.Channels) != 3 || !l.Whole {
		t.Fatalf("%+v", l)
	}
	if n := len(r.fake.Asked("get_all_channels")); n != 1 {
		t.Errorf("read %d times", n)
	}
}

func TestPagesThePortalWhereEveryChannelComesShortOfItsOwnCount(t *testing.T) {
	for _, o := range []stalkertest.Options{{EveryChannelShort: true}, {EveryChannelFails: true}} {
		r := provide(t, o, "", false)
		l, err := r.provider.Lineup(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(l.Channels) != 3 || !l.Whole || l.Channels[2].Name != "Late Show" {
			t.Fatalf("%+v", l)
		}
		pages := r.fake.Asked("get_ordered_list")
		if len(pages) != 2 || pages[0].Query["genre"] != "*" || pages[1].Query["p"] != "2" {
			t.Errorf("paged %v", pages)
		}
		if got := r.provider.Portal.LinkFor("101"); got != "" {
			t.Errorf("learned a form from commands that differ: %q", got)
		}
	}
}

func TestAnswersOneChannelsNowAndNextFromTheWholePortalsGuideAsItDoesADay(t *testing.T) {
	r := provide(t, stalkertest.Options{}, "", false)
	now := r.start
	nowAndNext := flat(r.programmes(t, plain("101"), now, now.Add(time.Hour)))
	if len(nowAndNext) != 1 || nowAndNext[0].title != "Show at 1000" || !nowAndNext[0].start.Equal(now) {
		t.Fatalf("%v", nowAndNext)
	}
	day := r.programmes(t, plain("101", "102"), now, now.Add(24*time.Hour))
	var periods []string
	for _, asked := range r.fake.Asked("get_epg_info") {
		periods = append(periods, asked.Query["period"])
	}
	// An hour beyond each window, so the same answer serves the hour after it too.
	if !slices.Equal(periods, []string{"2", "25"}) {
		t.Errorf("periods %v", periods)
	}
	if got := flat(day); len(got) != 3 {
		t.Fatalf("%v", got)
	}
	first := day[0].Programmes[0]
	if first.Description != "About it." || !first.End.Equal(now.Add(30*time.Minute)) {
		t.Errorf("%+v", first)
	}
	if asked := r.fake.Requests(); slices.ContainsFunc(asked, func(q stalkertest.Request) bool { return q.Query["action"] == "get_short_epg" }) {
		t.Error("asked for a short guide")
	}
}

func TestKeepsTheWholeGuideForEveryListWhileItCoversWhatTheyAskForAnHour(t *testing.T) {
	r := provide(t, stalkertest.Options{}, "", false)
	now := r.start
	seven := plain("101", "102", "103", "104", "105", "106", "107")
	guides := func() int { return len(r.fake.Asked("get_epg_info")) }
	r.programmes(t, seven, now, now.Add(time.Hour))
	// Another group's channels, a minute later: the same answer serves.
	r.clock.advance(time.Minute)
	if got := flat(r.programmes(t, append(plain("101"), seven[2:]...), now, now.Add(time.Hour))); len(got) == 0 || got[0].id != "101" {
		t.Errorf("%v", got)
	}
	if guides() != 1 {
		t.Fatalf("%d guides", guides())
	}
	// Five minutes on, the window has moved and the same answer still covers it.
	r.clock.advance(5 * time.Minute)
	r.programmes(t, seven, now.Add(6*time.Minute), now.Add(66*time.Minute))
	if guides() != 1 {
		t.Fatalf("%d guides", guides())
	}
	// A window past what it covers asks again.
	r.programmes(t, seven, now, now.Add(4*time.Hour))
	if guides() != 2 {
		t.Fatalf("%d guides", guides())
	}
	// And so does an hour on, whatever it covers.
	r.clock.advance(time.Hour)
	r.programmes(t, seven, now.Add(66*time.Minute), now.Add(126*time.Minute))
	if guides() != 3 {
		t.Fatalf("%d guides", guides())
	}
}

func TestReadsTheZoneFromTheConnectionAndAsksForTheHoursItShiftsBy(t *testing.T) {
	r := provide(t, stalkertest.Options{}, "Europe/Berlin", false)
	now := r.start
	// The fake clock is in January 1970, when Berlin was an hour ahead.
	day := flat(r.programmes(t, plain("101", "102"), now, now.Add(24*time.Hour)))
	if asked := r.fake.Asked("get_epg_info"); len(asked) != 1 || asked[0].Query["period"] != "26" {
		t.Errorf("asked %v", asked)
	}
	if len(day) != 1 || day[0].id != "101" || !day[0].start.Equal(now.Add(time.Hour)) {
		t.Errorf("%v", day)
	}
	// Still told the box is on UTC, so the shift is the whole of the difference.
	if asked := r.fake.Asked("get_epg_info"); !strings.Contains(asked[0].Header.Get("Cookie"), "timezone=UTC") {
		t.Error("told another zone")
	}
}

func TestPutsEachChannelsGuideRightByItsCountryUnlessToldToTakeItAsSent(t *testing.T) {
	from, to := at("2026-10-04T00:00:00Z"), at("2026-10-05T00:00:00Z")
	asked := []GuideChannel{{ID: "12", Country: "TR"}, {ID: "534", Country: "DE"}, {ID: "24", Country: "TR"}}
	r := provide(t, stalkertest.Options{Countries: true}, "", false)
	got := flat(r.programmes(t, asked, from, to))
	want := []shown{
		{"12", "Show Ana Haber", at("2026-10-04T15:25:00Z")},
		{"534", "Tagesschau", at("2026-10-04T18:00:00Z")},
		// No guide id, but a Turkish group: 18:00 in Istanbul.
		{"24", "Hafta Sonu Haberleri", at("2026-10-04T15:00:00Z")},
	}
	if !slices.EqualFunc(got, want, func(a, b shown) bool { return a.id == b.id && a.title == b.title && a.start.Equal(b.start) }) {
		t.Errorf("%v", got)
	}
	// A favourite's guide, read by the country the app carries, reads no list.
	if n := len(r.fake.Asked("get_all_channels")) + len(r.fake.Asked("get_genres")); n != 0 {
		t.Errorf("read %d lists", n)
	}
	asSent := provide(t, stalkertest.Options{Countries: true}, "", true)
	if got := flat(asSent.programmes(t, asked[:1], from, to)); len(got) != 1 || !got[0].start.Equal(at("2026-10-04T13:25:00Z")) {
		t.Errorf("as sent %v", got)
	}
	// A channel whose country is not known: as the portal says.
	if got := flat(r.programmes(t, plain("12"), from, to)); !got[0].start.Equal(at("2026-10-04T13:25:00Z")) {
		t.Errorf("no country %v", got)
	}
}

func TestAsksAWholeGuideThatFailedAgainOnlyAfterAWhileButOneThePortalStoppedOnceItMay(t *testing.T) {
	var reply atomic.Pointer[stalkertest.Reply]
	reply.Store(&stalkertest.Reply{Status: http.StatusInternalServerError})
	r := provide(t, stalkertest.Options{Throttle: func(typ, action string) *stalkertest.Reply {
		if action == "get_epg_info" {
			return reply.Load()
		}
		return nil
	}}, "", false)
	now := r.start
	ask := func() error {
		_, err := r.provider.Programmes(context.Background(), plain("101"), now, now.Add(time.Hour))
		return err
	}
	wantError(t, ask(), "PROVIDER_UNAVAILABLE", "backoff", "")
	wantError(t, ask(), "PROVIDER_UNAVAILABLE", "backoff", "")
	if n := len(r.fake.Asked("get_epg_info")); n != 1 {
		t.Fatalf("asked %d times", n)
	}
	r.clock.advance(GuideCoolDown)
	reply.Store(stalkertest.TooMany(nil))
	wantError(t, ask(), "PROVIDER_UNAVAILABLE", "", "too-many-attempts")
	reply.Store(nil)
	r.clock.advance(time.Minute)
	if err := ask(); err != nil {
		t.Fatal(err)
	}
	if n := len(r.fake.Asked("get_epg_info")); n != 3 {
		t.Errorf("asked %d times", n)
	}
}

func TestStartsAChannelBeforeAnythingElseStoppingAGuideDownloadUntilItPlays(t *testing.T) {
	r := provide(t, stalkertest.Options{Hold: "get_epg_info"}, "", false)
	if err := r.provider.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := r.start
	guide := make(chan []ChannelProgrammes, 1)
	go func() { guide <- r.programmes(t, plain("101"), now, now.Add(time.Hour)) }()
	for r.fake.Held() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	source, err := r.provider.Link(context.Background(), "itv", "ffmpeg http://localhost/ch/101_", "", true)
	if err != nil || !strings.Contains(source.URI, "stream.test") || source.Protocol != "mpegts" {
		t.Fatalf("%+v %v", source, err)
	}
	// The guide is asked again once the channel has its link, and its list
	// gets it as though nothing happened.
	for r.fake.Held() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	r.fake.Release()
	if got := flat(<-guide); len(got) == 0 {
		t.Error("no guide")
	}
	actions := r.fake.Actions()
	if from := slices.Index(actions, "get_epg_info"); !slices.Equal(actions[from:], []string{"get_epg_info", "create_link", "get_epg_info"}) {
		t.Errorf("asked %v", actions)
	}
}

func TestMakesALinkWithWhatAMagBoxSendsAndSaysWhyThereIsNone(t *testing.T) {
	var reply sync.Map
	r := provide(t, stalkertest.Options{Link: func(q stalkertest.Request) stalkertest.Reply {
		if v, ok := reply.Load("next"); ok {
			return v.(stalkertest.Reply)
		}
		return stalkertest.Reply{Status: http.StatusOK, JSON: map[string]any{"js": map[string]any{"cmd": "ffmpeg http://stream.test/series/611.mkv"}}}
	}}, "", false)
	ctx := context.Background()
	source, err := r.provider.Link(ctx, "vod", "L3Nlcmllcy82MTEubWt2", "1", false)
	if err != nil || source != (Source{URI: "http://stream.test/series/611.mkv", Protocol: "progressive", Container: "mkv"}) {
		t.Fatalf("%+v %v", source, err)
	}
	asked := r.fake.Asked("create_link")[0]
	if q := asked.Query; q["type"] != "vod" || q["cmd"] != "L3Nlcmllcy82MTEubWt2" || q["series"] != "1" || q["forced_storage"] != "undefined" {
		t.Errorf("%v", q)
	}
	for answer, code := range map[string]string{"limit": "PROVIDER_UNAVAILABLE", "nothing_to_play": "NOT_FOUND", "busy": "PROVIDER_UNAVAILABLE"} {
		reply.Store("next", stalkertest.Reply{Status: http.StatusOK, JSON: map[string]any{"js": map[string]any{"cmd": "", "error": answer}}})
		_, err := r.provider.Link(ctx, "itv", "ffmpeg http://localhost/ch/101_", "", true)
		wantError(t, err, code, "", "")
		if e, _ := ErrorOf(err); (answer == "limit") != (e.Reason == "limit") {
			t.Errorf("%s: reason %q", answer, e.Reason)
		}
	}
	reply.Store("next", stalkertest.Reply{Status: http.StatusOK, JSON: map[string]any{"js": map[string]any{"cmd": "ffmpeg"}}})
	_, err = r.provider.Link(ctx, "itv", "ffmpeg http://localhost/ch/101_", "", true)
	wantError(t, err, "PROVIDER_UNAVAILABLE", "backoff", "")
}

func TestReadsAWholeGuideAProgrammeAtATimeAndAnythingElseAsNothing(t *testing.T) {
	for body, want := range map[string]int{
		`{"js":{"data":{"1":[{"name":"A","start_timestamp":1,"stop_timestamp":2},7,null],"2":"none"},"other":[1,[2]]},"text":"x"}`: 1,
		`{"js":{"data":[]}}`: 0,
		`{"js":null}`:        0,
	} {
		channels, err := ReadGuide([]byte(body))
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		n := 0
		for _, each := range channels {
			n += len(each)
		}
		if n != want {
			t.Errorf("%s: %d", body, n)
		}
	}
	for _, body := range []string{`[]`, `{"nothing":1}`, `not json`, `{"js":{"data":{"1":[{"name":`} {
		if _, err := ReadGuide([]byte(body)); err == nil {
			t.Errorf("%s read", body)
		}
	}
}
