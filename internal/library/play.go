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

	"foyer/internal/adapters/zurg"
	"foyer/internal/media"
	"foyer/internal/stream"
)

// OpenFile opens a library file's bytes from Zurg: the stream's source, and
// the loopback's.
func (e *Engine) OpenFile(ctx context.Context, file *core.Record, header http.Header, head bool) (*http.Response, error) {
	lib, err := e.app.FindRecordById(LibrariesCollection, file.GetString("library"))
	if err != nil {
		return nil, stream.ErrGone
	}
	client, err := e.zurg()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", stream.ErrSource, err)
	}
	response, err := client.Open(ctx, lib.GetString("folder"), file.GetString("path"), header, head)
	switch {
	case errors.Is(err, zurg.ErrNotFound):
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
	Version   media.Version   `json:"version"`
	Versions  []media.Version `json:"versions"`
	Chapters  []media.Chapter `json:"chapters"`
}

// PlayRoutes mounts what a user's player asks for a title's stream by. A
// title the user may not see — a guess not yet confirmed — has none.
func (e *Engine) PlayRoutes(r *router.Router[*core.RequestEvent], streams *stream.Streamer) {
	r.POST("/api/foyer/play", func(re *core.RequestEvent) error {
		var body struct {
			Collection string `json:"collection"`
			ID         string `json:"id"`
			File       string `json:"file"`
		}
		if err := re.BindBody(&body); err != nil {
			return re.BadRequestError("The request could not be read.", err)
		}
		if !Managed(e.app, body.Collection) {
			return re.NotFoundError("", nil)
		}
		item, err := e.app.FindRecordById(body.Collection, body.ID)
		if err != nil {
			return re.NotFoundError("", nil)
		}
		info, err := re.RequestInfo()
		if err != nil {
			return err
		}
		if ok, _ := re.App.CanAccessRecord(item, info, item.Collection().ViewRule); !ok {
			return re.NotFoundError("", nil)
		}
		var versions []media.Version
		_ = item.UnmarshalJSONField("versions", &versions)
		if len(versions) == 0 {
			return re.NotFoundError("There is nothing to play here.", nil)
		}
		chosen := versions[0]
		if body.File != "" {
			found := false
			for _, v := range versions {
				if v.File == body.File {
					chosen, found = v, true
				}
			}
			if !found {
				return re.NotFoundError("That is none of this title's files.", nil)
			}
		}
		file, err := e.app.FindRecordById(FilesCollection, chosen.File)
		if err != nil || file.GetString("item_id") != item.Id || file.GetString("missing_since") != "" {
			return re.NotFoundError("The file is gone.", nil)
		}
		expires := time.Now().Add(stream.Lifetime).UTC().Truncate(time.Second)
		address, err := streams.Address(file.Id, re.Auth.Id, expires)
		if err != nil {
			return re.InternalServerError("", err)
		}
		playback := Playback{URL: address, ExpiresAt: expires, Version: chosen, Versions: versions, Chapters: []media.Chapter{}}
		if file.GetString("probe_state") == "done" {
			var probed media.Version
			if err := file.UnmarshalJSONField("probe", &probed); err == nil && probed.Chapters != nil {
				playback.Chapters = probed.Chapters
			}
		}
		return re.JSON(http.StatusOK, playback)
	}).Bind(apis.RequireAuth("users"))
}
