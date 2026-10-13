package stalker

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"foyer/internal/adapters/stalker/stalkertest"
)

const mac = "00:1A:79:12:34:56"

// skipClock is the app's fake clock: a wait is over the moment it begins,
// and time moves on by it.
type skipClock struct {
	mu  sync.Mutex
	now time.Time
}

func newSkipClock() *skipClock { return &skipClock{now: time.UnixMilli(1_000_000)} }

func (c *skipClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *skipClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	c.now = c.now.Add(max(0, d))
	c.mu.Unlock()
	var stopped atomic.Bool
	go func() {
		if !stopped.Load() {
			f()
		}
	}()
	return func() bool { return !stopped.Swap(true) }
}

func (c *skipClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type rig struct {
	portal *Portal
	fake   *stalkertest.Portal
	clock  *skipClock
	store  *MemoryStore
}

type setup struct {
	identity Identity
	atOnce   int
}

func open(t *testing.T, o stalkertest.Options, s setup) rig {
	t.Helper()
	clock := newSkipClock()
	if o.GuideStart.IsZero() {
		o.GuideStart = clock.Now()
	}
	fake := stalkertest.New(o)
	t.Cleanup(fake.Close)
	store := &MemoryStore{}
	if s.identity.MAC == "" {
		s.identity.MAC = mac
	}
	portal := New(Options{URL: fake.Address(), Identity: s.identity, AtOnce: s.atOnce, Store: store, Clock: clock})
	return rig{portal: portal, fake: fake, clock: clock, store: store}
}

func (r rig) saved(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	if s := r.store.Load(); s != "" {
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func (r rig) groups(ctx context.Context) ([]Group, error) {
	js, err := r.portal.Call(ctx, "itv", "get_genres", nil, CallOptions{})
	if err != nil {
		return nil, err
	}
	return Groups(js), nil
}

func (r rig) page(ctx context.Context, group string) ([]Channel, error) {
	js, err := r.portal.Call(ctx, "itv", "get_ordered_list", Params{{"genre", group}, {"fav", "0"}, {"sortby", "number"}, {"p", "1"}}, CallOptions{})
	if err != nil {
		return nil, err
	}
	return Channels(js, 1, r.portal.Root(), nil).Rows, nil
}

func wantError(t *testing.T, err error, code, retry, reason string) {
	t.Helper()
	e, ok := ErrorOf(err)
	if !ok {
		t.Fatalf("got %v, want %s", err, code)
	}
	if e.Code != code || retry != "" && e.Retry != retry || reason != "" && e.Reason != reason {
		t.Fatalf("got %s %s %s (%s), want %s %s %s", e.Code, e.Retry, e.Reason, e.Message, code, retry, reason)
	}
}

func TestEndpointsForTheAddressesProvidersHandOut(t *testing.T) {
	cases := map[string][]string{
		"portal.test/c/": {"http://portal.test/server/load.php", "http://portal.test/stalker_portal/server/load.php", "http://portal.test/portal.php"},
		"http://portal.test:8080/stalker_portal/c/": {"http://portal.test:8080/stalker_portal/server/load.php", "http://portal.test:8080/stalker_portal/portal.php"},
		"http://portal.test/portal.php?x=1":         {"http://portal.test/portal.php"},
	}
	for address, want := range cases {
		if got := EndpointsFor(address); !slices.Equal(got, want) {
			t.Errorf("%s: %q, want %q", address, got, want)
		}
	}
}

func TestFindsThePortalShakesHandsAsAMagBoxAndReadsTheProfile(t *testing.T) {
	r := open(t, stalkertest.Options{}, setup{identity: Identity{MAC: "00-1a-79-12-34-56", SerialNumber: "SN1"}})
	if err := r.portal.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	asked := r.fake.Requests()
	if len(asked) != 3 || asked[0].Path != "/server/load.php" {
		t.Fatalf("asked %v", asked)
	}
	handshake, profile := asked[1], asked[2]
	if q := handshake.Query; q["type"] != "stb" || q["action"] != "handshake" || q["token"] != "" || q["JsHttpRequest"] != "1-xml" {
		t.Errorf("handshake %v", q)
	}
	h := handshake.Header
	if h.Get("Cookie") != "mac="+mac+"; stb_lang=en; timezone=UTC" || h.Get("X-User-Agent") != "Model: MAG250; Link: WiFi" || h.Get("Referer") != r.fake.URL+"/c/" || !strings.Contains(h.Get("User-Agent"), "MAG200") || h.Get("Authorization") != "" {
		t.Errorf("handshake's headers %v", h)
	}
	if profile.Query["action"] != "get_profile" || profile.Query["sn"] != "SN1" || profile.Header.Get("Authorization") != "Bearer token-1" {
		t.Errorf("profile %v %v", profile.Query, profile.Header)
	}
	// The session keeps where the portal is and its token — never a link.
	if saved := r.saved(t); len(saved) != 2 || saved["endpoint"] != r.fake.Endpoint() || saved["token"] != "token-1" {
		t.Errorf("saved %v", saved)
	}
}

func TestRefusesAnAddressThatIsNoMacAddressAskingNothing(t *testing.T) {
	r := open(t, stalkertest.Options{}, setup{identity: Identity{MAC: "nonsense"}})
	wantError(t, r.portal.Check(context.Background()), "INVALID_STATE", "never", "")
	if asked := r.fake.Requests(); len(asked) != 0 {
		t.Errorf("asked %v", asked)
	}
}

func TestRemembersAPortalThatRefusedTheMacAddressAndNeverAsksItAgain(t *testing.T) {
	r := open(t, stalkertest.Options{Profile: map[string]any{"status": 1}}, setup{})
	wantError(t, r.portal.Check(context.Background()), "UNAUTHORIZED", "never", "")
	asked := len(r.fake.Requests())
	_, err := r.groups(context.Background())
	wantError(t, err, "UNAUTHORIZED", "", "")
	if len(r.fake.Requests()) != asked {
		t.Error("asked again")
	}
	blocked := open(t, stalkertest.Options{Profile: map[string]any{"blocked": "1"}}, setup{})
	if err := blocked.portal.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("blocked: %v", err)
	}
}

func TestShakesHandsOnceMoreWhenATokenRunsOutAndOnlyOnce(t *testing.T) {
	renewed := open(t, stalkertest.Options{ExpireOnce: true}, setup{})
	if err := renewed.portal.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if groups, err := renewed.groups(context.Background()); err != nil || len(groups) != 2 {
		t.Fatalf("%v %v", groups, err)
	}
	if renewed.fake.Handshakes() != 2 {
		t.Errorf("%d handshakes", renewed.fake.Handshakes())
	}

	lost := open(t, stalkertest.Options{ExpireAlways: true, Profile: map[string]any{}}, setup{})
	_, err := lost.groups(context.Background())
	wantError(t, err, "UNAUTHORIZED", "", "")
	handshakes := lost.fake.Handshakes()
	_, err = lost.groups(context.Background())
	wantError(t, err, "UNAUTHORIZED", "", "")
	if lost.fake.Handshakes() != handshakes {
		t.Error("shook hands again")
	}
}

func TestIsLeftAloneUntilItSaidAndAskedNothingMeanwhile(t *testing.T) {
	var throttle atomic.Bool
	throttle.Store(true)
	r := open(t, stalkertest.Options{Throttle: func(typ, action string) *stalkertest.Reply {
		if throttle.Load() && typ == "itv" && action == "get_genres" {
			return stalkertest.TooMany(map[string]string{"Retry-After": "30"})
		}
		return nil
	}}, setup{})
	ctx := context.Background()
	_, err := r.groups(ctx)
	wantError(t, err, "PROVIDER_UNAVAILABLE", "backoff", "too-many-attempts")
	asked := len(r.fake.Requests())
	throttle.Store(false)
	_, err = r.groups(ctx)
	wantError(t, err, "PROVIDER_UNAVAILABLE", "", "too-many-attempts")
	if len(r.fake.Requests()) != asked {
		t.Error("asked meanwhile")
	}
	r.clock.advance(30 * time.Second)
	if groups, err := r.groups(ctx); err != nil || len(groups) != 2 {
		t.Fatalf("%v %v", groups, err)
	}
}

func TestIsLeftAloneAMinuteLongerEachTimeAndAMinuteAgainAfterAnAnswer(t *testing.T) {
	var throttle atomic.Bool
	var throttled atomic.Int32
	throttle.Store(true)
	r := open(t, stalkertest.Options{Throttle: func(typ, action string) *stalkertest.Reply {
		if !throttle.Load() || typ != "itv" || action != "get_ordered_list" {
			return nil
		}
		throttled.Add(1)
		return stalkertest.TooMany(nil)
	}}, setup{})
	ctx := context.Background()
	page := func() error {
		_, err := r.page(ctx, "10")
		return err
	}
	wantError(t, page(), "PROVIDER_UNAVAILABLE", "", "too-many-attempts")
	r.clock.advance(time.Minute)
	wantError(t, page(), "PROVIDER_UNAVAILABLE", "", "too-many-attempts")
	if throttled.Load() != 2 {
		t.Fatalf("throttled %d", throttled.Load())
	}
	// Two minutes this time: after one, still nothing is asked.
	r.clock.advance(time.Minute)
	wantError(t, page(), "PROVIDER_UNAVAILABLE", "", "too-many-attempts")
	if throttled.Load() != 2 {
		t.Fatalf("throttled %d", throttled.Load())
	}
	r.clock.advance(time.Minute)
	throttle.Store(false)
	if err := page(); err != nil {
		t.Fatal(err)
	}
	throttle.Store(true)
	wantError(t, page(), "PROVIDER_UNAVAILABLE", "", "too-many-attempts")
	r.clock.advance(time.Minute)
	throttle.Store(false)
	if err := page(); err != nil {
		t.Fatal(err)
	}
}

func TestStopsASignInWhereAskedForFewerRatherThanLookElsewhere(t *testing.T) {
	r := open(t, stalkertest.Options{Throttle: func(typ, action string) *stalkertest.Reply {
		if typ == "stb" && action == "handshake" {
			return stalkertest.TooMany(nil)
		}
		return nil
	}}, setup{})
	wantError(t, r.portal.Check(context.Background()), "PROVIDER_UNAVAILABLE", "", "too-many-attempts")
	for _, asked := range r.fake.Requests() {
		if strings.HasSuffix(asked.Path, "/portal.php") {
			t.Fatal("looked for the portal elsewhere")
		}
	}
}

func TestReadsARetryAfterInSecondsOrAsADateWithinReason(t *testing.T) {
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	for value, want := range map[string]time.Duration{
		"120":                           2 * time.Minute,
		"Fri, 09 Oct 2026 10:02:00 GMT": 2 * time.Minute,
		"86400":                         time.Hour,
		"99999999999999999999999":       time.Hour,
		"Fri, 09 Oct 2026 09:00:00 GMT": 0,
	} {
		if got, ok := RetryAfter([]string{value}, now); !ok || got != want {
			t.Errorf("%q: %v %v, want %v", value, got, ok, want)
		}
	}
	for _, value := range [][]string{{"soon"}, {""}, {"-5"}, nil} {
		if got, ok := RetryAfter(value, now); ok {
			t.Errorf("%q: %v", value, got)
		}
	}
}

func TestLetsOneRequestOutAtATimeUnlessTheConnectionSaysMore(t *testing.T) {
	for _, atOnce := range []int{1, 2} {
		r := open(t, stalkertest.Options{Hold: "get_ordered_list"}, setup{atOnce: atOnce})
		ctx := context.Background()
		if _, err := r.groups(ctx); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for range 3 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := r.page(ctx, "10"); err != nil {
					t.Error(err)
				}
			}()
		}
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
	release:
		for {
			select {
			case <-done:
				break release
			case <-time.After(20 * time.Millisecond):
				r.fake.Release()
			}
		}
		if r.fake.Most() != atOnce {
			t.Errorf("at once %d: %d", atOnce, r.fake.Most())
		}
	}
}

