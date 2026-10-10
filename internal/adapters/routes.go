package adapters

import (
	"context"
	"net/http"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// testTimeout bounds one test of an adapter: the page waits for it.
const testTimeout = 30 * time.Second

// Routes mounts the Adapters page's routes on the superusers' group. A
// provider that refuses is a result, answered 200 — never 401, which the
// dashboard takes for its own session ending and signs the superuser out.
func (r *Registry) Routes(admin *router.RouterGroup[*core.RequestEvent]) {
	admin.GET("/adapters", func(e *core.RequestEvent) error {
		views := make([]View, 0, len(r.adapters))
		for _, a := range r.adapters {
			view, err := r.View(a.Key)
			if err != nil {
				return e.InternalServerError("", err)
			}
			views = append(views, view)
		}
		return e.JSON(http.StatusOK, views)
	})

	admin.PUT("/adapters/{key}", func(e *core.RequestEvent) error {
		key := e.Request.PathValue("key")
		if _, ok := r.Adapter(key); !ok {
			return e.NotFoundError("", nil)
		}
		var body struct {
			Enabled bool           `json:"enabled"`
			Values  map[string]any `json:"values"`
		}
		if err := e.BindBody(&body); err != nil {
			return e.BadRequestError("The settings could not be read.", err)
		}
		if _, err := r.Save(key, body.Enabled, body.Values); err != nil {
			return e.BadRequestError("The settings will not do.", err)
		}
		view, err := r.View(key)
		if err != nil {
			return e.InternalServerError("", err)
		}
		return e.JSON(http.StatusOK, view)
	})

	admin.POST("/adapters/{key}/test", func(e *core.RequestEvent) error {
		key := e.Request.PathValue("key")
		if _, ok := r.Adapter(key); !ok {
			return e.NotFoundError("", nil)
		}
		var body struct {
			Values map[string]any `json:"values"`
		}
		if err := e.BindBody(&body); err != nil {
			return e.BadRequestError("The settings could not be read.", err)
		}
		ctx, cancel := context.WithTimeout(e.Request.Context(), testTimeout)
		defer cancel()
		result, err := r.Test(ctx, key, body.Values)
		if err != nil {
			return e.BadRequestError("The settings will not do.", err)
		}
		return e.JSON(http.StatusOK, result)
	})
}
