package adapters

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// testTimeout bounds one test of a connection: the page waits for it.
const testTimeout = 30 * time.Second

// Routes mounts the Adapters page's routes on the superusers' group. A
// provider that refuses is a result, answered 200 — never 401, which the
// dashboard takes for its own session ending and signs the superuser out.
func (r *Registry) Routes(admin *router.RouterGroup[*core.RequestEvent]) {
	admin.GET("/adapters", func(e *core.RequestEvent) error {
		views, err := r.Views()
		if err != nil {
			return e.InternalServerError("", err)
		}
		return e.JSON(http.StatusOK, views)
	})

	// A single adapter — TMDB, FFmpeg — is saved by its key: it has one
	// connection, made with the first save.
	admin.PUT("/adapters/{key}", func(e *core.RequestEvent) error {
		a, ok := r.Adapter(e.Request.PathValue("key"))
		if !ok {
			return e.NotFoundError("", nil)
		}
		if !a.Single {
			return e.BadRequestError(a.Name+" is set up as connections of its own: add one.", nil)
		}
		var body struct {
			Enabled bool           `json:"enabled"`
			Values  map[string]any `json:"values"`
		}
		if err := e.BindBody(&body); err != nil {
			return e.BadRequestError("The settings could not be read.", err)
		}
		c, err := r.Save(a.Key, body.Enabled, body.Values)
		if err != nil {
			return refusal(e, err)
		}
		return answer(e, r, c.ID)
	})

	// A test of values not yet saved: a connection being made.
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
		result, err := r.Test(ctx, key, "", body.Values)
		if err != nil {
			return refusal(e, err)
		}
		return e.JSON(http.StatusOK, result)
	})

	admin.POST("/connections", func(e *core.RequestEvent) error {
		var body struct {
			Adapter string         `json:"adapter"`
			Name    string         `json:"name"`
			Enabled bool           `json:"enabled"`
			Values  map[string]any `json:"values"`
		}
		if err := e.BindBody(&body); err != nil {
			return e.BadRequestError("The connection could not be read.", err)
		}
		if _, ok := r.Adapter(body.Adapter); !ok {
			return e.NotFoundError("", nil)
		}
		c, err := r.Create(body.Adapter, body.Name, body.Enabled, body.Values)
		if err != nil {
			return refusal(e, err)
		}
		return answer(e, r, c.ID)
	})

	admin.PUT("/connections/{id}", func(e *core.RequestEvent) error {
		var body struct {
			Name    string         `json:"name"`
			Enabled bool           `json:"enabled"`
			Values  map[string]any `json:"values"`
		}
		if err := e.BindBody(&body); err != nil {
			return e.BadRequestError("The connection could not be read.", err)
		}
		c, err := r.Update(e.Request.PathValue("id"), body.Name, body.Enabled, body.Values)
		if err != nil {
			return refusal(e, err)
		}
		return answer(e, r, c.ID)
	})

	admin.DELETE("/connections/{id}", func(e *core.RequestEvent) error {
		err := r.Delete(e.Request.PathValue("id"))
		switch {
		case err == nil:
			return e.NoContent(http.StatusNoContent)
		case errors.Is(err, ErrNoConnection):
			return e.NotFoundError("", nil)
		default:
			return e.Error(http.StatusConflict, err.Error(), nil)
		}
	})

	admin.POST("/connections/{id}/test", func(e *core.RequestEvent) error {
		var body struct {
			Values map[string]any `json:"values"`
		}
		if err := e.BindBody(&body); err != nil {
			return e.BadRequestError("The settings could not be read.", err)
		}
		ctx, cancel := context.WithTimeout(e.Request.Context(), testTimeout)
		defer cancel()
		result, err := r.Test(ctx, "", e.Request.PathValue("id"), body.Values)
		if err != nil {
			return refusal(e, err)
		}
		return e.JSON(http.StatusOK, result)
	})
}

func answer(e *core.RequestEvent, r *Registry, id string) error {
	view, err := r.View(id)
	if err != nil {
		return e.InternalServerError("", err)
	}
	return e.JSON(http.StatusOK, view)
}

// refusal answers a connection that will not do: field errors name their
// fields; a connection that is gone is not found.
func refusal(e *core.RequestEvent, err error) error {
	switch {
	case errors.Is(err, ErrNoConnection):
		return e.NotFoundError("", nil)
	case errors.Is(err, ErrPlanned), errors.Is(err, ErrSingle):
		return e.BadRequestError(sentence(err), nil)
	default:
		return e.BadRequestError("The settings will not do.", err)
	}
}

// sentence is an error as the dashboard shows it.
func sentence(err error) string {
	s := err.Error()
	if s == "" {
		return s
	}
	return s + "."
}