func TestReadsTheSavedSessionOnceAndEveryCallUsesItsToken(t *testing.T) {
	r := open(t, stalkertest.Options{}, setup{})
	// Saved by an earlier run; the endpoint is the fake's own, known once it runs.
	r.store.Save(`{"endpoint":"` + r.fake.Endpoint() + `","token":"token-0"}`)
	ctx := context.Background()
	var wg sync.WaitGroup
	var groups []Group
	var channels []Channel
	var errs [2]error
	wg.Add(2)
	go func() { defer wg.Done(); groups, errs[0] = r.groups(ctx) }()
	go func() { defer wg.Done(); channels, errs[1] = r.page(ctx, "10") }()
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || len(groups) != 2 || len(channels) != 2 {
		t.Fatalf("%v %v %v", groups, channels, errs)
	}
	if r.fake.Handshakes() != 0 {
		t.Errorf("%d handshakes", r.fake.Handshakes())
	}
	for _, asked := range r.fake.Requests() {
		if asked.Path == stalkertest.Load && asked.Bearer() != "token-0" {
			t.Errorf("%s with %q", asked.Query["action"], asked.Bearer())
		}
	}
}

func TestRenewsATokenThatRanOutOnceForEveryCallThatFoundItSo(t *testing.T) {
	r := open(t, stalkertest.Options{}, setup{})
	r.store.Save(`{"endpoint":"` + r.fake.Endpoint() + `","token":"token-stale"}`)
	ctx := context.Background()
	var wg sync.WaitGroup
	counts := make([]int, 3)
	for i := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i == 1 {
				channels, err := r.page(ctx, "10")
				if err != nil {
					t.Error(err)
				}
				counts[i] = len(channels)
				return
			}
			groups, err := r.groups(ctx)
			if err != nil {
				t.Error(err)
			}
			counts[i] = len(groups)
		}()
	}
	wg.Wait()
	if !slices.Equal(counts, []int{2, 2, 2}) {
		t.Errorf("answered %v", counts)
	}
	if r.fake.Handshakes() != 1 {
		t.Errorf("%d handshakes", r.fake.Handshakes())
	}
}

