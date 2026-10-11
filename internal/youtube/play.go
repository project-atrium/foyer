package youtube

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"foyer/internal/adapters"
	"foyer/internal/ffmpeg"
	"foyer/internal/sessions"
	"foyer/internal/stream"
)

// Key is the YouTube adapter's key in the registry.
const Key = "youtube"

// YouTube is the site through Foyer: what yt-dlp says of a video, its
// streams relayed, and the site's own API for everything else.
type YouTube struct {
	Runner   *Runner
	Relay    *Relay
	Tube     *InnerTube
	Feeds    *Feeds
	playings *sessions.Registry
	seen     seen
	mu       sync.Mutex
	tabs     map[string]channelTabs
}

type channelTabs struct {
	params map[string]string
	until  time.Time
}

// New is YouTube on a server: yt-dlp as the adapter's settings name it —
// none while it is off — its cache under dir, the site asked for region's
// videos, each playing in playings.
func New(tool func() (string, bool), region func() string, dir string, streams *stream.Streamer, playings *sessions.Registry) *YouTube {
	browsing := &http.Client{Timeout: 30 * time.Second}
	return &YouTube{
		Runner:   NewRunner(tool, dir),
		Relay:    NewRelay(streams.Sign, streams.Derive, playings),
		Tube:     NewInnerTube(browsing, region),
		Feeds:    NewFeeds(browsing),
		playings: playings,
	}
}

// Source is one way to play a video, as the app is handed it.
type Source struct {
	URL         string   `json:"url"`
	Protocol    string   `json:"protocol"`
	Container   string   `json:"container,omitempty"`
	VideoCodec  string   `json:"videoCodec,omitempty"`
	AudioCodecs []string `json:"audioCodecs,omitempty"`
	Height      int      `json:"height,omitzero"`
}

// Subtitle is a caption beside the stream, as WebVTT.
type Subtitle struct {
	URL      string `json:"url"`
	Language string `json:"language,omitempty"`
	Label    string `json:"label"`
	Auto     bool   `json:"auto,omitzero"`
}

// ChapterMark is a chapter as Loge reads one.
type ChapterMark struct {
	StartMs int64  `json:"start_ms"`
	Title   string `json:"title,omitempty"`
}

// Playback is what play answers: a playing, its sources best first, and its
// captions — every address Foyer's own.
type Playback struct {
	Session    string        `json:"session"`
	ExpiresAt  time.Time     `json:"expiresAt"`
	Live       bool          `json:"live"`
	DurationMs int64         `json:"durationMs,omitzero"`
	Sources    []Source      `json:"sources"`
	Subtitles  []Subtitle    `json:"subtitles"`
	Chapters   []ChapterMark `json:"chapters"`
}

// Routes mounts what a user's device browses the site by, what its player
// plays a video by, and the relay.
func (y *YouTube) Routes(r *router.Router[*core.RequestEvent]) {
	y.Relay.Routes(r)
	y.BrowseRoutes(r)
	r.POST("/api/foyer/videos/play", y.play).Bind(apis.RequireAuth("users"))
}

// refuse answers why the site will not play a video: never a 401, which is
// the user's own session's alone.
func refuse(e *core.RequestEvent, reason, message string) error {
	return e.JSON(http.StatusConflict, map[string]any{"status": http.StatusConflict, "message": message, "data": map[string]any{}, "reason": reason})
}

func (y *YouTube) play(e *core.RequestEvent) error {
	var body struct {
		ID      string `json:"id"`
		StartMs int64  `json:"startMs"`
	}
	if err := e.BindBody(&body); err != nil {
		return e.BadRequestError("The request could not be read.", err)
	}
	info, err := y.Runner.Video(e.Request.Context(), body.ID)
	var refusal *Refusal
	switch {
	case errors.As(err, &refusal):
		return refuse(e, refusal.Reason, refusal.Message)
	case errors.Is(err, ErrOff):
		return refuse(e, Failed, "YouTube is switched off on this Foyer.")
	case errors.Is(err, context.Canceled):
		return nil
	case err != nil:
		e.App.Logger().Warn("yt-dlp could not read a video", "video", body.ID, "error", err.Error())
		return refuse(e, Failed, "Foyer could not read this video from the site.")
	}
	now := time.Now()
	expires := now.Add(stream.Lifetime)
	if site := info.Expires(now).Add(-5 * time.Minute); site.Before(expires) {
		expires = site
	}
	expires = expires.UTC().Truncate(time.Second)
	session := sessions.NewID()
	base, err := y.Relay.issue(ticket{ID: info.ID, User: e.Auth.Id, Session: session, Expires: expires.Unix()})
	if err != nil {
		return e.InternalServerError("", err)
	}
	base = Prefix + base
	s, err := y.Relay.sealer()
	if err != nil {
		return e.InternalServerError("", err)
	}
	sealed := func(kind, link, ext string) (string, error) {
		token, err := s.seal(link)
		return address(base, kind, token, ext), err
	}
	answer := Playback{Session: session, ExpiresAt: expires, Live: info.Live(), Sources: []Source{}, Subtitles: []Subtitle{}, Chapters: []ChapterMark{}}
	if !answer.Live && info.Duration > 0 {
		answer.DurationMs = int64(info.Duration * 1000)
	}
	if master := info.Master(); master != "" {
		link, err := sealed("p", master, ".m3u8")
		if err != nil {
			return e.InternalServerError("", err)
		}
		answer.Sources = append(answer.Sources, Source{URL: link, Protocol: "hls"})
	}
	if muxed, ok := info.Muxed(); ok && !answer.Live {
		link, err := sealed("x", muxed.URL, "."+muxed.Ext)
		if err != nil {
			return e.InternalServerError("", err)
		}
		source := Source{URL: link, Protocol: "progressive", Container: muxed.Ext, VideoCodec: videoCodec(muxed.VCodec), Height: muxed.Height}
		if codec := audioCodec(muxed.ACodec); codec != "" {
			source.AudioCodecs = []string{codec}
		}
		answer.Sources = append(answer.Sources, source)
	}
	if len(answer.Sources) == 0 {
		return refuse(e, Failed, "The site offered no stream Foyer can relay.")
	}
	for _, c := range info.Captions() {
		link, err := sealed("x", vttOf(c.URL), ".vtt")
		if err != nil {
			return e.InternalServerError("", err)
		}
		answer.Subtitles = append(answer.Subtitles, Subtitle{URL: link, Language: c.Language, Label: c.Label, Auto: c.Auto})
	}
	for _, c := range info.Chapters {
		answer.Chapters = append(answer.Chapters, ChapterMark{StartMs: int64(c.Start * 1000), Title: c.Title})
	}
	played := &Video{ID: info.ID, Title: info.Title, Thumbnail: info.Picture()}
	if info.Channel != "" && ValidChannel.MatchString(info.ChannelID) {
		played.Channel = &Owner{ID: info.ChannelID, Name: info.Channel}
	}
	y.seen.remember(played)
	y.playings.Start(sessions.Session{
		ID: session, User: e.Auth.Id, UserName: e.Auth.GetString("name"), Device: sessions.DeviceOf(e.Request), Kind: sessions.KindPlay,
		Item: sessions.Item{Collection: Key, ID: info.ID, Title: info.Title, Subtitle: info.Channel}, File: Key + ":" + info.ID, Mode: "relay",
		Expires: expires, DurationMs: answer.DurationMs,
	})
	return e.JSON(http.StatusOK, answer)
}

