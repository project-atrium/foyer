package trakt

import (
	"errors"
	"net/http"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// Routes mounts what a user's device connects them to Trakt by — the code,
// how it runs, letting go — and the dashboard's view of every user's. Never
// a 401 for Trakt's refusal: the app takes any 401 for its own session
// ending.
func (s *Service) Routes(r *router.Router[*core.RequestEvent], admin *router.RouterGroup[*core.RequestEvent]) {
	r.GET("/api/foyer/me/integrations", func(e *core.RequestEvent) error {
		return e.JSON(http.StatusOK, map[string]any{"integrations": []View{s.ViewOf(e.Auth.Id)}})
	}).Bind(apis.RequireAuth("users"))

	r.POST("/api/foyer/me/integrations/{key}/connect", func(e *core.RequestEvent) error {
		if e.Request.PathValue("key") != Key {
			return e.NotFoundError("", nil)
		}
		v, err := s.Connect(e.Request.Context(), e.Auth.Id)
		switch {
		case errors.Is(err, ErrNotSetUp):
			return e.Error(http.StatusConflict, "Trakt is not set up on this Foyer: its superuser sets it up in Settings → Adapters.", nil)
		case err != nil:
			return e.Error(http.StatusBadGateway, err.Error(), nil)
		}
		return e.JSON(http.StatusOK, v)
	}).Bind(apis.RequireAuth("users"))

	r.PATCH("/api/foyer/me/integrations/{key}", func(e *core.RequestEvent) error {
		if e.Request.PathValue("key") != Key {
			return e.NotFoundError("", nil)
		}
		var body struct {
			Mode string `json:"mode"`
		}
		if err := e.BindBody(&body); err != nil {
			return e.BadRequestError("The request could not be read.", err)
		}
		if _, err := s.record(e.Auth.Id); err != nil {
			return e.NotFoundError("Trakt is not connected.", nil)
		}
		v, err := s.SetMode(e.Auth.Id, body.Mode)
		if err != nil {
			return e.BadRequestError(err.Error(), nil)
		}
		return e.JSON(http.StatusOK, v)
	}).Bind(apis.RequireAuth("users"))

	r.DELETE("/api/foyer/me/integrations/{key}", func(e *core.RequestEvent) error {
		if e.Request.PathValue("key") != Key {
			return e.NotFoundError("", nil)
		}
		if err := s.Disconnect(e.Request.Context(), e.Auth.Id); err != nil {
			return e.InternalServerError("", err)
		}
		return e.NoContent(http.StatusNoContent)
	}).Bind(apis.RequireAuth("users"))

	admin.GET("/integrations", func(e *core.RequestEvent) error {
		of, err := s.Of()
		if err != nil {
			return e.InternalServerError("", err)
		}
		return e.JSON(http.StatusOK, map[string]any{"integrations": of})
	})

	admin.DELETE("/integrations/{user}/{key}", func(e *core.RequestEvent) error {
		if e.Request.PathValue("key") != Key {
			return e.NotFoundError("", nil)
		}
		if err := s.Disconnect(e.Request.Context(), e.Request.PathValue("user")); err != nil {
			return e.InternalServerError("", err)
		}
		return e.NoContent(http.StatusNoContent)
	})
}
