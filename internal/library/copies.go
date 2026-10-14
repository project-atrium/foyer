package library

import (
	"context"
	"errors"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/copies"
	"foyer/internal/hls"
	"foyer/internal/media"
	"foyer/internal/sessions"
	"foyer/internal/stream"
)

// copyOption is a smaller copy of the file made to the device's settings,
// where this Foyer makes copies and one would be smaller: its size made, or
// thought.
func (e *Engine) copyOption(ctx context.Context, p *playable, asked copies.Encode, made *copies.Manager) (DownloadOption, bool) {
	r, err := e.copyRequest(ctx, p, asked, made)
	if err != nil {
		return DownloadOption{}, false
	}
	if _, kept, ok := made.Path(r.Key); ok {
		return optionOf(kept.Version, r.Recipe.Encode.ID(), true), true
	}
	if !r.Recipe.Smaller(p.version) || made.NoSmaller(r.Key) {
		return DownloadOption{}, false
	}
	o := optionOf(r.Recipe.Made(p.version, r.Recipe.Bytes), r.Recipe.Encode.ID(), true)
	o.Bitrate = r.Recipe.Bitrate
	return o, true
}

// errNoCopy is a copy this Foyer will not make of the file, whyever.
var errNoCopy = errors.New("Foyer makes no copy of this file now.")

// copyRequest is the copy of the file made to asked: its key, what it will
// be. The file is read by ffprobe first where it was not — a copy keeps
// every sound and subtitle the file has, which only ffprobe says.
func (e *Engine) copyRequest(ctx context.Context, p *playable, asked copies.Encode, made *copies.Manager) (copies.Request, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !e.readied(ctx, p) {
		return copies.Request{}, errNoCopy
	}
	encode := asked.For(p.version)
	r := copies.Request{
		Key:    copies.Key(p.file.Id, p.file.GetInt64("size"), p.file.GetString("modified"), encode),
		File:   p.file.Id,
		Source: p.version,
		Title:  titleOf(p.item),
	}
	if _, kept, ok := made.Path(r.Key); ok {
		// Made already: whatever this ffmpeg can do now, that is the copy.
		r.Recipe = copies.Recipe{Encode: kept.Encode, Bytes: kept.Size}
		return r, nil
	}
	can, ok := made.Abilities(ctx)
	if !ok || !made.Available() {
		return copies.Request{}, errNoCopy
	}
	recipe, err := copies.Plan(p.version, e.dolbyVisionOf(ctx, p), encode, can)
	if err != nil {
		return copies.Request{}, err
	}
	r.Recipe = recipe
	return r, nil
}

// readied is the file as ffprobe read it — read now, where it was not.
func (e *Engine) readied(ctx context.Context, p *playable) bool {
	if p.version.Probed {
		return true
	}
	if e.probeNow == nil || !e.probeNow(ctx, p.file) {
		return false
	}
	fresh, err := e.app.FindRecordById(FilesCollection, p.file.Id)
	if err != nil {
		return false
	}
	p.file, p.version = fresh, versionOf(fresh)
	return p.version.Probed
}

// dolbyVisionOf is the Dolby Vision profile the file's own index names, 0
// for none: ffprobe takes Dolby Vision alone for no HDR at all.
func (e *Engine) dolbyVisionOf(ctx context.Context, p *playable) int {
	if p.version.Video == nil {
		return 0
	}
	switch strings.ToLower(p.version.Video.Codec) {
	case "hevc", "av1":
	default:
		return 0
	}
	idx, err := e.Keyframes(ctx, p.file)
	if err != nil {
		return 0
	}
	return idx.Video.DolbyVision
}

// downloadCopy answers a download of a smaller copy: its address once it is
// made, how far along it is until then, or that it could not be made — and
// the file as it is where the copy came out no smaller.
func (e *Engine) downloadCopy(re *core.RequestEvent, streams *stream.Streamer, playings *sessions.Registry, made *copies.Manager, p playable) error {
	asked, ok := copies.ParseID(p.option)
	if !ok || made == nil {
		return re.BadRequestError("That is no copy Foyer makes.", nil)
	}
	r, err := e.copyRequest(re.Request.Context(), &p, asked, made)
	if err != nil {
		return re.Error(http.StatusConflict, err.Error(), nil)
	}
	r.User = re.Auth.GetString("name")
	status := made.Ask(r)
	switch status.State {
	case copies.Done:
		if status.Made.NoSmaller {
			address, expires, _, err := e.address(re, streams, playings, p, sessions.KindDownload, hls.Direct)
			if err != nil {
				return re.InternalServerError("", err)
			}
			return re.JSON(http.StatusOK, Download{URL: address, ExpiresAt: expires, Version: p.version.Public()})
		}
		expires := time.Now().Add(stream.Lifetime).UTC().Truncate(time.Second)
		id := sessions.NewID()
		address, err := made.Address(r.Key, re.Auth.Id, id, expires)
		if err != nil {
			return re.InternalServerError("", err)
		}
		playings.Start(sessions.Session{
			ID: id, User: re.Auth.Id, UserName: re.Auth.GetString("name"), Device: sessions.DeviceOf(re.Request), Kind: sessions.KindDownload,
			Item: p.item, File: p.file.Id, Mode: "copy", Expires: expires, DurationMs: p.version.DurationMs,
		})
		return re.JSON(http.StatusOK, Download{URL: address, ExpiresAt: expires, Version: status.Made.Version.Public()})
	case copies.Failed:
		return re.Error(http.StatusUnprocessableEntity, "Foyer could not make this copy: "+status.Err, nil)
	}
	var answer Preparing
	answer.Preparing.State, answer.Preparing.Progress, answer.Preparing.EtaMs = status.State, status.Progress, status.EtaMs
	return re.JSON(http.StatusAccepted, answer)
}

// originalOption is the file as it is.
func originalOption(v media.Version) DownloadOption {
	container := v.Container
	if container == "" || container == "other" {
		container = strings.TrimPrefix(strings.ToLower(path.Ext(v.Path)), ".")
	}
	if container == "" {
		container = "mkv"
	}
	o := optionOf(v, copies.Original, false)
	o.Container = container
	return o
}

// optionOf is a version as a way to keep an item: its picture, each codec
// of its sound once, what it weighs.
func optionOf(v media.Version, id string, transcoded bool) DownloadOption {
	o := DownloadOption{ID: id, Bitrate: v.Bitrate, EstimatedBytes: v.Size, Container: v.Container, Transcoded: transcoded}
	if v.Video != nil {
		o.Width, o.Height, o.VideoCodec, o.HDR = v.Video.Width, v.Video.Height, v.Video.Codec, v.Video.HDR
	}
	for _, a := range v.Audio {
		if c := strings.ToLower(a.Codec); c != "" && !slices.Contains(o.AudioCodecs, c) {
			o.AudioCodecs = append(o.AudioCodecs, c)
		}
	}
	return o
}

// titleOf is what Live Activity calls an item: a series' episode by both.
func titleOf(item sessions.Item) string {
	if item.Subtitle != "" {
		return item.Title + " — " + item.Subtitle
	}
	return item.Title
}
