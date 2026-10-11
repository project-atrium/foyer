package youtube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// answer is a synthetic answer of the site's, shaped as its own.
func answer(t *testing.T, name string) any {
	t.Helper()
	b, err := os.ReadFile("testdata/innertube/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

var now = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

func TestTheSitesWordsAreRead(t *testing.T) {
	for text, want := range map[string]int64{"12,430 views": 12430, "435K": 435000, "4.55M subscribers": 4550000, "1.2K watching": 1200, "437 videos": 437, "3 videos": 3} {
		if got, ok := countOf(text); !ok || got != want {
			t.Errorf("%q: %d", text, got)
		}
	}
	if _, ok := countOf("No views"); ok {
		t.Error("no views counted")
	}
	for text, want := range map[string]int64{"52:14": 3134000, "1:02:03": 3723000, "0:59": 59000} {
		if got, ok := clockOf(text); !ok || got != want {
			t.Errorf("%q: %d", text, got)
		}
	}
	for text, want := range map[string]time.Time{
		"3 days ago": now.AddDate(0, 0, -3), "4mo ago": now.AddDate(0, -4, 0), "1 year ago": now.AddDate(-1, 0, 0),
		"Streamed 2 weeks ago": now.AddDate(0, 0, -14), "15 minutes ago": now.Add(-15 * time.Minute), "5h ago": now.Add(-5 * time.Hour),
	} {
		if got, ok := agoOf(text, now); !ok || !got.Equal(want) {
			t.Errorf("%q: %v", text, got)
		}
	}
	for text, want := range map[string]time.Time{"Oct 8, 2026": time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC), "Premiered Oct 8, 2026": time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)} {
		if got, ok := dateOf(text, now); !ok || !got.Equal(want) {
			t.Errorf("%q: %v", text, got)
		}
	}
}

func TestASearchIsReadInTheSitesOrderEachOnce(t *testing.T) {
	results, next := collect(answer(t, "search.json"), now)
	var got []string
	for _, r := range results {
		switch {
		case r.Video != nil:
			got = append(got, "video "+r.Video.ID)
		case r.Channel != nil:
			got = append(got, "channel "+r.Channel.ID)
		case r.Playlist != nil:
			got = append(got, "playlist "+r.Playlist.ID)
		}
	}
	if strings.Join(got, ", ") != "video x3TJFoQWCHA, channel UCabcdefghijklmnopqrstuv, playlist PLlaN88a7y2_qHDbY9eQbuNTAuEJUSEeuu, video jfKfPfyJRdk, video Sh0rtVideo1" {
		t.Fatalf("read %q", got)
	}
	if next != "EpoDEhFyaXZlciBkb2N1bWVudGFyeRqEA0VnSVFBVWdV" {
		t.Errorf("next %q", next)
	}
	film := results[0].Video
	if film.Title != "The River, a Film" || film.DurationMs != 5731000 || film.Views != 1204331 || film.PublishedAt != "2026-10-08T12:00:00Z" ||
		film.Channel == nil || film.Channel.ID != "UCabcdefghijklmnopqrstuv" || !strings.HasSuffix(film.Channel.Avatar, "s68") || !strings.Contains(film.Thumbnail, "hq720") {
		t.Errorf("the film %+v %+v", film, film.Channel)
	}
	// The site moved a channel's handle and followers about: each is read by what it says.
	channel := results[1].Channel
	if channel.Handle != "@riversoftheworld" || channel.Followers != 182000 || channel.Avatar != "https://yt3.googleusercontent.com/rivers=s176" {
		t.Errorf("the channel %+v", channel)
	}
	if p := results[2].Playlist; p.VideoCount != 12 || p.Channel == nil || p.Channel.Name != "Rivers of the World" {
		t.Errorf("the playlist %+v %+v", p, p.Channel)
	}
	if radio := results[3].Video; !radio.Live || radio.Views != 31000 || radio.DurationMs != 0 {
		t.Errorf("live %+v", radio)
	}
	if short := results[4].Video; !short.Short || short.Views != 2100000 || !strings.HasSuffix(short.Thumbnail, "oardefault.jpg") {
		t.Errorf("short %+v", short)
	}
	// A mix is no list a page opens.
	mix := map[string]any{"lockupViewModel": map[string]any{"contentId": "RDdQw4w9WgXcQ", "contentType": "LOCKUP_CONTENT_TYPE_PLAYLIST",
		"metadata": map[string]any{"lockupMetadataViewModel": map[string]any{"title": map[string]any{"content": "Mix"}}}}}
	if got, _ := collect([]any{mix}, now); len(got) != 0 {
		t.Errorf("a mix read: %+v", got)
	}
}

