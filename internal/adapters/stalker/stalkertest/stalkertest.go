// Package stalkertest is a pretend Ministra portal at /stalker_portal/,
// answering by `type` and `action` in the shapes a real one sends, trimmed to
// what is read: the app's own fake portal, ported with its answers.
package stalkertest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Load is where the pretend portal's API is.
const Load = "/stalker_portal/server/load.php"

// Request is one request the portal was sent.
type Request struct {
	Path   string
	Query  map[string]string
	Header http.Header
}

// Reply is an answer in place of the portal's own.
type Reply struct {
	Status int
	JSON   any
	Text   string
	Header map[string]string
}

// Options say how the portal behaves.
type Options struct {
	// Profile is laid over the profile it answers: `status: 1` refuses.
	Profile map[string]any
	// ExpireOnce refuses the first call after a sign-in; ExpireAlways every
	// one, the profile's included.
	ExpireOnce, ExpireAlways bool
	// Link answers create_link.
	Link func(Request) Reply
	// SeriesSection keeps the series apart from the films, as newer portals do.
	SeriesSection bool
	// FailCategories is how many times its series categories fail, as a busy
	// portal's do, before they answer.
	FailCategories int
	// Stolen is how many calls find the session ended — the same MAC address
	// signing in elsewhere — whatever the token.
	Stolen int
	// Vod answers its films' pages, where a test needs pages of its own.
	Vod func(page int) any
	// Countries gives it Turkish and German channels, with guide ids and a
	// guide on a German portal's clock.
	Countries bool
	// EveryChannelFails fails its every-channel list; EveryChannelShort gives
	// fewer channels than it says it has.
	EveryChannelFails, EveryChannelShort bool
	// Throttle answers in the portal's place — a 429, say — for the requests
	// it picks.
	Throttle func(typ, action string) *Reply
	// Hold keeps answers to this action until Release, or until the request
	// is given up.
	Hold string
	// GuideStart is when its guide starts: when it started, where none is said.
	GuideStart time.Time
}

// Portal is the pretend portal, on its own server.
type Portal struct {
	*httptest.Server
	o Options

	mu               sync.Mutex
	requests         []Request
	handshakes       int
	expired          bool
	categoriesFailed int
	stolen           int
	held             []chan struct{}
	inFlight, most   int
}

// New starts a pretend portal. Close it when done.
func New(o Options) *Portal {
	if o.GuideStart.IsZero() {
		o.GuideStart = time.Now()
	}
	p := &Portal{o: o}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	return p
}

// Address is the address a provider hands out for it.
func (p *Portal) Address() string { return p.URL + "/c/" }

// Endpoint is where its API answers.
func (p *Portal) Endpoint() string { return p.URL + Load }

// Handshakes is how many times it shook hands.
func (p *Portal) Handshakes() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.handshakes
}

// Requests are what it was sent, in order.
func (p *Portal) Requests() []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Request(nil), p.requests...)
}

// Actions are the actions it was sent to its API, in order.
func (p *Portal) Actions() []string {
	var out []string
	for _, r := range p.Requests() {
		if r.Path == Load {
			out = append(out, r.Query["action"])
		}
	}
	return out
}

// Asked are the requests to its API for one action.
func (p *Portal) Asked(action string) []Request {
	var out []Request
	for _, r := range p.Requests() {
		if r.Path == Load && r.Query["action"] == action {
			out = append(out, r)
		}
	}
	return out
}

// Held is how many answers wait to be released.
func (p *Portal) Held() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.held)
}

// Release lets every held answer go.
func (p *Portal) Release() {
	p.mu.Lock()
	held := p.held
	p.held = nil
	p.mu.Unlock()
	for _, each := range held {
		close(each)
	}
}

// Most is the most requests it was answering at once.
func (p *Portal) Most() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.most
}

