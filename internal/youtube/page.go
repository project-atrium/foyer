package youtube

import (
	"net/url"
	"strings"
	"time"
)

// Tabs are a channel's tabs Foyer knows, by the address of each on the site.
var Tabs = []struct{ Key, Path, Params string }{
	{"videos", "/videos", "EgZ2aWRlb3PyBgQKAjoA"},
	{"shorts", "/shorts", "EgZzaG9ydHPyBgUKA5oBAA=="},
	{"live", "/streams", "EgdzdHJlYW1z8gYECgJ6AA=="},
	{"playlists", "/playlists", "EglwbGF5bGlzdHPyBgQKAkIA"},
}

// channelPage reads a channel's page: its header, and the tabs it has with
// what asks for each.
func channelPage(answer any) (*Channel, map[string]string) {
	meta := dig(answer, "metadata", "channelMetadataRenderer")
	c := &Channel{ID: str(dig(meta, "externalId")), Name: str(dig(meta, "title")), Description: str(dig(meta, "description")), Avatar: picture(dig(meta, "avatar"))}
	if header := dig(answer, "header", "pageHeaderRenderer", "content", "pageHeaderViewModel"); header != nil {
		if name := textOf(dig(header, "title", "dynamicTextViewModel", "text")); name != "" {
			c.Name = name
		}
		if avatar := picture(dig(header, "image", "decoratedAvatarViewModel", "avatar", "avatarViewModel", "image")); avatar != "" {
			c.Avatar = avatar
		}
		c.Banner = picture(dig(header, "banner", "imageBannerViewModel", "image"))
		for _, row := range list(dig(header, "metadata", "contentMetadataViewModel", "metadataRows")) {
			for _, part := range list(dig(row, "metadataParts")) {
				readChannelFact(c, textOf(dig(part, "text")))
			}
		}
		if about := textOf(dig(header, "description", "descriptionPreviewViewModel", "description")); about != "" && c.Description == "" {
			c.Description = about
		}
	} else if old := dig(answer, "header", "c4TabbedHeaderRenderer"); old != nil {
		if id := str(dig(old, "channelId")); id != "" {
			c.ID = id
		}
		if name := str(dig(old, "title")); name != "" {
			c.Name = name
		}
		c.Banner = picture(dig(old, "banner"))
		for _, key := range []string{"channelHandleText", "subscriberCountText", "videosCountText"} {
			readChannelFact(c, textOf(dig(old, key)))
		}
	}
	tabs := map[string]string{}
	for _, tab := range list(dig(answer, "contents", "twoColumnBrowseResultsRenderer", "tabs")) {
		endpoint := dig(tab, "tabRenderer", "endpoint")
		path := str(dig(endpoint, "commandMetadata", "webCommandMetadata", "url"))
		params, _ := url.QueryUnescape(str(dig(endpoint, "browseEndpoint", "params")))
		for _, known := range Tabs {
			if strings.HasSuffix(path, known.Path) && params != "" {
				tabs[known.Key] = params
			}
		}
	}
	return c, tabs
}

