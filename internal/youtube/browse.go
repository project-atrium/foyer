package youtube

import (
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// Identity is how a video's watch status is kept: by the site's own id.
func Identity(id string) string { return Key + ":" + id }

var (
	validPlaylist = regexp.MustCompile(`^[0-9A-Za-z_-]{10,64}$`)
	tokenText     = regexp.MustCompile(`^[0-9A-Za-z_%=-]+$`)
)

// validToken is a page token as the site hands one out.
func validToken(token string) bool {
	return len(token) >= 8 && len(token) <= 4096 && tokenText.MatchString(token)
}

// scopes are what a search is narrowed by, as the site's own filter.
var scopes = map[string]string{"all": "", "video": "EgIQAQ==", "channel": "EgIQAg==", "playlist": "EgIQAw=="}

// Described is what a video is, as Foyer last saw it listed or played.
type Described struct {
	Title, Channel, ChannelID, Thumbnail string
}

// seen remembers what each video listed or played is, so a report of it
// later keeps its title, channel and picture with its watch status.
type seen struct {
	mu   sync.Mutex
	what map[string]Described
}

const seenMost = 20000

func (s *seen) remember(videos ...*Video) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.what == nil || len(s.what) > seenMost {
		s.what = map[string]Described{}
	}
	for _, v := range videos {
		if v == nil || v.Title == "" {
			continue
		}
		d := Described{Title: v.Title, Thumbnail: v.Thumbnail}
		if v.Channel != nil {
			d.Channel, d.ChannelID = v.Channel.Name, v.Channel.ID
		}
		s.what[v.ID] = d
	}
}

// Describe is what a video is, where Foyer has seen it listed or played.
func (y *YouTube) Describe(id string) (Described, bool) {
	y.seen.mu.Lock()
	d, ok := y.seen.what[id]
	y.seen.mu.Unlock()
	return d, ok
}

// BrowseRoutes mounts the site's videos as a user's device reads them, each
// with the user's own watch status.
func (y *YouTube) BrowseRoutes(r *router.Router[*core.RequestEvent]) {
	r.GET("/api/foyer/videos/search", y.search).Bind(apis.RequireAuth("users"))
	r.GET("/api/foyer/videos/v/{id}", y.video).Bind(apis.RequireAuth("users"))
	r.GET("/api/foyer/videos/channels/{id}", y.channel).Bind(apis.RequireAuth("users"))
	r.GET("/api/foyer/videos/channels/{id}/{tab}", y.channelTab).Bind(apis.RequireAuth("users"))
	r.GET("/api/foyer/videos/playlists/{id}", y.playlist).Bind(apis.RequireAuth("users"))
	r.POST("/api/foyer/videos/feed", y.feed).Bind(apis.RequireAuth("users"))
	r.GET("/api/foyer/videos/resume", y.resume).Bind(apis.RequireAuth("users"))
}

// asked answers a failure of the site's as a device should hear it: never a
// 401, which is the user's own session's alone.
func asked(e *core.RequestEvent, err error) error {
	if errors.Is(err, ErrOff) {
		return e.NotFoundError("YouTube is switched off on this Foyer.", nil)
	}
	if e.Request.Context().Err() != nil {
		return nil
	}
	return e.Error(http.StatusBadGateway, "The site did not answer as it should.", nil)
}

// page is an answer's results for a device: what each video is remembered,
// and the user's watch status laid over each.
type page struct {
	Items      []Result `json:"items"`
	Next       string   `json:"next,omitempty"`
	Incomplete bool     `json:"incomplete,omitzero"`
}

func (y *YouTube) finish(e *core.RequestEvent, results []Result) []Result {
	var videos []*Video
	for _, r := range results {
		if r.Video != nil {
			videos = append(videos, r.Video)
		}
	}
	y.seen.remember(videos...)
	y.watched(e.App, e.Auth.Id, videos)
	if results == nil {
		return []Result{}
	}
	return results
}

// watched lays the user's watch status over each video they began.
func (y *YouTube) watched(app core.App, user string, videos []*Video) {
	if len(videos) == 0 {
		return
	}
	identities := make([]any, 0, len(videos))
	for _, v := range videos {
		identities = append(identities, Identity(v.ID))
	}
	states, err := app.FindAllRecords("watch_states", dbx.HashExp{"user": user}, dbx.In("identity", identities...))
	if err != nil {
		return
	}
	byIdentity := map[string]*core.Record{}
	for _, s := range states {
		byIdentity[s.GetString("identity")] = s
	}
	for _, v := range videos {
		if s, ok := byIdentity[Identity(v.ID)]; ok {
			v.Watch = watchOf(s)
		}
	}
}

func watchOf(s *core.Record) *Watch {
	w := &Watch{Watched: s.GetBool("watched"), PositionMs: int64(s.GetInt("position_ms")), DurationMs: int64(s.GetInt("duration_ms"))}
	if at := s.GetDateTime("last_played_at"); !at.IsZero() {
		w.LastPlayedAt = at.Time().UTC().Format(time.RFC3339)
	}
	return w
}

