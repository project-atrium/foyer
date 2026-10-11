package youtube

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Owner is who put a video up: a channel.
type Owner struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Avatar    string `json:"avatar,omitempty"`
	Followers int64  `json:"followers,omitzero"`
}

// Watch is a user's watch status of a video, as Loge reads a title's.
type Watch struct {
	Watched      bool   `json:"watched"`
	PositionMs   int64  `json:"positionMs"`
	DurationMs   int64  `json:"durationMs"`
	LastPlayedAt string `json:"lastPlayedAt,omitempty"`
}

// Video is a video of the site's, as Foyer lists it.
type Video struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Channel     *Owner `json:"channel,omitzero"`
	DurationMs  int64  `json:"durationMs,omitzero"`
	PublishedAt string `json:"publishedAt,omitempty"`
	Views       int64  `json:"views,omitzero"`
	Live        bool   `json:"live,omitzero"`
	Upcoming    bool   `json:"upcoming,omitzero"`
	Short       bool   `json:"short,omitzero"`
	Thumbnail   string `json:"thumbnail,omitempty"`
	Description string `json:"description,omitempty"`
	Watch       *Watch `json:"watch,omitzero"`
}

// Channel is a channel, as its page or a search names it.
type Channel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Handle      string `json:"handle,omitempty"`
	Avatar      string `json:"avatar,omitempty"`
	Banner      string `json:"banner,omitempty"`
	Followers   int64  `json:"followers,omitzero"`
	VideoCount  int64  `json:"videoCount,omitzero"`
	Description string `json:"description,omitempty"`
}

// Playlist is a playlist, as a search, a channel or its own page names it.
type Playlist struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Channel     *Owner `json:"channel,omitzero"`
	VideoCount  int64  `json:"videoCount,omitzero"`
	Thumbnail   string `json:"thumbnail,omitempty"`
	Description string `json:"description,omitempty"`
}

// Result is one answer of a search, a tab or a playlist.
type Result struct {
	Kind     string    `json:"kind"`
	Video    *Video    `json:"video,omitzero"`
	Channel  *Channel  `json:"channel,omitzero"`
	Playlist *Playlist `json:"playlist,omitzero"`
}

// walker reads an answer of the site's, in its order: every video, channel
// and playlist it lists — as the site's older renderers have them, and its
// newer view models — once each, and the token for the page after.
type walker struct {
	now  time.Time
	out  []Result
	next string
	seen map[string]bool
}

func collect(answer any, now time.Time) ([]Result, string) {
	w := &walker{now: now, seen: map[string]bool{}}
	w.visit(answer)
	return w.out, w.next
}

func (w *walker) add(r Result) {
	key := r.Kind + "/"
	switch {
	case r.Video != nil:
		key += r.Video.ID
	case r.Channel != nil:
		key += r.Channel.ID
	case r.Playlist != nil:
		key += r.Playlist.ID
	}
	if !w.seen[key] {
		w.seen[key] = true
		w.out = append(w.out, r)
	}
}

func (w *walker) visit(v any) {
	switch n := v.(type) {
	case []any:
		for _, each := range n {
			w.visit(each)
		}
	case map[string]any:
		// In a fixed order: a map's keys come in none.
		keys := make([]string, 0, len(n))
		for k := range n {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			val := n[k]
			switch k {
			case "videoRenderer", "compactVideoRenderer", "gridVideoRenderer", "playlistVideoRenderer", "videoWithContextRenderer":
				if video, ok := videoRenderer(val, w.now); ok {
					w.add(Result{Kind: "video", Video: video})
				}
			case "lockupViewModel":
				if r, ok := lockup(val, w.now); ok {
					w.add(r)
				}
			case "shortsLockupViewModel", "reelItemRenderer":
				if video, ok := shortOf(val); ok {
					w.add(Result{Kind: "video", Video: video})
				}
			case "channelRenderer", "gridChannelRenderer":
				if channel, ok := channelRenderer(val); ok {
					w.add(Result{Kind: "channel", Channel: channel})
				}
			case "playlistRenderer", "gridPlaylistRenderer":
				if playlist, ok := playlistRenderer(val); ok {
					w.add(Result{Kind: "playlist", Playlist: playlist})
				}
			case "continuationItemRenderer":
				if t := str(dig(val, "continuationEndpoint", "continuationCommand", "token")); t != "" && w.next == "" {
					w.next = t
				}
			case "continuationItemViewModel":
				if t := str(dig(val, "continuationCommand", "innertubeCommand", "continuationCommand", "token")); t != "" && w.next == "" {
					w.next = t
				}
			default:
				w.visit(val)
			}
		}
	}
}