func TestSignsInForEveryoneEvenWhenTheCallThatStartedItIsGivenUp(t *testing.T) {
	r := open(t, stalkertest.Options{Hold: "handshake"}, setup{})
	leaving, leave := context.WithCancel(context.Background())
	left := make(chan error, 1)
	go func() {
		_, err := r.groups(leaving)
		left <- err
	}()
	staying := make(chan []Channel, 1)
	go func() {
		channels, err := r.page(context.Background(), "10")
		if err != nil {
			t.Error(err)
		}
		staying <- channels
	}()
	for r.fake.Held() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	leave()
	if err := <-left; !errors.Is(err, context.Canceled) {
		t.Fatalf("left with %v", err)
	}
	r.fake.Release()
	if channels := <-staying; len(channels) != 2 {
		t.Fatalf("stayed with %v", channels)
	}
	if r.fake.Handshakes() != 1 {
		t.Errorf("%d handshakes", r.fake.Handshakes())
	}
}

func TestSaysTheSessionWasEndedElsewhereAndDoesNotHoldItAgainstTheNextCall(t *testing.T) {
	r := open(t, stalkertest.Options{Stolen: 2}, setup{})
	ctx := context.Background()
	_, err := r.groups(ctx)
	wantError(t, err, "PROVIDER_UNAVAILABLE", "never", "")
	if r.fake.Handshakes() != 2 {
		t.Errorf("%d handshakes", r.fake.Handshakes())
	}
	// Not latched: the next call signs in again, once, and is answered.
	if groups, err := r.groups(ctx); err != nil || len(groups) != 2 {
		t.Fatalf("%v %v", groups, err)
	}
	if r.fake.Handshakes() != 3 {
		t.Errorf("%d handshakes", r.fake.Handshakes())
	}
}

