package household

import (
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// Service is the household's routes and hooks.
type Service struct {
	app   core.App
	Codes *Codes
	// served says what this Foyer serves, for the household's answer; nil
	// until something is.
	served func(core.App) []map[string]any
}

func New(app core.App) *Service {
	return &Service{app: app, Codes: NewCodes()}
}

// Serving tells the household's answer what Foyer serves.
func (s *Service) Serving(fn func(core.App) []map[string]any) { s.served = fn }

// Bind remembers the device a user signs in on, or refreshes a session from,
// when it says which it is.
func (s *Service) Bind() {
	s.app.OnRecordAuthRequest("users").BindFunc(func(e *core.RecordAuthRequestEvent) error {
		if err := e.Next(); err != nil {
			return err
		}
		if d, ok := deviceOf(e.Request); ok && e.Record != nil {
			if err := remember(e.App, d, e.Record.Id, false, s.Codes.now()); err != nil {
				e.App.Logger().Warn("A device could not be remembered", "error", err.Error())
			}
		}
		return nil
	})
}

// Routes mounts the household's routes: a user's, the pairing route anyone
// with a code may call, and the superusers'.
func (s *Service) Routes(r *router.Router[*core.RequestEvent], admin *router.RouterGroup[*core.RequestEvent]) {
	// What a device may know of the household it signed in to: its members —
	// names and pictures, never a PIN — and what Foyer serves.
	r.GET("/api/foyer/household", func(e *core.RequestEvent) error {
		h, err := Get(e.App)
		if err != nil {
			return e.InternalServerError("", err)
		}
		members, err := Members(e.App)
		if err != nil {
			return e.InternalServerError("", err)
		}
		served := []map[string]any{}
		if s.served != nil {
			served = s.served(e.App)
		}
		return e.JSON(http.StatusOK, map[string]any{"household": h, "members": members, "served": served})
	}).Bind(apis.RequireAuth("users"))

	// A code the superuser made, traded for the sessions of the users it
	// names: no password crosses. Any code that will not do is the same 400,
	// and the route is rate-limited before /api/'s rule.
	r.POST("/api/foyer/pair", func(e *core.RequestEvent) error {
		var body struct {
			Code   string `json:"code"`
			Device Device `json:"device"`
		}
		if err := e.BindBody(&body); err != nil {
			return e.BadRequestError("The request could not be read.", err)
		}
		if !body.Device.valid() {
			return e.BadRequestError("The device did not say which it is.", nil)
		}
		ids, err := s.Codes.redeem(body.Code)
		if err != nil {
			return e.BadRequestError(sentence(err), nil)
		}
		// A user removed since the code was made is left out.
		users, err := e.App.FindRecordsByIds("users", ids)
		if err != nil || len(users) == 0 {
			return e.BadRequestError(sentence(ErrUnusable), nil)
		}
		inOrder(users, ids)
		h, err := Get(e.App)
		if err != nil {
			return e.InternalServerError("", err)
		}
		device := body.Device.clean()
		paired := make([]map[string]any, 0, len(users))
		for _, u := range users {
			token, err := u.NewAuthToken()
			if err != nil {
				return e.InternalServerError("", err)
			}
			if err := remember(e.App, device, u.Id, true, s.Codes.now()); err != nil {
				e.App.Logger().Warn("A device could not be remembered", "error", err.Error())
			}
			paired = append(paired, map[string]any{"member": memberOf(u), "pin": u.GetString("pin"), "token": token})
		}
		// No pairing route answer, a code or a token in it, is logged.
		return e.JSON(http.StatusOK, map[string]any{"household": h, "users": paired})
	}).Bind(apis.SkipSuccessActivityLog())

	admin.GET("/household", func(e *core.RequestEvent) error {
		h, err := Get(e.App)
		if err != nil {
			return e.InternalServerError("", err)
		}
		members, err := Members(e.App)
		if err != nil {
			return e.InternalServerError("", err)
		}
		devices, err := devicesView(e.App)
		if err != nil {
			return e.InternalServerError("", err)
		}
		return e.JSON(http.StatusOK, map[string]any{"household": h, "members": members, "devices": devices})
	})

	admin.PATCH("/household", func(e *core.RequestEvent) error {
		var body struct {
			Name string `json:"name"`
		}
		if err := e.BindBody(&body); err != nil {
			return e.BadRequestError("The household could not be read.", err)
		}
		records, err := e.App.FindRecordsByFilter(Collection, "", "created", 1, 0)
		if err != nil || len(records) == 0 {
			return e.InternalServerError("", err)
		}
		records[0].Set("name", strings.TrimSpace(body.Name))
		if err := e.App.Save(records[0]); err != nil {
			return e.BadRequestError("That name will not do.", err)
		}
		h, _ := Get(e.App)
		return e.JSON(http.StatusOK, h)
	})

	admin.POST("/pairings", func(e *core.RequestEvent) error {
		var body struct {
			Users []string `json:"users"`
		}
		if err := e.BindBody(&body); err != nil {
			return e.BadRequestError("The request could not be read.", err)
		}
		if _, err := usersByID(e.App, body.Users); err != nil {
			return e.BadRequestError(sentence(err), nil)
		}
		code, expires, err := s.Codes.Make(body.Users)
		if err != nil {
			return e.InternalServerError("", err)
		}
		return e.JSON(http.StatusOK, map[string]any{"code": code, "expiresAt": expires})
	}).Bind(apis.SkipSuccessActivityLog())

	// Forgetting a device takes it off the list. Its sessions end only when
	// its users' passwords change: PocketBase ends a user's sessions
	// everywhere, or nowhere.
	admin.DELETE("/devices/{id}", func(e *core.RequestEvent) error {
		record, err := e.App.FindRecordById(DevicesCollection, e.Request.PathValue("id"))
		if err != nil {
			return e.NotFoundError("", nil)
		}
		if err := e.App.Delete(record); err != nil {
			return e.InternalServerError("", err)
		}
		return e.NoContent(http.StatusNoContent)
	})
}

// sentence is an error as an answer says it.
func sentence(err error) string {
	s := err.Error()
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:] + "."
}