// dig follows keys through maps, and through an array by its first element.
func dig(v any, keys ...string) any {
	for _, k := range keys {
		if list, ok := v.([]any); ok {
			if len(list) == 0 {
				return nil
			}
			v = list[0]
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

// textOf is a text as the site writes one: plain, in runs, or a view
// model's content.
func textOf(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return str(v)
	}
	if s := str(m["simpleText"]); s != "" {
		return s
	}
	if s := str(m["content"]); s != "" {
		return s
	}
	var b strings.Builder
	for _, run := range list(m["runs"]) {
		b.WriteString(str(dig(run, "text")))
	}
	return b.String()
}

// picture is the widest of a picture's sizes: thumbnails, or a view model's
// sources.
func picture(v any) string {
	best, width := "", -1.0
	for _, key := range []string{"thumbnails", "sources"} {
		for _, each := range list(dig(v, key)) {
			u := str(dig(each, "url"))
			w, _ := dig(each, "width").(float64)
			if u != "" && w > width {
				best, width = u, w
			}
		}
	}
	if strings.HasPrefix(best, "//") {
		best = "https:" + best
	}
	return best
}

var (
	countText = regexp.MustCompile(`([\d][\d.,]*)\s*([KMB])?`)
	clockText = regexp.MustCompile(`^(?:(\d+):)?(\d{1,2}):(\d{2})$`)
	bareCount = regexp.MustCompile(`^\d[\d.,]*\s*[KMB]?$`)
	agoText   = regexp.MustCompile(`(\d+)\s*(seconds?|secs?|minutes?|mins?|hours?|hrs?|h|days?|d|weeks?|wks?|w|months?|mos?|years?|yrs?|y)\b`)
)

// countOf reads a count as the site writes it: 12,430 views, 435K, 4.55M
// subscribers. "No views" is none.
func countOf(text string) (int64, bool) {
	m := countText.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	digits := m[1]
	if m[2] == "" {
		digits = strings.NewReplacer(",", "", ".", "").Replace(digits)
	} else {
		digits = strings.ReplaceAll(digits, ",", "")
	}
	n, err := strconv.ParseFloat(digits, 64)
	if err != nil {
		return 0, false
	}
	switch m[2] {
	case "K":
		n *= 1e3
	case "M":
		n *= 1e6
	case "B":
		n *= 1e9
	}
	return int64(math.Round(n)), true
}

// clockOf reads a length as the site shows one: 52:14, 1:02:03.
func clockOf(text string) (int64, bool) {
	m := clockText.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return 0, false
	}
	h, _ := strconv.Atoi(m[1])
	mins, _ := strconv.Atoi(m[2])
	secs, _ := strconv.Atoi(m[3])
	return int64((h*3600 + mins*60 + secs) * 1000), true
}

// agoOf reads when something went up from the site's words — 3 days ago,
// 4mo ago, Streamed 2 weeks ago — as an instant, as near as they say.
func agoOf(text string, now time.Time) (time.Time, bool) {
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "ago") {
		return time.Time{}, false
	}
	m := agoText.FindStringSubmatch(lower)
	if m == nil {
		return time.Time{}, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return time.Time{}, false
	}
	switch unit := m[2]; {
	case strings.HasPrefix(unit, "s"):
		return now.Add(-time.Duration(n) * time.Second), true
	case strings.HasPrefix(unit, "mo"):
		return now.AddDate(0, -n, 0), true
	case strings.HasPrefix(unit, "mi"):
		return now.Add(-time.Duration(n) * time.Minute), true
	case strings.HasPrefix(unit, "h"):
		return now.Add(-time.Duration(n) * time.Hour), true
	case strings.HasPrefix(unit, "d"):
		return now.AddDate(0, 0, -n), true
	case strings.HasPrefix(unit, "w"):
		return now.AddDate(0, 0, -7*n), true
	case strings.HasPrefix(unit, "y"):
		return now.AddDate(-n, 0, 0), true
	}
	return time.Time{}, false
}