func commandsOf(t *testing.T, r rig) []LinkCommand {
	t.Helper()
	js, err := r.portal.Call(context.Background(), "itv", "get_all_channels", nil, CallOptions{Timeout: WholePortal})
	if err != nil {
		t.Fatal(err)
	}
	var out []LinkCommand
	for _, c := range Channels(js, 1, r.portal.Root(), nil).Rows {
		out = append(out, LinkCommand{ID: c.ID, Cmd: c.Cmd})
	}
	return out
}

func TestLearnsThatEveryCommandIsThePlainFormForItsID(t *testing.T) {
	const form = "ffmpeg http://localhost/ch/{id}_"
	first := open(t, stalkertest.Options{Countries: true}, setup{})
	first.portal.LearnLinks(commandsOf(t, first))
	if saved := first.saved(t); saved["link"] != form || saved["token"] != "token-1" {
		t.Fatalf("saved %v", saved)
	}
	// After a restart, a channel's link is made from its id, with no list read.
	again := open(t, stalkertest.Options{Countries: true}, setup{})
	again.store.Save(`{"endpoint":"` + again.fake.Endpoint() + `","token":"token-0","link":"` + form + `"}`)
	if got := again.portal.LinkFor("12"); got != "ffmpeg http://localhost/ch/12_" {
		t.Errorf("link %q", got)
	}
	if len(again.fake.Requests()) != 0 || again.fake.Handshakes() != 0 {
		t.Error("asked the portal")
	}
}

