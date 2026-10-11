package youtube

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// ValidChannel is a channel's id as the site writes it.
var ValidChannel = regexp.MustCompile(`^UC[0-9A-Za-z_-]{22}$`)

// feedKept is how long a channel's own feed is read again from what was
// kept: it changes when the channel puts something up.
const feedKept = 15 * time.Minute

// Feeds reads each channel's own Atom feed — its newest videos, each with
// exactly when it went up — a few at once, and keeps each a while.
type Feeds struct {
	// Site is the site's address; a test points it elsewhere.
	Site   string
	client *http.Client
	now    func() time.Time
	mu     sync.Mutex
	kept   map[string]feed
	busy   map[string]bool
	slots  chan struct{}
}

type feed struct {
	videos []Video
	until  time.Time
}

// NewFeeds reads feeds through client, eight at once.
func NewFeeds(client *http.Client) *Feeds {
	return &Feeds{Site: "https://www.youtube.com", client: client, now: time.Now, kept: map[string]feed{}, busy: map[string]bool{}, slots: make(chan struct{}, 8)}
}

// Newest are the newest videos of the channels, newest first: what is at
// hand within wait, and whether every channel was — the rest are read on,
// for the next asking.
func (f *Feeds) Newest(ctx context.Context, channels []string, wait time.Duration) ([]Video, bool) {
	deadline := time.Now().Add(wait)
	for {
		var videos []Video
		missing := 0
		f.mu.Lock()
		for _, id := range channels {
			if k, ok := f.kept[id]; ok {
				videos = append(videos, k.videos...)
				if f.now().After(k.until) && !f.busy[id] {
					f.busy[id] = true
					go f.read(id)
				}
				continue
			}
			missing++
			if !f.busy[id] {
				f.busy[id] = true
				go f.read(id)
			}
		}
		f.mu.Unlock()
		if missing == 0 || time.Now().After(deadline) || ctx.Err() != nil {
			slices.SortStableFunc(videos, func(a, b Video) int { return strings.Compare(b.PublishedAt, a.PublishedAt) })
			return videos, missing == 0
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// read reads one channel's feed and keeps it — an empty one where the site
// would not say, so a channel gone does not hold the rest up.
func (f *Feeds) read(id string) {
	f.slots <- struct{}{}
	defer func() { <-f.slots }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	videos, err := f.fetch(ctx, id)
	kept := feedKept
	if err != nil {
		videos, kept = nil, 2*time.Minute
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.busy, id)
	if err != nil {
		if old, ok := f.kept[id]; ok {
			// What was read before stands a little longer.
			videos = old.videos
		}
	}
	f.kept[id] = feed{videos: videos, until: f.now().Add(kept)}
}

type atom struct {
	Author struct {
		Name string `xml:"name"`
	} `xml:"author"`
	ChannelID string `xml:"http://www.youtube.com/xml/schemas/2015 channelId"`
	Entries   []struct {
		VideoID   string `xml:"http://www.youtube.com/xml/schemas/2015 videoId"`
		ChannelID string `xml:"http://www.youtube.com/xml/schemas/2015 channelId"`
		Title     string `xml:"title"`
		Published string `xml:"published"`
		Links     []struct {
			Rel  string `xml:"rel,attr"`
			Href string `xml:"href,attr"`
		} `xml:"link"`
		Author struct {
			Name string `xml:"name"`
		} `xml:"author"`
		Group struct {
			Thumbnail struct {
				URL string `xml:"url,attr"`
			} `xml:"http://search.yahoo.com/mrss/ thumbnail"`
			Description string `xml:"http://search.yahoo.com/mrss/ description"`
			Community   struct {
				Statistics struct {
					Views int64 `xml:"views,attr"`
				} `xml:"http://search.yahoo.com/mrss/ statistics"`
			} `xml:"http://search.yahoo.com/mrss/ community"`
		} `xml:"http://search.yahoo.com/mrss/ group"`
	} `xml:"entry"`
}

func (f *Feeds) fetch(ctx context.Context, id string) ([]Video, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.Site+"/feeds/videos.xml?channel_id="+id, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", agent)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w (%d)", ErrRefused, resp.StatusCode)
	}
	var doc atom
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&doc); err != nil {
		return nil, err
	}
	videos := make([]Video, 0, len(doc.Entries))
	for _, e := range doc.Entries {
		if !ValidID.MatchString(e.VideoID) {
			continue
		}
		v := Video{ID: e.VideoID, Title: e.Title, Thumbnail: e.Group.Thumbnail.URL, Description: e.Group.Description, Views: e.Group.Community.Statistics.Views}
		channel := e.ChannelID
		if channel == "" {
			channel = doc.ChannelID
		}
		if name := e.Author.Name; name != "" && ValidChannel.MatchString(channel) {
			v.Channel = &Owner{ID: channel, Name: name}
		}
		if at, err := time.Parse(time.RFC3339, e.Published); err == nil {
			v.PublishedAt = stamp(at)
		}
		for _, l := range e.Links {
			if l.Rel == "alternate" && strings.Contains(l.Href, "/shorts/") {
				v.Short = true
			}
		}
		videos = append(videos, v)
	}
	return videos, nil
}