func stamp(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// browseID is the channel a run or a view model's tap leads to.
func browseID(v any) string {
	for _, path := range [][]string{
		{"navigationEndpoint", "browseEndpoint", "browseId"},
		{"onTap", "innertubeCommand", "browseEndpoint", "browseId"},
		{"rendererContext", "commandContext", "onTap", "innertubeCommand", "browseEndpoint", "browseId"},
	} {
		if id := str(dig(v, path...)); strings.HasPrefix(id, "UC") {
			return id
		}
	}
	for _, run := range list(dig(v, "commandRuns")) {
		if id := browseID(run); id != "" {
			return id
		}
	}
	return ""
}

// videoRenderer reads the site's older renderer of a video: a search's, a
// playlist's, a grid's.
func videoRenderer(v any, now time.Time) (*Video, bool) {
	id := str(dig(v, "videoId"))
	if !ValidID.MatchString(id) {
		return nil, false
	}
	video := &Video{ID: id, Title: textOf(dig(v, "title")), Thumbnail: picture(dig(v, "thumbnail"))}
	for _, key := range []string{"longBylineText", "ownerText", "shortBylineText"} {
		run := dig(v, key, "runs")
		if name, channel := str(dig(run, "text")), browseID(run); name != "" && channel != "" {
			video.Channel = &Owner{ID: channel, Name: name, Avatar: picture(dig(v, "channelThumbnailSupportedRenderers", "channelThumbnailWithLinkRenderer", "thumbnail"))}
			break
		}
	}
	if ms, ok := clockOf(textOf(dig(v, "lengthText"))); ok {
		video.DurationMs = ms
	} else if secs, err := strconv.ParseInt(str(dig(v, "lengthSeconds")), 10, 64); err == nil {
		video.DurationMs = secs * 1000
	}
	views := textOf(dig(v, "viewCountText"))
	published := textOf(dig(v, "publishedTimeText"))
	// A playlist's entries say both in one line: "1.2M views • 2 years ago".
	for _, run := range list(dig(v, "videoInfo", "runs")) {
		part := str(dig(run, "text"))
		switch {
		case strings.Contains(part, "view"):
			views = part
		case strings.Contains(part, "ago"):
			published = part
		}
	}
	if n, ok := countOf(views); ok && !strings.Contains(strings.ToLower(views), "no view") {
		video.Views = n
	}
	if at, ok := agoOf(published, now); ok {
		video.PublishedAt = stamp(at)
	}
	for _, badge := range list(dig(v, "badges")) {
		if str(dig(badge, "metadataBadgeRenderer", "style")) == "BADGE_STYLE_TYPE_LIVE_NOW" {
			video.Live = true
		}
	}
	for _, overlay := range list(dig(v, "thumbnailOverlays")) {
		switch str(dig(overlay, "thumbnailOverlayTimeStatusRenderer", "style")) {
		case "LIVE":
			video.Live = true
		case "UPCOMING":
			video.Upcoming = true
		case "SHORTS":
			video.Short = true
		}
	}
	if strings.Contains(views, "watching") {
		video.Live = true
	}
	if dig(v, "upcomingEventData") != nil {
		video.Upcoming = true
	}
	if dig(v, "navigationEndpoint", "reelWatchEndpoint") != nil {
		video.Short = true
	}
	return video, true
}

// labels are what a lockup says of what it is, never whose: "Playlist",
// "Mix".
var labels = []string{"playlist", "mix", "podcast", "album", "course", "view full playlist", "updated today", "updated yesterday"}

// lockup reads the site's newer view model of a video or a playlist: what
// it suggests, a channel's tabs, a playlist's videos.
func lockup(v any, now time.Time) (Result, bool) {
	id := str(dig(v, "contentId"))
	title := textOf(dig(v, "metadata", "lockupMetadataViewModel", "title"))
	image := dig(v, "contentImage", "thumbnailViewModel", "image")
	if image == nil {
		image = dig(v, "contentImage", "collectionThumbnailViewModel", "primaryThumbnail", "thumbnailViewModel", "image")
	}
	var badges []string
	live, upcoming := false, false
	overlays := list(dig(v, "contentImage", "thumbnailViewModel", "overlays"))
	overlays = append(overlays, list(dig(v, "contentImage", "collectionThumbnailViewModel", "primaryThumbnail", "thumbnailViewModel", "overlays"))...)
	for _, overlay := range overlays {
		for _, holder := range []any{dig(overlay, "thumbnailBottomOverlayViewModel", "badges"), dig(overlay, "thumbnailOverlayBadgeViewModel", "thumbnailBadges")} {
			for _, badge := range list(holder) {
				b := dig(badge, "thumbnailBadgeViewModel")
				text, style := str(dig(b, "text")), str(dig(b, "badgeStyle"))
				switch {
				case strings.Contains(style, "LIVE"), text == "LIVE":
					live = true
				case strings.Contains(style, "UPCOMING"), text == "UPCOMING", text == "PREMIERE":
					upcoming = true
				}
				badges = append(badges, text)
			}
		}
	}
	var owner *Owner
	avatar := dig(v, "metadata", "lockupMetadataViewModel", "image", "decoratedAvatarViewModel")
	ownerID := browseID(avatar)
	var parts []any
	for _, row := range list(dig(v, "metadata", "lockupMetadataViewModel", "metadata", "contentMetadataViewModel", "metadataRows")) {
		parts = append(parts, list(dig(row, "metadataParts"))...)
	}
	var views, published, name string
	for _, part := range parts {
		text := textOf(dig(part, "text"))
		lower := strings.ToLower(text)
		switch {
		case text == "", slices.Contains(labels, lower):
		// A suggestion's views are a bare count beside a play icon: 435K.
		case strings.Contains(lower, "view"), strings.Contains(lower, "watching"), str(dig(part, "leadingIcon", "name")) == "PLAY_ARROW_OUTLINED", bareCount.MatchString(text):
			views = text
		case strings.Contains(lower, "ago"), strings.HasPrefix(lower, "streamed"), strings.HasPrefix(lower, "premier"), strings.HasPrefix(lower, "scheduled"):
			published = text
		case name != "":
			// The channel is the first name a lockup gives.
		default:
			if id := browseID(dig(part, "text")); id != "" {
				ownerID, name = id, text
			} else if ownerID != "" || avatar != nil {
				name = text
			}
		}
	}
	if ownerID != "" && name != "" {
		owner = &Owner{ID: ownerID, Name: name, Avatar: picture(dig(avatar, "avatar", "avatarViewModel", "image"))}
	}
	switch str(dig(v, "contentType")) {
	case "LOCKUP_CONTENT_TYPE_VIDEO":
		if !ValidID.MatchString(id) {
			return Result{}, false
		}
		video := &Video{ID: id, Title: title, Channel: owner, Thumbnail: picture(image), Live: live, Upcoming: upcoming}
		for _, b := range badges {
			if ms, ok := clockOf(b); ok {
				video.DurationMs = ms
			}
		}
		if n, ok := countOf(views); ok && !strings.Contains(strings.ToLower(views), "no view") {
			video.Views = n
		}
		if strings.Contains(strings.ToLower(views), "watching") {
			video.Live = true
		}
		if at, ok := agoOf(published, now); ok {
			video.PublishedAt = stamp(at)
		}
		return Result{Kind: "video", Video: video}, true
	case "LOCKUP_CONTENT_TYPE_PLAYLIST", "LOCKUP_CONTENT_TYPE_ALBUM", "LOCKUP_CONTENT_TYPE_PODCAST":
		// A mix is a radio the site makes as it plays, no list a page opens.
		if id == "" || strings.HasPrefix(id, "RD") {
			return Result{}, false
		}
		playlist := &Playlist{ID: id, Title: title, Channel: owner, Thumbnail: picture(image)}
		for _, b := range badges {
			if strings.Contains(strings.ToLower(b), "video") || strings.Contains(strings.ToLower(b), "episode") {
				if n, ok := countOf(b); ok {
					playlist.VideoCount = n
				}
			}
		}
		return Result{Kind: "playlist", Playlist: playlist}, true
	}
	return Result{}, false
}

// shortOf reads a short, as a tab of shorts or a shelf lists it.
func shortOf(v any) (*Video, bool) {
	id := str(dig(v, "onTap", "innertubeCommand", "reelWatchEndpoint", "videoId"))
	if id == "" {
		id = str(dig(v, "videoId"))
	}
	if id == "" {
		id = strings.TrimPrefix(str(dig(v, "entityId")), "shorts-shelf-item-")
	}
	if !ValidID.MatchString(id) {
		return nil, false
	}
	video := &Video{ID: id, Short: true, Title: textOf(dig(v, "overlayMetadata", "primaryText"))}
	if video.Title == "" {
		video.Title = textOf(dig(v, "headline"))
	}
	for _, image := range []any{dig(v, "thumbnailViewModel", "thumbnailViewModel", "image"), dig(v, "thumbnail"), dig(v, "onTap", "innertubeCommand", "reelWatchEndpoint", "thumbnail")} {
		if video.Thumbnail = picture(image); video.Thumbnail != "" {
			break
		}
	}
	views := textOf(dig(v, "overlayMetadata", "secondaryText"))
	if views == "" {
		views = textOf(dig(v, "viewCountText"))
	}
	if n, ok := countOf(views); ok {
		video.Views = n
	}
	return video, true
}

// channelRenderer reads a channel as a search lists it. The site moved its
// handle into what was once the followers' text, and the followers into what
// was once the videos': each is read by what it says.
func channelRenderer(v any) (*Channel, bool) {
	id := str(dig(v, "channelId"))
	if !strings.HasPrefix(id, "UC") {
		return nil, false
	}
	c := &Channel{ID: id, Name: textOf(dig(v, "title")), Avatar: picture(dig(v, "thumbnail")), Description: textOf(dig(v, "descriptionSnippet"))}
	for _, key := range []string{"subscriberCountText", "videoCountText"} {
		readChannelFact(c, textOf(dig(v, key)))
	}
	return c, c.Name != ""
}

func readChannelFact(c *Channel, text string) {
	lower := strings.ToLower(text)
	switch {
	case strings.HasPrefix(text, "@"):
		c.Handle = text
	case strings.Contains(lower, "subscriber"):
		if n, ok := countOf(text); ok {
			c.Followers = n
		}
	case strings.Contains(lower, "video"):
		if n, ok := countOf(text); ok {
			c.VideoCount = n
		}
	}
}

// playlistRenderer reads a playlist as an older search lists it.
func playlistRenderer(v any) (*Playlist, bool) {
	id := str(dig(v, "playlistId"))
	if id == "" || strings.HasPrefix(id, "RD") {
		return nil, false
	}
	p := &Playlist{ID: id, Title: textOf(dig(v, "title"))}
	if n, err := strconv.ParseInt(str(dig(v, "videoCount")), 10, 64); err == nil {
		p.VideoCount = n
	} else if n, ok := countOf(textOf(dig(v, "videoCountText"))); ok {
		p.VideoCount = n
	}
	p.Thumbnail = picture(dig(v, "thumbnails"))
	if p.Thumbnail == "" {
		p.Thumbnail = picture(dig(v, "thumbnail"))
	}
	run := dig(v, "shortBylineText", "runs")
	if name, channel := str(dig(run, "text")), browseID(run); name != "" && channel != "" {
		p.Channel = &Owner{ID: channel, Name: name}
	}
	return p, true
}