func TestAVideosPageIsReadWithWhatTheSiteSuggests(t *testing.T) {
	video, related := videoPage(answer(t, "next.json"), "x3TJFoQWCHA", now)
	if video.Title != "The River, a Film" || video.Views != 1204331 || video.PublishedAt != "2026-10-08T00:00:00Z" || video.Channel.Followers != 182000 ||
		!strings.HasSuffix(video.Channel.Avatar, "s88") || !strings.HasPrefix(video.Description, "Two hours") {
		t.Errorf("the video %+v %+v", video, video.Channel)
	}
	if len(related) != 2 || related[0].ID != "ZHzUjw5VInQ" || related[0].Views != 435000 || related[0].DurationMs != 3134000 ||
		related[0].Channel == nil || related[0].Channel.Name != "Seas of the World" || related[0].Channel.ID != "UCSJ4gkVC6NrvII8umztf0Ow" ||
		related[0].PublishedAt != now.AddDate(0, -4, 0).Format(time.RFC3339) || related[1].Views != 1200000 {
		t.Errorf("related %+v", related)
	}
}

func TestAChannelsPageNamesItsTabs(t *testing.T) {
	a := answer(t, "channel.json")
	channel, tabs := channelPage(a)
	if channel.ID != "UCabcdefghijklmnopqrstuv" || channel.Handle != "@riversoftheworld" || channel.Followers != 182000 || channel.VideoCount != 437 ||
		!strings.HasSuffix(channel.Banner, "w2120") || !strings.HasSuffix(channel.Avatar, "s176") || channel.Description == "" {
		t.Errorf("the channel %+v", channel)
	}
	if len(tabs) != 4 || tabs["playlists"] != "EglwbGF5bGlzdHPyBgoKCEIGCgIQaCIA" || tabs["shorts"] != "EgZzaG9ydHPyBgUKA5oBAA==" {
		t.Errorf("tabs %v", tabs)
	}
	results, next := collect(a, now)
	if len(results) != 2 || results[0].Video.Channel != nil || results[1].Video.Views != 105000 || next == "" {
		t.Errorf("its videos %+v %q", results, next)
	}
}

func TestAPlaylistsPageIsRead(t *testing.T) {
	a := answer(t, "playlist.json")
	p := playlistPage(a, "PLlaN88a7y2_qHDbY9eQbuNTAuEJUSEeuu")
	if p.Title != "Rivers, the Whole Series" || p.VideoCount != 12 || p.Channel == nil || p.Channel.Name != "Rivers of the World" || p.Thumbnail == "" || p.Description == "" {
		t.Errorf("the playlist %+v %+v", p, p.Channel)
	}
	results, next := collect(a, now)
	if len(results) != 2 || results[1].Video.DurationMs != 721000 || results[1].Video.Views != 105000 || next != "4qmFsgJbEiRWTFBMbGFOODhhN3ky" {
		t.Errorf("its videos %+v %q", results, next)
	}
}

func TestTheFeedIsEachChannelsOwnNewestFirst(t *testing.T) {
	feeds := map[string][2]string{
		"UCabcdefghijklmnopqrstuv": {"2026-10-08T09:00:00+00:00", "2026-09-30T12:00:00+00:00"},
		"UCSJ4gkVC6NrvII8umztf0Ow": {"2026-10-10T18:00:00+00:00", "2026-10-01T08:00:00+00:00"},
	}
	asked := 0
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("channel_id")
		dates, ok := feeds[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		asked++
		b, _ := os.ReadFile("testdata/innertube/feed.xml")
		doc := strings.NewReplacer("CHANNEL", id, "NAME", "Channel "+id[2:5], "VIDEO1", id[2:13], "VIDEO2", id[13:24], "TITLE1", "Newest of "+id[2:5],
			"TITLE2", "A short of "+id[2:5], "PUBLISHED1", dates[0], "PUBLISHED2", dates[1]).Replace(string(b))
		_, _ = w.Write([]byte(doc))
	}))
	defer site.Close()
	f := NewFeeds(site.Client())
	f.Site = site.URL
	videos, complete := f.Newest(context.Background(), []string{"UCabcdefghijklmnopqrstuv", "UCSJ4gkVC6NrvII8umztf0Ow", "UCzzzzzzzzzzzzzzzzzzzzzz"}, 5*time.Second)
	if !complete || len(videos) != 4 {
		t.Fatalf("%d videos, complete %v", len(videos), complete)
	}
	var order []string
	for _, v := range videos {
		order = append(order, v.PublishedAt)
	}
	if strings.Join(order, " ") != "2026-10-10T18:00:00Z 2026-10-08T09:00:00Z 2026-10-01T08:00:00Z 2026-09-30T12:00:00Z" {
		t.Errorf("order %q", order)
	}
	if first := videos[0]; first.Channel == nil || first.Channel.ID != "UCSJ4gkVC6NrvII8umztf0Ow" || first.Views != 1204331 || first.Short || first.Description == "" {
		t.Errorf("the newest %+v", first)
	}
	if !videos[3].Short {
		t.Error("a short not told apart")
	}
	// Kept: asked again, the site is not.
	before := asked
	if again, _ := f.Newest(context.Background(), []string{"UCabcdefghijklmnopqrstuv"}, time.Second); len(again) != 2 || asked != before {
		t.Errorf("asked again: %d videos, %d asks", len(again), asked-before)
	}
}
