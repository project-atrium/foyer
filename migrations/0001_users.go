// Package migrations holds Foyer's own collections and settings. PocketBase
// runs them in the order of their file names, compared as text — so the
// numbers are zero-padded: `10_x.go` would run before `1_x.go`.
//
// Never edit one that has run on a server; fix forward with a new one. A
// library's collections are not here: Foyer makes those itself, one set per
// folder of the WebDAV share (internal/library).
package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// Sessions last 30 days.
const sessionSeconds = 30 * 24 * 60 * 60

// A user is a profile — Alex, Sarah, Kids — and one Foyer is one household.
// The household's superuser makes them in the dashboard: nobody registers
// through PocketBase's own route. Each signs in with a username and a
// password, and may keep a four-digit PIN: a child lock, readable by its own
// user, so a device that signs in as several users can ask for it.
func init() {
	m.Register(func(app core.App) error {
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		if name, ok := users.Fields.GetByName("name").(*core.TextField); ok {
			name.Required = true
			name.Max = 50
		}
		if avatar, ok := users.Fields.GetByName("avatar").(*core.FileField); ok {
			// No SVG: a picture that can carry a script has no business here.
			avatar.MimeTypes = []string{"image/jpeg", "image/png", "image/webp", "image/gif"}
			avatar.MaxSize = 5 << 20
			avatar.Thumbs = []string{"128x128", "256x256"}
		}
		users.Fields.Add(&core.TextField{
			Name:     "username",
			Required: true,
			Min:      3,
			Max:      50,
			Pattern:  `^[A-Za-z0-9][A-Za-z0-9._-]*$`,
		})
		users.Fields.Add(&core.TextField{Name: "pin", Pattern: `^[0-9]{4}$`})
		// NOCASE makes PocketBase's sign-in compare the name without regard to case, too.
		users.AddIndex("idx_users_username", true, "`username` COLLATE NOCASE", "")
		if email, ok := users.Fields.GetByName(core.FieldNameEmail).(*core.EmailField); ok {
			email.Required = false
		}

		users.PasswordAuth.Enabled = true
		users.PasswordAuth.IdentityFields = []string{"username"}
		users.OAuth2.Enabled = false
		users.OAuth2.MappedFields = core.OAuth2KnownFields{}
		users.OTP.Enabled = false
		users.MFA.Enabled = false
		// It mails about a sign-in from a new device, and Foyer sends no mail.
		users.AuthAlert.Enabled = false
		users.AuthToken.Duration = sessionSeconds

		self := types.Pointer("id = @request.auth.id")
		users.ListRule, users.ViewRule, users.UpdateRule = self, self, self
		users.CreateRule, users.DeleteRule = nil, nil

		return app.Save(users)
	}, nil)
}