func TestNeverLearnsAFormFromCommandsThatDiffer(t *testing.T) {
	r := open(t, stalkertest.Options{}, setup{})
	r.portal.LearnLinks(commandsOf(t, r))
	if saved := r.saved(t); saved["link"] != "" {
		t.Errorf("learned %q", saved["link"])
	}
	if got := r.portal.LinkFor("101"); got != "" {
		t.Errorf("link %q", got)
	}
}

func TestKeepsTheTokenBesideAFormItCannotReadAndTheFormAcrossASignIn(t *testing.T) {
	unreadable := open(t, stalkertest.Options{}, setup{})
	unreadable.store.Save(`{"endpoint":"` + unreadable.fake.Endpoint() + `","token":"token-0","link":"rtsp://somewhere"}`)
	if _, err := unreadable.groups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if unreadable.fake.Handshakes() != 0 {
		t.Error("shook hands")
	}
	const form = "ffmpeg http://localhost/ch/{id}_"
	renewed := open(t, stalkertest.Options{}, setup{})
	renewed.store.Save(`{"endpoint":"` + renewed.fake.Endpoint() + `","token":"token-stale","link":"` + form + `"}`)
	if _, err := renewed.groups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if renewed.fake.Handshakes() != 1 {
		t.Errorf("%d handshakes", renewed.fake.Handshakes())
	}
	if saved := renewed.saved(t); saved["token"] != "token-1" || saved["link"] != form {
		t.Errorf("saved %v", saved)
	}
}

func TestSendsAPortalsIDsEncodedAsTheAppEncodesThem(t *testing.T) {
	for in, want := range map[string]string{
		"18390:18390": "18390%3A18390",
		"a b/c?d=é":   "a%20b%2Fc%3Fd%3D%C3%A9",
		"-_.!~*'()":   "-_.!~*'()",
	} {
		if got := EncodeURIComponent(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	r := open(t, stalkertest.Options{SeriesSection: true}, setup{})
	if _, err := r.portal.Call(context.Background(), "series", "get_ordered_list", Params{{"movie_id", "18390:18390"}, {"season_id", "0"}}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	asked := r.fake.Asked("get_ordered_list")
	if len(asked) != 1 || asked[0].Query["movie_id"] != "18390:18390" {
		t.Fatalf("asked %v", asked)
	}
}

func TestReadsLooselyTypedValuesAsJavaScriptDoes(t *testing.T) {
	for in, want := range map[string]string{"12": "12", "1e3": "1000", "1.50": "1.5", "1e21": "1e+21", "1e-7": "1e-7"} {
		n := jsNumber(in)
		if got := jsNumberString(n); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	for in, want := range map[any]float64{"": 0, " 7 ": 7, "0x1A": 26, true: 1} {
		if got := jsNumber(in); got != want {
			t.Errorf("%v: %v, want %v", in, got, want)
		}
	}
	if n := jsNumber("12abc"); !math.IsNaN(n) {
		t.Errorf("12abc: %v", n)
	}
	if _, ok := number("Infinity"); ok {
		t.Error("Infinity is a number")
	}
	if _, ok := number(""); ok {
		t.Error("nothing is a number")
	}
	if text(float64(101)) != "101" || text(true) != "" || text(nil) != "" {
		t.Error("text")
	}
}