func (p *Portal) serve(w http.ResponseWriter, r *http.Request) {
	query := map[string]string{}
	for key, values := range r.URL.Query() {
		query[key] = values[0]
	}
	request := Request{Path: r.URL.Path, Query: query, Header: r.Header.Clone()}
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.inFlight++
	p.most = max(p.most, p.inFlight)
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.inFlight--
		p.mu.Unlock()
	}()
	if r.URL.Path != Load {
		http.NotFound(w, r)
		return
	}
	if p.o.Hold != "" && query["action"] == p.o.Hold {
		release := make(chan struct{})
		p.mu.Lock()
		p.held = append(p.held, release)
		p.mu.Unlock()
		select {
		case <-release:
		case <-r.Context().Done():
			// Given up: no longer held.
			p.mu.Lock()
			if i := slices.Index(p.held, release); i >= 0 {
				p.held = slices.Delete(p.held, i, i+1)
			}
			p.mu.Unlock()
			return
		}
	}
	reply := p.route(request)
	for key, value := range reply.Header {
		w.Header().Set(key, value)
	}
	body := reply.Text
	if reply.Text == "" {
		b, _ := json.Marshal(reply.JSON)
		if reply.JSON == nil {
			b = []byte("{}")
		}
		body = string(b)
	}
	w.WriteHeader(reply.Status)
	_, _ = w.Write([]byte(body))
}

func ok(v any) Reply { return Reply{Status: http.StatusOK, JSON: v} }

var failed = Reply{Status: http.StatusOK, Text: "Authorization failed."}

func (p *Portal) route(r Request) Reply {
	typ, action := r.Query["type"], r.Query["action"]
	if p.o.Throttle != nil {
		if reply := p.o.Throttle(typ, action); reply != nil {
			return *reply
		}
	}
	p.mu.Lock()
	if typ == "stb" && action == "handshake" {
		p.handshakes++
		token := fmt.Sprintf("token-%d", p.handshakes)
		p.mu.Unlock()
		return ok(map[string]any{"js": map[string]any{"token": token}})
	}
	current := fmt.Sprintf("Bearer token-%d", p.handshakes)
	if r.Header.Get("Authorization") != current {
		p.mu.Unlock()
		return failed
	}
	if typ != "stb" && p.stolen < p.o.Stolen {
		p.stolen++
		p.mu.Unlock()
		return failed
	}
	if p.o.ExpireAlways || p.o.ExpireOnce && !p.expired && typ != "stb" {
		p.expired = true
		p.mu.Unlock()
		return failed
	}
	p.mu.Unlock()
	switch {
	case typ == "stb" && action == "get_profile":
		profile := map[string]any{"id": "9", "name": "box", "status": 0}
		for key, value := range p.o.Profile {
			profile[key] = value
		}
		return ok(map[string]any{"js": profile})
	case typ == "itv" && action == "get_genres":
		if p.o.Countries {
			return ok(CountryGenres)
		}
		return ok(Genres)
	case typ == "itv" && action == "get_ordered_list":
		if p.o.Countries {
			return ok(map[string]any{"js": map[string]any{"total_items": 3, "max_page_items": 14, "cur_page": 1, "data": CountryChannels}})
		}
		page, _ := strconv.Atoi(r.Query["p"])
		return ok(ChannelPage(page))
	case typ == "itv" && action == "get_all_channels":
		switch {
		case p.o.EveryChannelFails:
			return Reply{Status: http.StatusServiceUnavailable}
		case p.o.EveryChannelShort:
			return ok(map[string]any{"js": map[string]any{"total_items": 5, "data": channelRows(1)}})
		case p.o.Countries:
			return ok(map[string]any{"js": map[string]any{"total_items": 3, "data": CountryChannels}})
		}
		return ok(map[string]any{"js": map[string]any{"total_items": 3, "data": append(channelRows(1), channelRows(2)...)}})
	case typ == "itv" && action == "get_epg_info":
		if p.o.Countries {
			return ok(map[string]any{"js": map[string]any{"data": CountryGuide}})
		}
		start := p.o.GuideStart.Unix()
		return ok(map[string]any{"js": map[string]any{"data": map[string]any{
			"101": []any{EPG("101", start), EPG("101", start+7200)},
			"102": []any{EPG("102", start)},
		}}})
	case action == "create_link":
		if p.o.Link != nil {
			return p.o.Link(r)
		}
		return ok(map[string]any{"js": map[string]any{"id": "1", "cmd": "ffmpeg http://stream.test/live/abc123/101.ts?play_token=t0k3n", "error": ""}})
	case typ == "series":
		if !p.o.SeriesSection {
			return Reply{Status: http.StatusNotFound}
		}
		switch action {
		case "get_categories":
			p.mu.Lock()
			fail := p.categoriesFailed < p.o.FailCategories
			if fail {
				p.categoriesFailed++
			}
			p.mu.Unlock()
			if fail {
				return Reply{Status: http.StatusServiceUnavailable}
			}
			return ok(SeriesCategories)
		case "get_ordered_list":
			if r.Query["movie_id"] == "18390:18390" {
				return ok(SeriesSeasons)
			}
			return ok(SeriesPage)
		}
	case typ == "vod" && action == "get_ordered_list":
		switch r.Query["movie_id"] {
		case "601":
			if r.Query["season_id"] == "0" {
				return ok(Seasons)
			}
			return ok(Episodes)
		case "701":
			return ok(map[string]any{"js": map[string]any{"total_items": 0, "data": []any{}}})
		}
		if p.o.Vod != nil {
			page, _ := strconv.Atoi(r.Query["p"])
			return ok(p.o.Vod(page))
		}
		return ok(VodPage)
	}
	return Reply{Status: http.StatusNotFound}
}