// vttOf is a caption's link asking for WebVTT, which every player here reads.
func vttOf(link string) string {
	if strings.Contains(link, "fmt=") {
		return link
	}
	if strings.Contains(link, "?") {
		return link + "&fmt=vtt"
	}
	return link + "?fmt=vtt"
}

// Adapter is YouTube as the Adapters page shows it.
func Adapter() adapters.Adapter {
	return adapters.Adapter{
		Key:      Key,
		Category: adapters.Sources,
		Single:   true,
		Name:     "YouTube",
		Description: "YouTube's videos through Foyer. yt-dlp, here, says what each video is and where its streams are, and Foyer relays them: " +
			"the site binds its links to the address that asked, so a device away from home plays them too. Each user signed in with their own account, for their own recommendations, comes later.",
		Fields: []adapters.Field{
			{Key: "ytdlp", Label: "yt-dlp", Type: adapters.Text, Default: "yt-dlp", Help: "Its path, or its name on the PATH. It needs Deno on the PATH too, for the site's challenges."},
			{Key: "region", Label: "Region", Type: adapters.Text, Default: "US", Pattern: `^[A-Z]{2}$`},
			{Key: "language", Label: "Language", Type: adapters.Text, Default: "en", Pattern: `^[a-z]{2}(-[A-Z]{2})?$`},
		},
		Test: func(ctx context.Context, s adapters.Settings) adapters.Result {
			tool := s.String("ytdlp")
			if tool == "" {
				tool = "yt-dlp"
			}
			return test(ctx, tool)
		},
	}
}

// test reads a short video the site has kept since its first days, as play
// would: whether yt-dlp and Deno run, and what the site offers to relay.
func test(ctx context.Context, tool string) adapters.Result {
	versions := []string{}
	if v, err := version(ctx, tool, "--version"); err == nil {
		versions = append(versions, "yt-dlp "+v)
	} else {
		return adapters.Result{Message: "yt-dlp does not run here: " + err.Error()}
	}
	if v, err := version(ctx, "deno", "--version"); err == nil {
		// "deno 2.9.7 (stable, release, …)"
		if fields := strings.Fields(v); len(fields) >= 2 {
			versions = append(versions, "Deno "+fields[1])
		} else {
			versions = append(versions, "Deno")
		}
	} else {
		versions = append(versions, "no Deno — the site's challenges go unsolved")
	}
	runner := NewRunner(func() (string, bool) { return tool, true }, filepath.Join(os.TempDir(), "foyer-youtube-test"))
	info, err := runner.Video(ctx, "jNQXAC9IVRw")
	if err != nil {
		var refusal *Refusal
		if errors.As(err, &refusal) {
			return adapters.Result{Message: strings.Join(versions, " · ") + " · " + refusal.Message}
		}
		return adapters.Result{Message: strings.Join(versions, " · ") + " · " + err.Error()}
	}
	var offers []string
	if info.Master() != "" {
		offers = append(offers, "HLS")
	}
	if _, ok := info.Muxed(); ok {
		offers = append(offers, "a muxed file")
	}
	if len(offers) == 0 {
		return adapters.Result{Message: "yt-dlp read a video, and the site offered nothing Foyer relays."}
	}
	slices.Sort(offers)
	return adapters.Result{OK: true, Message: strings.Join(versions, " · ") + " · a video read: " + strings.Join(offers, " and ") + "."}
}

// version is what a tool says of itself, its first line.
func version(ctx context.Context, tool string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := ffmpeg.Command(ctx, tool, args...).Output()
	if err != nil {
		return "", errors.New(filepath.Base(tool) + " is not installed here, or will not run")
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]), nil
}
