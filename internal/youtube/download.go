package youtube

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"foyer/internal/copies"
	"foyer/internal/media"
	"foyer/internal/sessions"
	"foyer/internal/stream"
)

// A video is kept as the site's own renditions: a picture and a sound chosen
// to the device's download settings, fetched by yt-dlp and put together by
// ffmpeg as they are. The site's AV1 and VP9 are small already — made anew
// they would only lose — so the size is chosen by the picture's height.

// fetchVersion is in every kept video's key: raise it when what one holds
// changes, and each is fetched again.
const fetchVersion = 1

// waysOffered is how many heights a video is offered at: the tallest the
// settings allow, and those beneath it.
const waysOffered = 4

// way is one way to keep a video: a picture of the site's, a sound, and the
// container they are put together in.
type way struct {
	Video, Audio Format
	Container    string
}

// id is the way as the app asks for it: opaque above the adapter.
func (w way) id() string { return "yt." + w.Video.ID + "+" + w.Audio.ID + "." + w.Container }

// parseWay reads a way's id.
func parseWay(id string) (video, audio, container string, ok bool) {
	rest, found := strings.CutPrefix(id, "yt.")
	if !found {
		return "", "", "", false
	}
	dot := strings.LastIndexByte(rest, '.')
	if dot < 0 {
		return "", "", "", false
	}
	formats, container := rest[:dot], rest[dot+1:]
	video, audio, found = strings.Cut(formats, "+")
	if !found || video == "" || audio == "" || (container != "mkv" && container != "mp4") {
		return "", "", "", false
	}
	return video, audio, container, true
}

// keyOf is a kept video's name among the copies: the video and its way.
func keyOf(id string, w way) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "youtube copy v%d|%s|%s+%s|%s", fetchVersion, id, w.Video.ID, w.Audio.ID, w.Container))
	return hex.EncodeToString(sum[:16])
}

// keepable is a format a way may hold: fetched whole by ranges — never a
// playlist's pieces — and none of the site's second takes: a sound made
// quieter and louder alike (DRC), a picture made bigger by guesswork.
func keepable(f Format) bool {
	note := strings.ToLower(f.Note)
	return f.Protocol == "https" && !strings.Contains(f.ID, "drc") && !strings.HasSuffix(f.ID, "-sr") &&
		!strings.Contains(note, "drc") && !strings.Contains(note, "upscaled")
}

// lines is a picture's height as "1080p" counts it: its short side.
func lines(f Format) int {
	if f.Width > 0 {
		return min(f.Width, f.Height)
	}
	return f.Height
}

// sizeOf is a format's bytes: as the site says, as yt-dlp thinks, or by its
// bitrate.
func sizeOf(f Format, seconds float64) int64 {
	switch {
	case f.Size > 0:
		return f.Size
	case f.Approx > 0:
		return f.Approx
	}
	return int64(f.TBR * 125 * seconds)
}

// hdrOf is a picture's high dynamic range, as Loge names it.
func hdrOf(f Format) string {
	switch strings.ToUpper(f.DynamicRange) {
	case "HDR10", "HDR12":
		return "hdr10"
	case "HLG":
		return "hlg"
	}
	return ""
}

// ways are the ways to keep a video to settings e: at each height no taller
// than e allows, the picture in the codec e asks for where the site has it —
// else the smallest kind there is, H.264 last but where asked for — and its
// richest sound in the video's own language, in the codec asked for where
// there is one. The tallest first.
func ways(info *Info, e copies.Encode) []way {
	order := []string{"av1", "vp9", "h264"}
	if e.Video == "h264" {
		order = []string{"h264", "vp9", "av1"}
	}
	rank := func(f Format) int { return slices.Index(order, videoCodec(f.VCodec)) }
	var pictures, sounds []Format
	for _, f := range info.Formats {
		switch {
		case !keepable(f):
		case f.hasVideo() && !f.hasAudio() && lines(f) > 0 && rank(f) >= 0 && (e.HDR || hdrOf(f) == ""):
			pictures = append(pictures, f)
		case f.hasAudio() && !f.hasVideo() && audioCodec(f.ACodec) != "":
			sounds = append(sounds, f)
		}
	}
	sound, ok := richestSound(sounds, e.Audio)
	if !ok {
		return nil
	}
	container := e.Container
	if container != "mp4" {
		container = "mkv"
	}
	better := func(a, b Format) int {
		return cmp.Or(
			cmp.Compare(rank(a), rank(b)),
			// HDR first where the settings keep it, the smoother, then the richer.
			cmp.Compare(boolRank(e.HDR && hdrOf(b) != ""), boolRank(e.HDR && hdrOf(a) != "")),
			cmp.Compare(b.FPS, a.FPS),
			cmp.Compare(b.TBR, a.TBR),
		)
	}
	byHeight := map[int]Format{}
	for _, f := range pictures {
		if e.MaxHeight > 0 && lines(f) > e.MaxHeight {
			continue
		}
		if held, ok := byHeight[lines(f)]; !ok || better(f, held) < 0 {
			byHeight[lines(f)] = f
		}
	}
	heights := make([]int, 0, len(byHeight))
	for h := range byHeight {
		heights = append(heights, h)
	}
	slices.SortFunc(heights, func(a, b int) int { return cmp.Compare(b, a) })
	out := []way{}
	for _, h := range heights[:min(len(heights), waysOffered)] {
		out = append(out, way{Video: byHeight[h], Audio: sound, Container: container})
	}
	return out
}