// Answers in the shape a Ministra portal sends them.
var (
	Genres = map[string]any{"js": []any{
		map[string]any{"id": "*", "title": "All"},
		map[string]any{"id": "10", "title": "News"},
		map[string]any{"id": "11", "title": "Sports"},
	}}
	VodPage = map[string]any{"js": map[string]any{"total_items": 3, "max_page_items": 14, "cur_page": 1, "data": []any{
		map[string]any{"id": "501", "name": "Harbor Lights", "o_name": "Harbor Lights", "description": "A film.", "year": "2019", "time": "112", "genres_str": "Drama, Thriller", "rating_imdb": "7.4", "screenshot_uri": "http://cdn.test/501.jpg", "cmd": "L21lZGlhLzUwMS5tcGc=", "is_series": "0"},
		map[string]any{"id": "601", "name": "The Relay", "year": "2021", "is_series": "1", "cmd": "L21lZGlhLzYwMS5tcGc="},
		map[string]any{"id": "701", "name": "Old Show", "year": "2009", "is_series": 1, "series": []any{1, 2, 3}, "cmd": "L21lZGlhLzcwMS5tcGc="},
	}}}
	Seasons = map[string]any{"js": map[string]any{"total_items": 2, "max_page_items": 14, "data": []any{
		map[string]any{"id": "61", "name": "Season 1", "season_number": "1"},
		map[string]any{"id": "62", "name": "Season 2", "season_number": "2"},
	}}}
	// A portal that keeps its series apart, with ids of its own that hold a
	// colon, and a season that lists its episodes by number.
	SeriesCategories = map[string]any{"js": []any{map[string]any{"id": "*", "title": "All"}, map[string]any{"id": "270", "title": "Drama"}}}
	SeriesPage       = map[string]any{"js": map[string]any{"total_items": 1, "max_page_items": 14, "cur_page": 1, "data": []any{
		map[string]any{"id": "18390:18390", "name": "Harbour Nights", "year": "2026", "screenshot_uri": "http://cdn.test/18390.jpg"},
	}}}
	SeriesSeasons = map[string]any{"js": map[string]any{"total_items": 1, "max_page_items": 14, "data": []any{
		map[string]any{"id": "18390:1", "name": "Season 1", "series": []any{1, 2, 3}, "cmd": "L3Nlcmllcy8xODM5MC5ta3Y="},
	}}}
	Episodes = map[string]any{"js": map[string]any{"total_items": 2, "max_page_items": 14, "data": []any{
		map[string]any{"id": "611", "name": "Pilot", "series_number": "1", "cmd": "L3Nlcmllcy82MTEubWt2"},
		map[string]any{"id": "612", "name": "Second", "series_number": "2", "cmd": "L3Nlcmllcy82MTIubWt2"},
	}}}
	// A German portal with Turkish channels, as a real one answered: its
	// `time` is its own Berlin wall clock, and its stamps are that clock's
	// instants. Show TV's weekend news airs at 18:25 in Istanbul — 15:25Z —
	// but its guide was written in UTC and taken for Berlin's clock: stamped
	// 13:25Z, `time` 15:25.
	CountryGenres = map[string]any{"js": []any{
		map[string]any{"id": "*", "title": "All"},
		map[string]any{"id": "1", "title": "TR ✨ ULUSAL"},
		map[string]any{"id": "22", "title": "DE ✨ DEUTSCHLAND"},
	}}
	CountryChannels = []any{
		map[string]any{"id": "12", "name": "↺SHOW TV FHD", "number": "2836", "cmd": "ffmpeg http://localhost/ch/12_", "tv_genre_id": "1", "xmltv_id": "showtv.tr"},
		map[string]any{"id": "534", "name": "↺DAS ERSTE FHD", "number": "1", "cmd": "ffmpeg http://localhost/ch/534_", "tv_genre_id": "22", "xmltv_id": "ard.de"},
		map[string]any{"id": "24", "name": "↺KANAL 7 FHD", "number": "2841", "cmd": "ffmpeg http://localhost/ch/24_", "tv_genre_id": "1"},
	}
	CountryGuide = map[string]any{
		"12":  []any{map[string]any{"ch_id": "12", "name": "Show Ana Haber", "start_timestamp": Stamp("2026-10-04T13:25:00Z"), "stop_timestamp": Stamp("2026-10-04T14:45:00Z"), "time": "2026-10-04 15:25:00", "time_to": "2026-10-04 16:45:00"}},
		"534": []any{map[string]any{"ch_id": "534", "name": "Tagesschau", "start_timestamp": Stamp("2026-10-04T18:00:00Z"), "stop_timestamp": Stamp("2026-10-04T18:15:00Z"), "time": "2026-10-04 20:00:00", "time_to": "2026-10-04 20:15:00"}},
		"24":  []any{map[string]any{"ch_id": "24", "name": "Hafta Sonu Haberleri", "start_timestamp": Stamp("2026-10-04T13:00:00Z"), "stop_timestamp": Stamp("2026-10-04T14:00:00Z"), "time": "2026-10-04 15:00:00", "time_to": "2026-10-04 16:00:00"}},
	}
)