// playlistPage reads what a playlist is from its page's header: the site's
// newer one, its older one, or its sidebar.
func playlistPage(answer any, id string) *Playlist {
	p := &Playlist{ID: id}
	if header := dig(answer, "header", "pageHeaderRenderer", "content", "pageHeaderViewModel"); header != nil {
		p.Title = textOf(dig(header, "title", "dynamicTextViewModel", "text"))
		p.Description = textOf(dig(header, "description", "descriptionPreviewViewModel", "description"))
		p.Thumbnail = picture(dig(header, "heroImage", "contentPreviewImageViewModel", "image"))
		for _, row := range list(dig(header, "metadata", "contentMetadataViewModel", "metadataRows")) {
			for _, part := range list(dig(row, "metadataParts")) {
				if stack := dig(part, "avatarStack", "avatarStackViewModel", "text"); stack != nil {
					if channel, name := browseID(stack), strings.TrimPrefix(textOf(stack), "by "); channel != "" && name != "" {
						p.Channel = &Owner{ID: channel, Name: name}
					}
					continue
				}
				if text := textOf(dig(part, "text")); strings.Contains(strings.ToLower(text), "video") {
					if n, ok := countOf(text); ok {
						p.VideoCount = n
					}
				}
			}
		}
	}
	if old := dig(answer, "header", "playlistHeaderRenderer"); old != nil && p.Title == "" {
		p.Title = textOf(dig(old, "title"))
		p.Description = textOf(dig(old, "descriptionText"))
		if n, ok := countOf(textOf(dig(old, "numVideosText"))); ok {
			p.VideoCount = n
		}
		run := dig(old, "ownerText", "runs")
		if name, channel := str(dig(run, "text")), browseID(run); name != "" && channel != "" {
			p.Channel = &Owner{ID: channel, Name: name}
		}
	}
	for _, item := range list(dig(answer, "sidebar", "playlistSidebarRenderer", "items")) {
		if primary := dig(item, "playlistSidebarPrimaryInfoRenderer"); primary != nil {
			if p.Title == "" {
				p.Title = textOf(dig(primary, "title"))
			}
			if p.VideoCount == 0 {
				if n, ok := countOf(textOf(dig(primary, "stats"))); ok {
					p.VideoCount = n
				}
			}
			if p.Thumbnail == "" {
				p.Thumbnail = picture(dig(primary, "thumbnailRenderer", "playlistVideoThumbnailRenderer", "thumbnail"))
			}
		}
		if owner := dig(item, "playlistSidebarSecondaryInfoRenderer", "videoOwner", "videoOwnerRenderer"); owner != nil && p.Channel == nil {
			run := dig(owner, "title", "runs")
			if name, channel := str(dig(run, "text")), browseID(run); name != "" && channel != "" {
				p.Channel = &Owner{ID: channel, Name: name, Avatar: picture(dig(owner, "thumbnail"))}
			}
		}
	}
	return p
}

// videoPage reads a video's page: what it is, its channel, and what the
// site suggests after it, in its order.
func videoPage(answer any, id string, now time.Time) (*Video, []Video) {
	video := &Video{ID: id, Thumbnail: "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg"}
	for _, item := range list(dig(answer, "contents", "twoColumnWatchNextResults", "results", "results", "contents")) {
		if primary := dig(item, "videoPrimaryInfoRenderer"); primary != nil {
			video.Title = textOf(dig(primary, "title"))
			count := dig(primary, "viewCount", "videoViewCountRenderer")
			views := textOf(dig(count, "viewCount"))
			if live, _ := dig(count, "isLive").(bool); live || strings.Contains(views, "watching") {
				video.Live = true
			}
			if n, ok := countOf(views); ok {
				video.Views = n
			}
			if at, ok := dateOf(textOf(dig(primary, "dateText")), now); ok {
				video.PublishedAt = stamp(at)
			}
		}
		if secondary := dig(item, "videoSecondaryInfoRenderer"); secondary != nil {
			owner := dig(secondary, "owner", "videoOwnerRenderer")
			run := dig(owner, "title", "runs")
			if name, channel := str(dig(run, "text")), browseID(run); name != "" && channel != "" {
				video.Channel = &Owner{ID: channel, Name: name, Avatar: picture(dig(owner, "thumbnail"))}
				if n, ok := countOf(textOf(dig(owner, "subscriberCountText"))); ok {
					video.Channel.Followers = n
				}
			}
			video.Description = textOf(dig(secondary, "attributedDescription"))
			if video.Description == "" {
				video.Description = textOf(dig(secondary, "description"))
			}
		}
	}
	results, _ := collect(dig(answer, "contents", "twoColumnWatchNextResults", "secondaryResults"), now)
	related := []Video{}
	for _, r := range results {
		if r.Video != nil && r.Video.ID != id {
			related = append(related, *r.Video)
		}
	}
	return video, related
}

// dateOf reads when a video went up from its page: "Oct 9, 2026",
// "Premiered Oct 9, 2026", "Streamed live on Oct 9, 2026" — or "Started
// streaming 2 hours ago".
func dateOf(text string, now time.Time) (time.Time, bool) {
	if at, ok := agoOf(text, now); ok {
		return at, true
	}
	for _, prefix := range []string{"Premiered ", "Streamed live on ", "Published on ", "Started streaming on ", "Scheduled for "} {
		text = strings.TrimPrefix(text, prefix)
	}
	at, err := time.Parse("Jan 2, 2006", strings.TrimSpace(text))
	return at, err == nil
}
