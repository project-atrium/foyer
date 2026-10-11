package library

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"foyer/internal/adapters/webdav"
	"foyer/internal/hls"
	"foyer/internal/media"
	"foyer/internal/sessions"
	"foyer/internal/stream"
)

// OpenFile opens a library file's bytes from the share: the stream's source, and
// the loopback's.
func (e *Engine) OpenFile(ctx context.Context, file *core.Record, header http.Header, head bool) (*http.Response, error) {
	lib, err := e.app.FindRecordById(LibrariesCollection, file.GetString("library"))
	if err != nil {
		return nil, stream.ErrGone
	}
	client, err := e.shareOf(lib)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", stream.ErrSource, err)
	}
	response, err := client.Open(ctx, lib.GetString("folder"), file.GetString("path"), header, head)
	switch {
	case errors.Is(err, webdav.ErrNotFound):
		return nil, stream.ErrGone
	case err != nil:
		return nil, fmt.Errorf("%w: %w", stream.ErrSource, err)
	}
	return response, nil
}

// Playback is what a player is handed: an address good for hours, the
// playing it belongs to — what its reports name — the version it plays, and
// its chapters. For a player that plays the picture and not the file, the
// address is a remux to HLS, and the file's own is beside it.
type Playback struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expiresAt"`
	Session   string    `json:"session"`
	// Convertible is a file a remux would play for this player, and the
	// household has converting switched off.
	Convertible bool            `json:"convertible,omitzero"`
	Mode        string          `json:"mode,omitempty"`
	File        string          `json:"file,omitempty"`
	Remux       *Remux          `json:"remux,omitzero"`
	Version     media.Public    `json:"version"`
	Versions    []media.Public  `json:"versions"`
	Chapters    []media.Chapter `json:"chapters"`
}

// Remux is what a remux carries: each audio rendition's codec, in the
// file's track order, and the subtitle tracks that come as WebVTT.
type Remux struct {
	Audio     []string `json:"audio"`
	Subtitles []int    `json:"subtitles"`
}

// Download is a file as it is, at an address asked for afresh each time: a
// copy that stopped asks again and carries on from the bytes it has.
type Download struct {
	URL       string       `json:"url"`
	ExpiresAt time.Time    `json:"expiresAt"`
	Version   media.Public `json:"version"`
}

// playable is what a user asked for by a title and, maybe, a file: the
// version and its file, every version of the title, and what a playing of
// it shows on Live Activity.
type playable struct {
	version  media.Version
	versions []media.Version
	file     *core.Record
	item     sessions.Item
	// profile is what the player plays, where it said; startMs where it starts.
	profile *hls.Profile
	startMs int64
}

// PlayRoutes mounts what a user's player asks for a title's stream by, and
// what a download asks for its file by. A title the user may not see — a
// guess not yet confirmed — has neither.
func (e *Engine) PlayRoutes(r *router.Router[*core.RequestEvent], streams *stream.Streamer, playings *sessions.Registry, remux *hls.Manager) {
	r.POST("/api/foyer/play", func(re *core.RequestEvent) error {
		p, err := e.asked(re)
		if err != nil {
			return err
		}
		decision := e.decide(re.Request.Context(), &p, remux)
		address, expires, session, err := e.address(re, streams, playings, p, sessions.KindPlay, decision.Mode)
		if err != nil {
			return re.InternalServerError("", err)
		}
		playback := Playback{URL: address, ExpiresAt: expires, Session: session, Convertible: decision.Convertible,
			Version: p.version.Public(), Versions: media.PublicAll(p.versions), Chapters: []media.Chapter{}}
		if decision.Mode == hls.Remux {
			plan := decision.Plan
			plan.StartMs = p.startMs
			master, err := remux.Address(hls.Ticket{File: p.file.Id, User: re.Auth.Id, Session: session, Expires: expires.Unix(), Plan: plan})
			if err != nil {
				return re.InternalServerError("", err)
			}
			codecs := make([]string, len(plan.Audio))
			for i, a := range plan.Audio {
				codecs[i] = a.Codec
			}
			playback.URL, playback.File, playback.Mode = master, address, hls.Remux
			playback.Remux = &Remux{Audio: codecs, Subtitles: plan.Subtitles}
		}
		if p.file.GetString("probe_state") == "done" {
			var probed media.Version
			if err := p.file.UnmarshalJSONField("probe", &probed); err == nil && probed.Chapters != nil {
				playback.Chapters = probed.Chapters
			}
		}
		return re.JSON(http.StatusOK, playback)
	}).Bind(apis.RequireAuth("users"))

	r.POST("/api/foyer/download", func(re *core.RequestEvent) error {
		p, err := e.asked(re)
		if err != nil {
			return err
		}
		address, expires, _, err := e.address(re, streams, playings, p, sessions.KindDownload, hls.Direct)
		if err != nil {
			return re.InternalServerError("", err)
		}
		return re.JSON(http.StatusOK, Download{URL: address, ExpiresAt: expires, Version: p.version.Public()})
	}).Bind(apis.RequireAuth("users"))
}

