package watch

import (
	"net/http"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"foyer/internal/identity"
)

// Heard is told of each report a player sent of a playing: where it got
// to, and its stop — and the request, which says what device sent it.
type Heard func(r *http.Request, session, user, kind string, positionMs, durationMs int64, paused bool)

// Routes mounts what a user's player reports to, and reads from. isTitle
// says whether a collection is a library's; heard hears each report.
func Routes(r *router.Router[*core.RequestEvent], isTitle func(core.App, string) bool, heard Heard) {
	resolve := func(re *core.RequestEvent, collection, id string) (*core.Record, error) {
		if !isTitle(re.App, collection) {
			return nil, re.NotFoundError("", nil)
		}
		item, err := re.App.FindRecordById(collection, id)
		if err != nil {
			return nil, re.NotFoundError("", nil)
		}
		info, err := re.RequestInfo()
		if err != nil {
			return nil, err
		}
		if ok, _ := re.App.CanAccessRecord(item, info, item.Collection().ViewRule); !ok {
			return nil, re.NotFoundError("", nil)
		}
		return item, nil
	}

	r.POST("/api/foyer/watch/progress", func(re *core.RequestEvent) error {
		var body struct {
			Collection string `json:"collection"`
			ID         string `json:"id"`
			// One of the title's extras, played by its file.
			File string `json:"file"`
			Report
		}
		if err := re.BindBody(&body); err != nil {
			return re.BadRequestError("The report could not be read.", err)
		}
		switch body.Kind {
		case "started", "progress", "stopped", "end":
		default:
			return re.BadRequestError("A report is started, progress, stopped or end.", nil)
		}
		if body.PositionMs < 0 || body.DurationMs < 0 {
			return re.BadRequestError("A position is never below 0.", nil)
		}
		item, err := resolve(re, body.Collection, body.ID)
		if err != nil {
			return err
		}
		watched, ok := itemOrExtra(re.App, item, body.File)
		if !ok {
			return re.NotFoundError("That is none of this title's extras.", nil)
		}
		now := time.Now().UTC()
		if err := update(re.App, re.Auth.Id, watched, func(s State) State { return Apply(s, body.Report, now) }); err != nil {
			return re.Error(http.StatusConflict, err.Error(), nil)
		}
		if heard != nil && body.Session != "" {
			heard(re.Request, body.Session, re.Auth.Id, body.Kind, body.PositionMs, body.DurationMs, body.Paused)
		}
		return re.NoContent(http.StatusNoContent)
	}).Bind(apis.RequireAuth("users"))

	r.POST("/api/foyer/watch/played", func(re *core.RequestEvent) error {
		var body struct {
			Collection string `json:"collection"`
			ID         string `json:"id"`
			File       string `json:"file"`
			Played     bool   `json:"played"`
		}
		if err := re.BindBody(&body); err != nil {
			return re.BadRequestError("The request could not be read.", err)
		}
		item, err := resolve(re, body.Collection, body.ID)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		mark := func(s State) State { return Mark(s, body.Played, now) }
		if body.File != "" {
			extra, ok := itemOrExtra(re.App, item, body.File)
			if !ok {
				return re.NotFoundError("That is none of this title's extras.", nil)
			}
			if err := update(re.App, re.Auth.Id, extra, mark); err != nil {
				return re.Error(http.StatusConflict, err.Error(), nil)
			}
			return re.NoContent(http.StatusNoContent)
		}
		// A series is marked by its episodes.
		if episodes, err := re.App.FindCollectionByNameOrId(body.Collection + "_episodes"); err == nil && item.GetString("show") == "" {
			list, err := re.App.FindAllRecords(episodes.Name, dbx.HashExp{"show": item.Id})
			if err != nil {
				return err
			}
			for _, ep := range list {
				if err := update(re.App, re.Auth.Id, itemOf(re.App, ep), mark); err != nil {
					return re.Error(http.StatusConflict, err.Error(), nil)
				}
			}
			return re.NoContent(http.StatusNoContent)
		}
		if err := update(re.App, re.Auth.Id, itemOf(re.App, item), mark); err != nil {
			return re.Error(http.StatusConflict, err.Error(), nil)
		}
		return re.NoContent(http.StatusNoContent)
	}).Bind(apis.RequireAuth("users"))
}

// itemOrExtra is what is watched: the title, or the one of its extras whose
// file is named — known by the title's identity and what the extra is. False
// for a file that is none of its extras.
func itemOrExtra(app core.App, r *core.Record, file string) (Item, bool) {
	if file == "" {
		return itemOf(app, r), true
	}
	var extras []struct {
		File  string `json:"file"`
		Path  string `json:"path"`
		Kind  string `json:"kind"`
		Title string `json:"title"`
	}
	_ = r.UnmarshalJSONField("extras", &extras)
	for _, x := range extras {
		if x.File == file {
			return Item{Collection: r.Collection().Name, ID: r.Id, Identity: identity.Extra(r.GetString("identity"), x.Kind, x.Title, x.Path)}, true
		}
	}
	return Item{}, false
}

// itemOf is a title as watch status knows it: its identity, and the
// catalogue ids — an episode's, its series'.
func itemOf(app core.App, r *core.Record) Item {
	item := Item{Collection: r.Collection().Name, ID: r.Id, Identity: r.GetString("identity")}
	source := r
	if show := r.GetString("show"); show != "" {
		if parent, err := app.FindRecordById(strings.TrimSuffix(item.Collection, "_episodes"), show); err == nil {
			source = parent
		}
	}
	ids := map[string]string{}
	if err := source.UnmarshalJSONField("external_ids", &ids); err == nil && len(ids) > 0 {
		item.ExternalIDs = ids
	}
	return item
}
