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
	"foyer/internal/media"
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
// version it plays, and its chapters.
type Playback struct {
	URL       string          `json:"url"`
	ExpiresAt time.Time       `json:"expiresAt"`
	Version   media.Public    `json:"version"`
	Versions  []media.Public  `json:"versions"`
	Chapters  []media.Chapter `json:"chapters"`
}

// Download is a file as it is, at an address asked for afresh each time: a
// copy that stopped asks again and carries on from the bytes it has.
type Download struct {
	URL       string       `json:"url"`
	ExpiresAt time.Time    `json:"expiresAt"`
	Version   media.Public `json:"version"`
}

// playable is what a user asked for by a title and, maybe, a file: the
// version and its file, and every version of the title.
type playable struct {
	version  media.Version
	versions []media.Version
	file     *core.Record
}

// PlayRoutes mounts what a user's player asks for a title's stream by, and
// what a download asks for its file by. A title the user may not see — a
// guess not yet confirmed — has neither.
func (e *Engine) PlayRoutes(r *router.Router[*core.RequestEvent], streams *stream.Streamer) {
	r.POST("/api/foyer/play", func(re *core.RequestEvent) error {
		p, err := e.asked(re)
		if err != nil {
			return err
		}
		address, expires, err := e.address(streams, p.file, re.Auth.Id)
		if err != nil {
			return re.InternalServerError("", err)
		}
		playback := Playback{URL: address, ExpiresAt: expires, Version: p.version.Public(), Versions: media.PublicAll(p.versions), Chapters: []media.Chapter{}}
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
		address, expires, err := e.address(streams, p.file, re.Auth.Id)
		if err != nil {
			return re.InternalServerError("", err)
		}
		return re.JSON(http.StatusOK, Download{URL: address, ExpiresAt: expires, Version: p.version.Public()})
	}).Bind(apis.RequireAuth("users"))
}

// address is a file's signed address for a user, good for hours from now.
func (e *Engine) address(streams *stream.Streamer, file *core.Record, user string) (string, time.Time, error) {
	expires := time.Now().Add(stream.Lifetime).UTC().Truncate(time.Second)
	address, err := streams.Address(file.Id, user, expires)
	return address, expires, err
}

// asked reads which title, and which of its files, a request is for: the
// file named — one of its versions, or of its extras — else its first
// version.
func (e *Engine) asked(re *core.RequestEvent) (playable, error) {
	var body struct {
		Collection string `json:"collection"`
		ID         string `json:"id"`
		File       string `json:"file"`
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
	var p playable
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