func (y *YouTube) available() bool {
	_, ok := y.Runner.tool()
	return ok
}

func (y *YouTube) search(e *core.RequestEvent) error {
	if !y.available() {
		return asked(e, ErrOff)
	}
	q := e.Request.URL.Query()
	scope := q.Get("scope")
	if scope == "" {
		scope = "all"
	}
	params, ok := scopes[scope]
	if !ok {
		return e.BadRequestError("A search is narrowed to all, video, channel or playlist.", nil)
	}
	body := map[string]any{}
	if cursor := q.Get("cursor"); cursor != "" {
		if !validToken(cursor) {
			return e.BadRequestError("That is no page of a search.", nil)
		}
		body["continuation"] = cursor
	} else {
		term := q.Get("q")
		if term == "" || len(term) > 200 {
			return e.BadRequestError("A search needs words, two hundred letters at most.", nil)
		}
		body["query"] = term
		if params != "" {
			body["params"] = params
		}
	}
	answer, err := y.Tube.call(e.Request.Context(), "search", body)
	if err != nil {
		return asked(e, err)
	}
	results, next := collect(answer, time.Now())
	// What the search was narrowed to, and never a short: they have their
	// channel's own tab.
	results = slices.DeleteFunc(results, func(r Result) bool {
		return (r.Video != nil && r.Video.Short) || (scope != "all" && r.Kind != scope)
	})
	return e.JSON(http.StatusOK, page{Items: y.finish(e, results), Next: next})
}

func (y *YouTube) video(e *core.RequestEvent) error {
	if !y.available() {
		return asked(e, ErrOff)
	}
	id := e.Request.PathValue("id")
	if !ValidID.MatchString(id) {
		return e.NotFoundError("", nil)
	}
	answer, err := y.Tube.call(e.Request.Context(), "next", map[string]any{"videoId": id})
	if err != nil {
		return asked(e, err)
	}
	video, related := videoPage(answer, id, time.Now())
	if video.Title == "" {
		return e.NotFoundError("This video is no longer on the site, or is private.", nil)
	}
	// Its length is what yt-dlp said, where Foyer has that at hand.
	if info, ok := y.Runner.peek(id); ok && !info.Live() && info.Duration > 0 {
		video.DurationMs = int64(info.Duration * 1000)
	}
	all := []Result{{Kind: "video", Video: video}}
	for i := range related {
		all = append(all, Result{Kind: "video", Video: &related[i]})
	}
	y.finish(e, all)
	return e.JSON(http.StatusOK, map[string]any{"video": video, "related": related})
}

// tabsOf are the params each of a channel's tabs is asked by, as its page
// last said: kept an hour.
func (y *YouTube) tabsOf(channel string) (map[string]string, bool) {
	y.mu.Lock()
	defer y.mu.Unlock()
	t, ok := y.tabs[channel]
	if !ok || time.Now().After(t.until) {
		return nil, false
	}
	return t.params, true
}

func (y *YouTube) keepTabs(channel string, params map[string]string) {
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.tabs == nil || len(y.tabs) > 2000 {
		y.tabs = map[string]channelTabs{}
	}
	y.tabs[channel] = channelTabs{params: params, until: time.Now().Add(time.Hour)}
}

func (y *YouTube) channel(e *core.RequestEvent) error {
	if !y.available() {
		return asked(e, ErrOff)
	}
	id := e.Request.PathValue("id")
	if !ValidChannel.MatchString(id) {
		return e.NotFoundError("", nil)
	}
	answer, err := y.Tube.call(e.Request.Context(), "browse", map[string]any{"browseId": id, "params": Tabs[0].Params})
	if err != nil {
		return asked(e, err)
	}
	channel, params := channelPage(answer)
	if channel.Name == "" {
		return e.NotFoundError("This channel is no longer on the site.", nil)
	}
	channel.ID = id
	y.keepTabs(id, params)
	tabs := []string{}
	for _, t := range Tabs {
		if _, ok := params[t.Key]; ok {
			tabs = append(tabs, t.Key)
		}
	}
	return e.JSON(http.StatusOK, map[string]any{"channel": channel, "tabs": tabs})
}

func (y *YouTube) channelTab(e *core.RequestEvent) error {
	if !y.available() {
		return asked(e, ErrOff)
	}
	id, tab := e.Request.PathValue("id"), e.Request.PathValue("tab")
	if !ValidChannel.MatchString(id) {
		return e.NotFoundError("", nil)
	}
	body := map[string]any{}
	if cursor := e.Request.URL.Query().Get("cursor"); cursor != "" {
		if !validToken(cursor) {
			return e.BadRequestError("That is no page of a channel.", nil)
		}
		body["continuation"] = cursor
	} else {
		params, known := "", false
		for _, t := range Tabs {
			if t.Key == tab {
				params, known = t.Params, true
			}
		}
		if !known {
			return e.NotFoundError("A channel's tabs are videos, shorts, live and playlists.", nil)
		}
		if kept, ok := y.tabsOf(id); ok && kept[tab] != "" {
			params = kept[tab]
		}
		body["browseId"], body["params"] = id, params
	}
	answer, err := y.Tube.call(e.Request.Context(), "browse", body)
	if err != nil {
		return asked(e, err)
	}
	results, next := collect(answer, time.Now())
	return e.JSON(http.StatusOK, page{Items: y.finish(e, results), Next: next})
}