// richestSound is the sound a way keeps: in the video's own language — a
// dubbed video's says so by its preference — in the codec asked for where
// there is one, the richest.
func richestSound(sounds []Format, want string) (Format, bool) {
	if len(sounds) == 0 {
		return Format{}, false
	}
	own := slices.MaxFunc(sounds, func(a, b Format) int { return cmp.Compare(a.LanguagePreference, b.LanguagePreference) }).LanguagePreference
	return slices.MaxFunc(sounds, func(a, b Format) int {
		return cmp.Or(
			cmp.Compare(boolRank(a.LanguagePreference == own), boolRank(b.LanguagePreference == own)),
			cmp.Compare(boolRank(audioCodec(a.ACodec) == want), boolRank(audioCodec(b.ACodec) == want)),
			cmp.Compare(max(a.ABR, a.TBR), max(b.ABR, b.TBR)),
		)
	}), true
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// versionOf is what a way comes to, as a download's version says it.
func versionOf(info *Info, w way, size int64) media.Version {
	v := media.Version{
		File:       Key + ":" + info.ID,
		Size:       size,
		Quality:    media.Quality(w.Video.Width, w.Video.Height),
		Container:  w.Container,
		DurationMs: int64(info.Duration * 1000),
		Video: &media.Video{Codec: videoCodec(w.Video.VCodec), Width: w.Video.Width, Height: w.Video.Height, FrameRate: w.Video.FPS,
			HDR: hdrOf(w.Video), Bitrate: int64(w.Video.TBR * 1000)},
		Audio: []media.Audio{{Codec: audioCodec(w.Audio.ACodec), Language: w.Audio.Language, Channels: w.Audio.Channels,
			Bitrate: int64(max(w.Audio.ABR, w.Audio.TBR) * 1000), Default: true}},
		Subtitles: []media.Subtitle{},
	}
	return v
}

// encodeOf is what Live Activity says a kept video is made to.
func encodeOf(w way) copies.Encode {
	return copies.Encode{Video: videoCodec(w.Video.VCodec), MaxHeight: lines(w.Video), HDR: hdrOf(w.Video) != "",
		Audio: audioCodec(w.Audio.ACodec), Container: w.Container}
}

// Option is one way to keep a video, as the app is offered it — the shape
// a library's download options take.
type Option struct {
	ID          string   `json:"id"`
	Width       int      `json:"width,omitzero"`
	Height      int      `json:"height,omitzero"`
	Bitrate     int64    `json:"bitrate,omitzero"`
	Bytes       int64    `json:"estimatedBytes,omitzero"`
	Container   string   `json:"container"`
	VideoCodec  string   `json:"videoCodec,omitempty"`
	AudioCodecs []string `json:"audioCodecs,omitempty"`
	HDR         string   `json:"hdr,omitempty"`
	Transcoded  bool     `json:"transcoded"`
}

// Download is a kept video's address, good for a while, and what it is.
type Download struct {
	URL       string       `json:"url"`
	ExpiresAt time.Time    `json:"expiresAt"`
	Version   media.Public `json:"version"`
}

// DownloadRoutes mounts what a device asks to keep a video by: the ways it
// can, and one of them — waited for while Foyer fetches it.
func (y *YouTube) DownloadRoutes(r *router.Router[*core.RequestEvent]) {
	r.POST("/api/foyer/videos/download/options", y.downloadOptions).Bind(apis.RequireAuth("users"))
	r.POST("/api/foyer/videos/download", y.download).Bind(apis.RequireAuth("users"))
}

// asked is the video a download's request names, as yt-dlp says it is — or
// none, and why not already answered.
func (y *YouTube) asked(e *core.RequestEvent, id string) (*Info, error) {
	if y.Copies == nil || !y.Copies.Available() {
		return nil, refuse(e, Failed, "Foyer keeps no videos while making copies is off in its FFmpeg settings.")
	}
	info, err := y.Runner.Video(e.Request.Context(), id)
	var refusal *Refusal
	switch {
	case err == nil && info.Live():
		return nil, refuse(e, Failed, "A stream on the air cannot be kept.")
	case err == nil:
		return info, nil
	case errors.As(err, &refusal):
		return nil, refuse(e, refusal.Reason, refusal.Message)
	case errors.Is(err, ErrOff):
		return nil, refuse(e, Failed, "YouTube is switched off on this Foyer.")
	case errors.Is(err, context.Canceled):
		return nil, nil
	}
	e.App.Logger().Warn("yt-dlp could not read a video", "video", id, "error", err.Error())
	return nil, refuse(e, Failed, "Foyer could not read this video from the site.")
}

func (y *YouTube) downloadOptions(e *core.RequestEvent) error {
	var body struct {
		ID     string         `json:"id"`
		Encode *copies.Encode `json:"encode"`
	}
	if err := e.BindBody(&body); err != nil {
		return e.BadRequestError("The request could not be read.", err)
	}
	// Absent, the app's own defaults: AV1, Opus, Matroska, any height.
	encode := copies.Encode{Video: "av1", Audio: "opus", Container: "mkv"}
	if body.Encode != nil {
		if !body.Encode.Valid() {
			return e.BadRequestError("Those are no settings a video is kept to.", nil)
		}
		encode = *body.Encode
	}
	info, err := y.asked(e, body.ID)
	if info == nil {
		return err
	}
	options := []Option{}
	for _, w := range ways(info, encode) {
		bytes := sizeOf(w.Video, info.Duration) + sizeOf(w.Audio, info.Duration)
		if _, kept, ok := y.Copies.Path(keyOf(info.ID, w)); ok {
			bytes = kept.Size
		}
		v := versionOf(info, w, bytes)
		options = append(options, Option{ID: w.id(), Width: w.Video.Width, Height: w.Video.Height, Bitrate: v.Video.Bitrate + v.Audio[0].Bitrate,
			Bytes: bytes, Container: w.Container, VideoCodec: v.Video.Codec, AudioCodecs: []string{v.Audio[0].Codec}, HDR: v.Video.HDR})
	}
	return e.JSON(http.StatusOK, map[string]any{"options": options})
}

func (y *YouTube) download(e *core.RequestEvent) error {
	var body struct {
		ID     string `json:"id"`
		Option string `json:"option"`
	}
	if err := e.BindBody(&body); err != nil {
		return e.BadRequestError("The request could not be read.", err)
	}
	videoID, audioID, container, ok := parseWay(body.Option)
	if !ok {
		return e.BadRequestError("That is no way Foyer keeps a video.", nil)
	}
	info, err := y.asked(e, body.ID)
	if info == nil {
		return err
	}
	w := way{Container: container}
	for _, f := range info.Formats {
		switch {
		case !keepable(f):
		case f.ID == videoID && f.hasVideo():
			w.Video = f
		case f.ID == audioID && f.hasAudio():
			w.Audio = f
		}
	}
	if w.Video.ID == "" || w.Audio.ID == "" {
		return refuse(e, Failed, "The site no longer offers the video that way: ask again.")
	}
	bytes := sizeOf(w.Video, info.Duration) + sizeOf(w.Audio, info.Duration)
	r := copies.Request{
		Key:    keyOf(info.ID, w),
		File:   Key + ":" + info.ID,
		Source: media.Version{DurationMs: int64(info.Duration * 1000)},
		Recipe: copies.Recipe{Encode: encodeOf(w), Bytes: bytes},
		Title:  info.Title,
		User:   e.Auth.GetString("name"),
		Make:   y.maker(info.ID, w, bytes),
	}
	status := y.Copies.Ask(r)
	switch status.State {
	case copies.Done:
		expires := time.Now().Add(stream.Lifetime).UTC().Truncate(time.Second)
		id := sessions.NewID()
		address, err := y.Copies.Address(r.Key, e.Auth.Id, id, expires)
		if err != nil {
			return e.InternalServerError("", err)
		}
		y.playings.Start(sessions.Session{
			ID: id, User: e.Auth.Id, UserName: e.Auth.GetString("name"), Device: sessions.DeviceOf(e.Request), Kind: sessions.KindDownload,
			Item: sessions.Item{Collection: Key, ID: info.ID, Title: info.Title, Subtitle: info.Channel}, File: r.File, Mode: "copy",
			Expires: expires, DurationMs: r.Source.DurationMs,
		})
		return e.JSON(http.StatusOK, Download{URL: address, ExpiresAt: expires, Version: status.Made.Version.Public()})
	case copies.Failed:
		return e.Error(http.StatusUnprocessableEntity, "Foyer could not keep this video: "+status.Err, nil)
	}
	var answer struct {
		Preparing struct {
			State    string  `json:"state"`
			Progress float64 `json:"progress,omitzero"`
			EtaMs    int64   `json:"etaMs,omitzero"`
		} `json:"preparing"`
	}
	answer.Preparing.State, answer.Preparing.Progress, answer.Preparing.EtaMs = status.State, status.Progress, status.EtaMs
	return e.JSON(http.StatusAccepted, answer)
}

// maker fetches a way of a video for the copies: what yt-dlp says of it now
// — its links fresh — handed back to yt-dlp, which fetches the two and has
// ffmpeg put them together.
func (y *YouTube) maker(id string, w way, want int64) copies.Maker {
	return func(ctx context.Context, dir, tool string, progress func(done, speed float64)) (string, media.Version, error) {
		info, err := y.Runner.Video(ctx, id)
		if err != nil {
			return "", media.Version{}, err
		}
		file, err := y.Runner.fetch(ctx, info, dir, tool, w.Video.ID+"+"+w.Audio.ID, w.Container, want, progress)
		if err != nil {
			return "", media.Version{}, err
		}
		return file, versionOf(info, w, 0), nil
	}
}
