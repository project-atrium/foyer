package stalker

import (
	"context"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"
)

const (
	// Keep is how long the whole portal's guide is kept, then asked for
	// again.
	Keep = time.Hour
	// LongestGuide is the most hours of guide asked for at once.
	LongestGuide = 48
	// GuideCoolDown is how long a whole guide that failed is not asked for
	// again: each time it is the portal's whole guide.
	GuideCoolDown = 2 * time.Minute
	// mostPages is the furthest the channels are paged through where every
	// channel in one answer came short.
	mostPages = 2000
)

// ProviderOptions are a portal's, and how its guide is read.
type ProviderOptions struct {
	Options
	// Zone is the zone the portal keeps its guide in: "" for as it says.
	Zone string
	// AsSent takes every guide as the portal says, never putting another
	// country's right by it.
	AsSent bool
}

// Provider is a portal's live TV as Foyer reads it: the groups and every
// channel in one answer, the whole guide kept while it covers what is asked,
// and a link made as a channel starts — all of it through one Portal, and so
// one gate.
type Provider struct {
	Portal *Portal
	zone   string
	asSent bool
	clock  Clock

	mu      sync.Mutex
	lineup  *flight[Lineup]
	guide   *guideRead
	failed  error
	failedA time.Time
}

type flight[T any] struct {
	done chan struct{}
	val  T
	err  error
}