func (y *YouTube) playlist(e *core.RequestEvent) error {
	if !y.available() {
		return asked(e, ErrOff)
	}
	id := e.Request.PathValue("id")
	if !validPlaylist.MatchString(id) {
		return e.NotFoundError("", nil)
	}
	body := map[string]any{"browseId": "VL" + id}
	if cursor := e.Request.URL.Query().Get("cursor"); cursor != "" {
		if !validToken(cursor) {
			return e.BadRequestError("That is no page of a playlist.", nil)
		}
		body = map[string]any{"continuation": cursor}
	}
	answer, err := y.Tube.call(e.Request.Context(), "browse", body)
	if err != nil {
		return asked(e, err)
	}
	results, next := collect(answer, time.Now())
	results = slices.DeleteFunc(results, func(r Result) bool { return r.Kind != "video" })
	about := playlistPage(answer, id)
	if about.Title == "" && body["continuation"] == nil {
		return e.NotFoundError("This playlist is no longer on the site, or is private.", nil)
	}
	return e.JSON(http.StatusOK, map[string]any{"playlist": about, "items": y.finish(e, results), "next": next})
}

// feedWait is how long the feed waits for channels it has not read yet:
// what is at hand comes now, the rest when asked again.
const feedWait = 6 * time.Second

func (y *YouTube) feed(e *core.RequestEvent) error {
	if !y.available() {
		return asked(e, ErrOff)
	}
	var body struct {
		Channels []string `json:"channels"`
		Cursor   string   `json:"cursor"`
		Limit    int      `json:"limit"`
	}
	if err := e.BindBody(&body); err != nil {
		return e.BadRequestError("The request could not be read.", err)
	}
	channels := slices.DeleteFunc(slices.Clone(body.Channels), func(id string) bool { return !ValidChannel.MatchString(id) })
	slices.Sort(channels)
	channels = slices.Compact(channels)
	if len(channels) > 500 {
		channels = channels[:500]
	}
	limit := body.Limit
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	start, _ := strconv.Atoi(body.Cursor)
	videos, complete := y.Feeds.Newest(e.Request.Context(), channels, feedWait)
	videos = slices.DeleteFunc(videos, func(v Video) bool { return v.Short })
	answer := page{Items: []Result{}, Incomplete: !complete}
	for i := max(start, 0); i < len(videos) && len(answer.Items) < limit; i++ {
		// A card says nothing of what a video is about: its page does.
		video := videos[i]
		video.Description = ""
		answer.Items = append(answer.Items, Result{Kind: "video", Video: &video})
	}
	if end := max(start, 0) + len(answer.Items); end < len(videos) {
		answer.Next = strconv.Itoa(end)
	}
	answer.Items = y.finish(e, answer.Items)
	return e.JSON(http.StatusOK, answer)
}

func (y *YouTube) resume(e *core.RequestEvent) error {
	limit, err := strconv.Atoi(e.Request.URL.Query().Get("limit"))
	if err != nil || limit <= 0 || limit > 100 {
		limit = 20
	}
	states, err := e.App.FindRecordsByFilter("watch_states",
		"user = {:user} && item_collection = {:videos} && watched = false && position_ms >= 30000", "-last_played_at", limit, 0,
		dbx.Params{"user": e.Auth.Id, "videos": Key})
	if err != nil {
		return e.InternalServerError("", err)
	}
	items := []Video{}
	for _, s := range states {
		v := Video{ID: s.GetString("item_id"), Title: s.GetString("title"), Thumbnail: s.GetString("picture"), DurationMs: int64(s.GetInt("duration_ms")), Watch: watchOf(s)}
		if !ValidID.MatchString(v.ID) {
			continue
		}
		if v.Title == "" {
			// Never described: what Foyer has at hand, else the video's id.
			if d, ok := y.Describe(v.ID); ok {
				v.Title, v.Thumbnail = d.Title, d.Thumbnail
			} else {
				v.Title = v.ID
			}
		}
		if v.Thumbnail == "" {
			v.Thumbnail = "https://i.ytimg.com/vi/" + v.ID + "/hqdefault.jpg"
		}
		if name, channel := s.GetString("subtitle"), s.GetString("subtitle_id"); name != "" && ValidChannel.MatchString(channel) {
			v.Channel = &Owner{ID: channel, Name: name}
		}
		items = append(items, v)
	}
	return e.JSON(http.StatusOK, map[string]any{"items": items})
}