// decide is how a playing is served: the file as it is, unless the player
// said what it plays and a remux plays where the file does not. Converting
// for players is plan z, off until the household turns it on: off, the
// player is told only that it could be. The index of the file's keyframes
// is read for a remux alone, the first time.
func (e *Engine) decide(ctx context.Context, p *playable, remux *hls.Manager) hls.Decision {
	direct := hls.Decision{Mode: hls.Direct}
	if p.profile == nil || remux == nil || !p.profile.MayRemux(p.version) {
		return direct
	}
	if !remux.Available() {
		return hls.Decision{Mode: hls.Direct, Convertible: true}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// A remux carries each sound and subtitle the file holds: only ffprobe
	// says which, so a file it has not read yet is read now.
	if !p.version.Probed {
		if e.probeNow == nil || !e.probeNow(ctx, p.file) {
			return direct
		}
		fresh, err := e.app.FindRecordById(FilesCollection, p.file.Id)
		if err != nil {
			return direct
		}
		p.file, p.version = fresh, versionOf(fresh)
		if !p.version.Probed || !p.profile.MayRemux(p.version) {
			return direct
		}
	}
	idx, err := e.Keyframes(ctx, p.file)
	if err != nil {
		if !errors.Is(err, hls.ErrNoIndex) {
			e.app.Logger().Warn("A file's keyframes could not be read", "file", p.file.Id, "error", err.Error())
		}
		return direct
	}
	return hls.Decide(p.version, *p.profile, &idx)
}

// address is a new playing of a file, and its signed address, good for hours
// from now: Live Activity shows the playing, with the device that asked.
func (e *Engine) address(re *core.RequestEvent, streams *stream.Streamer, playings *sessions.Registry, p playable, kind, mode string) (string, time.Time, string, error) {
	expires := time.Now().Add(stream.Lifetime).UTC().Truncate(time.Second)
	id := sessions.NewID()
	address, err := streams.Address(p.file.Id, re.Auth.Id, id, expires)
	if err != nil {
		return "", time.Time{}, "", err
	}
	playings.Start(sessions.Session{
		ID: id, User: re.Auth.Id, UserName: re.Auth.GetString("name"), Device: sessions.DeviceOf(re.Request), Kind: kind, Item: p.item, File: p.file.Id,
		Mode: mode, Expires: expires, DurationMs: p.version.DurationMs,
	})
	return address, expires, id, nil
}

// asked reads which title, and which of its files, a request is for: the
// file named — one of its versions, or of its extras — else its first
// version.
func (e *Engine) asked(re *core.RequestEvent) (playable, error) {
	var body struct {
		Collection string       `json:"collection"`
		ID         string       `json:"id"`
		File       string       `json:"file"`
		Profile    *hls.Profile `json:"profile"`
		StartMs    int64        `json:"startMs"`
	}
	if err := re.BindBody(&body); err != nil {
		return playable{}, re.BadRequestError("The request could not be read.", err)
	}
	if !Managed(e.app, body.Collection) {
		return playable{}, re.NotFoundError("", nil)
	}
	item, err := e.app.FindRecordById(body.Collection, body.ID)
	if err != nil {
		return playable{}, re.NotFoundError("", nil)
	}
	info, err := re.RequestInfo()
	if err != nil {
		return playable{}, err
	}
	if ok, _ := re.App.CanAccessRecord(item, info, item.Collection().ViewRule); !ok {
		return playable{}, re.NotFoundError("", nil)
	}
	p := playable{item: sessions.ItemOf(e.app, body.Collection, item.Id), profile: body.Profile, startMs: max(0, body.StartMs)}
	_ = item.UnmarshalJSONField("versions", &p.versions)
	switch {
	case body.File != "":
		found := false
		for _, v := range p.versions {
			if v.File == body.File {
				p.version, found = v, true
			}
		}
		// A series' extras play too: the films in its pack, its OVAs.
		var extras []extra
		_ = item.UnmarshalJSONField("extras", &extras)
		for _, x := range extras {
			if !found && x.File == body.File {
				if f, err := e.app.FindRecordById(FilesCollection, x.File); err == nil {
					p.version, found = versionOf(f), true
					// An extra, by its own name, and the title it came with.
					p.item.Subtitle, p.item.Title = p.item.Title, x.Title
				}
			}
		}
		if !found {
			return playable{}, re.NotFoundError("That is none of this title's files.", nil)
		}
	case len(p.versions) > 0:
		p.version = p.versions[0]
	default:
		return playable{}, re.NotFoundError("There is nothing to play here.", nil)
	}
	file, err := e.app.FindRecordById(FilesCollection, p.version.File)
	if err != nil || file.GetString("item_id") != item.Id || file.GetString("missing_since") != "" {
		return playable{}, re.NotFoundError("The file is gone.", nil)
	}
	p.file = file
	return p, nil
}