// ChannelPage is a page of its channels, two to a page.
func ChannelPage(page int) map[string]any {
	return map[string]any{"js": map[string]any{"total_items": 3, "max_page_items": 2, "cur_page": page, "data": channelRows(page)}}
}

func channelRows(page int) []any {
	if page == 1 {
		return []any{
			map[string]any{"id": "101", "name": "Morning Wire", "number": "1", "cmd": "ffmpeg http://localhost/ch/101_", "tv_genre_id": "10", "logo": "/stalker_portal/misc/logos/320/101.png", "archive": 1, "tv_archive_duration": 168},
			map[string]any{"id": "102", "name": "Stadium One", "number": "2", "cmd": "ffrt http://localhost/ch/102_", "tv_genre_id": "11", "logo": "http://cdn.test/102.png", "archive": 0},
		}
	}
	return []any{map[string]any{"id": "103", "name": "Late Show", "number": "3", "cmd": "ffmpeg http://localhost/ch/103_", "tv_genre_id": "10"}}
}

// EPG is a programme half an hour long.
func EPG(channel string, start int64) map[string]any {
	return map[string]any{"ch_id": channel, "name": fmt.Sprintf("Show at %d", start), "descr": "About it.", "start_timestamp": start, "stop_timestamp": start + 1800}
}

// Stamp is an instant as a portal stamps it, in seconds.
func Stamp(iso string) int64 {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		panic(err)
	}
	return t.Unix()
}

// TooMany is a 429, with what headers it is given.
func TooMany(header map[string]string) *Reply {
	return &Reply{Status: http.StatusTooManyRequests, Text: "Too Many Requests", Header: header}
}

// Values are a request's query as sent, for a test that reads one.
func (r Request) Values() url.Values {
	v := url.Values{}
	for key, value := range r.Query {
		v.Set(key, value)
	}
	return v
}

// Bearer is the token a request carried.
func (r Request) Bearer() string { return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") }
