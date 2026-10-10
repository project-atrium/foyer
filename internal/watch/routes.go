package watch

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// resumeFrom is how far in a title must be to be resumed: less is a look.
const resumeFrom = 30_000

// Routes mounts what a user's player reports to, and reads from. isTitle
// says whether a collection is a library's.
func Routes(r *router.Router[*core.RequestEvent], isTitle func(core.App, string) bool) {
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
		now := time.Now().UTC()
		if err := update(re.App, re.Auth.Id, itemOf(re.App, item), func(s State) State { return Apply(s, body.Report, now) }); err != nil {
			return re.Error(http.StatusConflict, err.Error(), nil)
		}
		return re.NoContent(http.StatusNoContent)
	}).Bind(apis.RequireAuth("users"))

	r.POST("/api/foyer/watch/played", func(re *core.RequestEvent) error {
		var body struct {
			Collection string `json:"collection"`
			ID         string `json:"id"`
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

	r.GET("/api/foyer/watch/resume", func(re *core.RequestEvent) error {
		limit, _ := strconv.Atoi(re.Request.URL.Query().Get("limit"))
		limit = min(max(limit, 1), 100)
		if re.Request.URL.Query().Get("limit") == "" {
			limit = 20
		}
		states, err := re.App.FindRecordsByFilter(Collection, "user = {:user} && position_ms >= {:from}", "-last_played_at", limit*2, 0,
			dbx.Params{"user": re.Auth.Id, "from": resumeFrom})
		if err != nil {
			return err
		}
		info, err := re.RequestInfo()
		if err != nil {
			return err
		}
		items := []map[string]any{}
		for _, s := range states {
			if len(items) >= limit {
				break
			}
			collection := s.GetString("item_collection")
			if !isTitle(re.App, collection) {
				continue
			}
			item, err := re.App.FindRecordById(collection, s.GetString("item_id"))
			if err != nil {
				continue
			}
			if ok, _ := re.App.CanAccessRecord(item, info, item.Collection().ViewRule); !ok {
				continue
			}
			entry := map[string]any{
				"collection": collection, "id": item.Id, "identity": s.GetString("identity"), "title": item.GetString("title"),
				"positionMs": s.GetInt64("position_ms"), "durationMs": s.GetInt64("duration_ms"), "watched": s.GetBool("watched"),
				"lastPlayedAt": s.GetDateTime("last_played_at").Time(),
			}
			if show := item.GetString("show"); show != "" {
				if parent, err := re.App.FindRecordById(strings.TrimSuffix(collection, "_episodes"), show); err == nil {
					entry["showTitle"] = parent.GetString("title")
					entry["showId"] = parent.Id
				}
				entry["season"], entry["episode"] = item.GetInt("season"), item.GetInt("episode")
			}
			items = append(items, entry)
		}
		return re.JSON(http.StatusOK, map[string]any{"items": items})
	}).Bind(apis.RequireAuth("users"))
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
