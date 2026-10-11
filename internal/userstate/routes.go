package userstate

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// maxPush is the most records a push takes: a device sends its journal in
// batches well under it.
const maxPush = 1_000

// pageSize is how many records a page of changes holds.
const pageSize = 500

// Routes mounts a user's own state: what their devices push, and read back.
func Routes(r *router.Router[*core.RequestEvent]) {
	users := apis.RequireAuth("users")

	r.POST("/api/foyer/me/push", func(re *core.RequestEvent) error {
		var body struct {
			Records []json.RawMessage `json:"records"`
		}
		if err := re.BindBody(&body); err != nil {
			return re.BadRequestError("The records could not be read.", err)
		}
		if len(body.Records) > maxPush {
			return re.BadRequestError("Too many records at once.", nil)
		}
		seq, refused, err := Push(re.App, re.Auth.Id, body.Records)
		if err != nil {
			return re.InternalServerError("", err)
		}
		if refused != nil {
			return re.JSON(http.StatusUnprocessableEntity, map[string]any{"message": "A record was refused, and nothing was stored.", "refused": refused})
		}
		return re.JSON(http.StatusOK, map[string]any{"seq": seq})
	}).Bind(users)

	r.GET("/api/foyer/me/changes", func(re *core.RequestEvent) error {
		after, err := strconv.ParseInt(re.Request.URL.Query().Get("after"), 10, 64)
		if err != nil || after < 0 {
			return re.BadRequestError("after is a seq: a whole number from 0.", nil)
		}
		page, err := Changes(re.App, re.Auth.Id, after, pageSize)
		if err != nil {
			return re.InternalServerError("", err)
		}
		return re.JSON(http.StatusOK, page)
	}).Bind(users)
}
