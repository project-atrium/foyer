package sessions

import (
	"net/http"
	"strconv"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// Routes mounts Live Activity's: what plays now, stopping a playing, and
// what played before.
func (r *Registry) Routes(admin *router.RouterGroup[*core.RequestEvent]) {
	// Polled every two seconds by an open dashboard: never in the request log.
	admin.GET("/sessions", func(re *core.RequestEvent) error {
		return re.JSON(http.StatusOK, map[string]any{"sessions": r.List()})
	}).Bind(apis.SkipSuccessActivityLog())

	admin.POST("/sessions/{id}/stop", func(re *core.RequestEvent) error {
		if !r.Stop(re.Request.PathValue("id")) {
			return re.NotFoundError("Nothing plays under that id.", nil)
		}
		return re.NoContent(http.StatusNoContent)
	})

	admin.GET("/sessions/history", func(re *core.RequestEvent) error {
		q := re.Request.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		perPage, _ := strconv.Atoi(q.Get("perPage"))
		page, perPage = max(page, 1), min(max(perPage, 1), 100)
		if q.Get("perPage") == "" {
			perPage = 30
		}
		records, err := re.App.FindRecordsByFilter(Collection, "", "-ended", perPage, (page-1)*perPage)
		if err != nil {
			return re.InternalServerError("", err)
		}
		if errs := re.App.ExpandRecords(records, []string{"user"}, nil); len(errs) > 0 {
			re.App.Logger().Warn("A playing's user could not be read", "errors", len(errs))
		}
		items := make([]map[string]any, 0, len(records))
		for _, record := range records {
			user := map[string]any{"id": record.GetString("user")}
			if u := record.ExpandedOne("user"); u != nil {
				user["name"] = u.GetString("name")
			}
			items = append(items, map[string]any{
				"id": record.Id, "session": record.GetString("session"), "user": user,
				"device": Device{Name: record.GetString("device_name"), Platform: record.GetString("device_platform"), AppVersion: record.GetString("app_version")},
				"kind":   record.GetString("kind"), "mode": record.GetString("mode"), "client": record.GetString("client"),
				"item":    Item{Collection: record.GetString("item_collection"), ID: record.GetString("item_id"), Title: record.GetString("title"), Subtitle: record.GetString("subtitle")},
				"started": record.GetDateTime("started"), "ended": record.GetDateTime("ended"),
				"positionMs": record.GetInt("position_ms"), "durationMs": record.GetInt("duration_ms"), "bytes": record.GetInt("bytes"),
				"revoked": record.GetBool("revoked"),
			})
		}
		return re.JSON(http.StatusOK, map[string]any{"page": page, "perPage": perPage, "items": items})
	}).Bind(apis.SkipSuccessActivityLog())
}