func (f *flight[T]) wait(ctx context.Context) (T, error) {
	select {
	case <-f.done:
		return f.val, f.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

type guideRead struct {
	flight[*Guide]
	at      time.Time
	period  int
	settled bool
}

// NewProvider is the portal o says, read as o says.
func NewProvider(o ProviderOptions) *Provider {
	portal := New(o.Options)
	return &Provider{Portal: portal, zone: o.Zone, asSent: o.AsSent, clock: portal.o.Clock}
}

// Check finds the portal, signs in and reads the profile.
func (p *Provider) Check(ctx context.Context) error { return p.Portal.Check(ctx) }

// Lineup is the portal's groups and channels, read together.
type Lineup struct {
	Groups []Group
	// Channels are in the portal's channel order: by number, those without
	// one last, each as it came.
	Channels []Channel
	// Whole says every channel the portal says it has is here: only then may
	// one it lacks be taken for gone.
	Whole bool
}

// Lineup reads the groups, then every channel in one answer — where that
// comes short of its own count, the portal's pages of them instead. Once,
// however many ask, and on no caller's context: one giving up fails nobody
// else.
func (p *Provider) Lineup(ctx context.Context) (Lineup, error) {
	p.mu.Lock()
	f := p.lineup
	if f == nil {
		f = &flight[Lineup]{done: make(chan struct{})}
		p.lineup = f
		go func() {
			f.val, f.err = p.readLineup(context.Background())
			p.mu.Lock()
			p.lineup = nil
			p.mu.Unlock()
			close(f.done)
		}()
	}
	p.mu.Unlock()
	return f.wait(ctx)
}

func (p *Provider) readLineup(ctx context.Context) (Lineup, error) {
	var l Lineup
	titles := map[string]string{}
	// A portal without groups still has its channels.
	if js, err := p.Portal.Call(ctx, "itv", "get_genres", nil, CallOptions{}); err == nil {
		l.Groups = Groups(js)
		for _, g := range l.Groups {
			titles[g.ID] = g.Name
		}
	} else if refusedOrPaused(err) {
		return Lineup{}, err
	}
	titleOf := func(id string) string { return titles[id] }
	js, err := p.Portal.Call(ctx, "itv", "get_all_channels", nil, CallOptions{Timeout: WholePortal})
	if err != nil && refusedOrPaused(err) {
		return Lineup{}, err
	}
	if err == nil {
		page := Channels(js, 1, p.Portal.Root(), titleOf)
		if len(page.Rows) > 0 && (!page.HasTotal || len(page.Rows) >= page.Total) {
			l.Channels, l.Whole = page.Rows, true
		}
	}
	if !l.Whole {
		if l.Channels, l.Whole, err = p.pages(ctx, titleOf); err != nil {
			return Lineup{}, err
		}
	}
	slices.SortStableFunc(l.Channels, func(a, b Channel) int {
		order := func(c Channel) float64 {
			if c.HasNumber {
				return c.Number
			}
			return math.Inf(1)
		}
		switch x, y := order(a), order(b); {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	})
	// Whether every command is the plain form for its channel's id: then a
	// link is made from an id alone.
	if l.Whole {
		commands := make([]LinkCommand, 0, len(l.Channels))
		for _, c := range l.Channels {
			commands = append(commands, LinkCommand{ID: c.ID, Cmd: c.Cmd})
		}
		p.Portal.LearnLinks(commands)
	}
	return l, nil
}

// pages reads every channel as the portal pages them, where one answer of
// them all came short.
func (p *Provider) pages(ctx context.Context, titleOf func(string) string) ([]Channel, bool, error) {
	var out []Channel
	seen := map[string]bool{}
	for at := 1; at <= mostPages; at++ {
		js, err := p.Portal.Call(ctx, "itv", "get_ordered_list", Params{{"genre", "*"}, {"fav", "0"}, {"sortby", "number"}, {"hd", "0"}, {"force_ch_link_check", ""}, {"p", strconv.Itoa(at)}}, CallOptions{})
		if err != nil {
			return nil, false, err
		}
		page := Channels(js, at, p.Portal.Root(), titleOf)
		for _, c := range page.Rows {
			if !seen[c.ID] {
				seen[c.ID] = true
				out = append(out, c)
			}
		}
		if !page.More() {
			return out, len(out) > 0 && (!page.HasTotal || len(out) >= page.Total), nil
		}
	}
	return out, false, nil
}

func refusedOrPaused(err error) bool {
	e, ok := ErrorOf(err)
	return ok && (e.Code == "UNAUTHORIZED" || e.Reason == "too-many-attempts")
}

// coolsDown is a failure worth waiting out: the portal was slow or broken —
// not asking to be left alone, which says how long for itself.
func coolsDown(err error) bool {
	e, ok := ErrorOf(err)
	return ok && e.Retry == "backoff" && e.Reason != "too-many-attempts"
}

// GuideChannel is a channel whose guide is asked for: the portal's id, and
// where it is from, where that is known.
type GuideChannel struct {
	ID, Country string
}

// ChannelProgrammes are one channel's programmes.
type ChannelProgrammes struct {
	ID         string
	Programmes []Programme
}

// Programmes are the programmes of channels between from and to, each read
// by its own country, from the whole portal's guide: even one channel's now
// and next comes from it. `get_short_epg` picks its programmes by the box's
// clock — UTC, as Foyer says — against the portal's own wall clock: a portal
// in Berlin answered at 02:49 with what aired from 00:30 to 02:45, and not
// what was on.
func (p *Provider) Programmes(ctx context.Context, channels []GuideChannel, from, to time.Time) ([]ChannelProgrammes, error) {
	if len(channels) == 0 {
		return nil, nil
	}
	// The portal counts its hours from its own clock: a guide shifted by its
	// zone needs that many more to reach the end of the window.
	shift := 0
	if p.zone != "" {
		if offset, ok := ZoneOffset(p.zone, p.clock.Now()); ok {
			shift = int(math.Ceil(math.Abs(offset.Hours())))
		}
	}
	guide, err := p.Guide(ctx, func(at time.Time) int {
		return max(1, int(math.Ceil(to.Sub(at).Hours()))+shift)
	})
	if err != nil {
		return nil, err
	}
	out := make([]ChannelProgrammes, 0, len(channels))
	for _, c := range channels {
		each := ChannelProgrammes{ID: c.ID}
		for _, programme := range guide.Programmes(c.ID, p.readingOf(c.Country)) {
			if programme.End.After(from) && programme.Start.Before(to) {
				each.Programmes = append(each.Programmes, programme)
			}
		}
		out = append(out, each)
	}
	return out, nil
}

// readingOf is how a channel's guide is read: the connection's zone, and the
// zone of the channel's own country. One whose country is not known keeps
// the connection's reading.
func (p *Provider) readingOf(country string) Reading {
	r := Reading{Zone: p.zone}
	if !p.asSent && country != "" {
		r.CountryZone = CountryZone(country)
	}
	return r
}

// Guide is the whole portal's guide, kept for an hour while it covers the
// window asked about, however many ask meanwhile. hoursFrom says how many
// hours of the portal's reach the end of the window from a moment. Last of
// the queue, behind every channel and page; on no caller's context. One that
// failed is not asked for again for a while; one that the portal stopped by
// asking to be left alone is, once it may be.
func (p *Provider) Guide(ctx context.Context, hoursFrom func(at time.Time) int) (*Guide, error) {
	for {
		p.mu.Lock()
		now := p.clock.Now()
		if p.failed != nil && now.Sub(p.failedA) < GuideCoolDown {
			err := p.failed
			p.mu.Unlock()
			return nil, err
		}
		held := p.guide
		covers := held != nil && now.Sub(held.at) < Keep && min(LongestGuide, hoursFrom(held.at)) <= held.period
		if held != nil && !covers && !held.settled {
			// One download of the whole guide at a time: a longer window waits
			// for the one under way, then asks for what is still missing.
			p.mu.Unlock()
			if _, err := held.wait(ctx); err != nil && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if covers {
			p.mu.Unlock()
			return held.wait(ctx)
		}
		// An hour beyond the window, so the same answer serves the hour after
		// it too.
		read := &guideRead{flight: flight[*Guide]{done: make(chan struct{})}, at: now, period: min(LongestGuide, hoursFrom(now)+1)}
		p.guide = read
		p.mu.Unlock()
		go p.readGuide(read)
		return read.wait(ctx)
	}
}

func (p *Provider) readGuide(read *guideRead) {
	body, err := p.Portal.CallRaw(context.Background(), "itv", "get_epg_info", Params{{"period", strconv.Itoa(read.period)}}, CallOptions{Timeout: WholePortal, Lane: LaneGuide})
	var channels map[string][]entry
	if err == nil {
		channels, err = ReadGuide(body)
	}
	p.mu.Lock()
	read.settled = true
	if err != nil {
		if p.guide == read {
			p.guide = nil
		}
		if coolsDown(err) {
			p.failed, p.failedA = err, p.clock.Now()
		}
		read.err = err
	} else {
		p.failed = nil
		read.val = &Guide{At: read.at, Period: read.period, channels: channels}
	}
	p.mu.Unlock()
	close(read.done)
}

// Link is a stream to play, made by `create_link`: a live channel's from its
// `cmd`, a film's from its own, an episode's from its series' or season's
// with its number. A channel starting goes before anything waiting, and
// stops a guide download in its way.
func (p *Provider) Link(ctx context.Context, typ, cmd, series string, live bool) (Source, error) {
	js, err := p.Portal.Call(ctx, typ, "create_link", Params{
		{"series", series}, {"forced_storage", "undefined"}, {"disable_ad", "0"}, {"download", "0"}, {"force_ch_link_check", "0"}, {"cmd", cmd},
	}, CallOptions{Lane: LanePlay})
	if err != nil {
		return Source{}, err
	}
	link := LinkOf(js)
	switch {
	case link.Error == "limit":
		return Source{}, &Error{Code: "PROVIDER_UNAVAILABLE", Message: "Too many devices are watching on this subscription right now.", Retry: "backoff", Reason: "limit", answer: true}
	case link.Error == "nothing_to_play":
		return Source{}, &Error{Code: "NOT_FOUND", Message: "The portal has nothing to play for this.", answer: true}
	case link.Error != "":
		return Source{}, &Error{Code: "PROVIDER_UNAVAILABLE", Message: "The portal could not make a link to play.", Retry: "backoff", answer: true}
	case link.URL == "":
		return Source{}, Unreadable()
	}
	return SourceFor(link.URL, live), nil
}
