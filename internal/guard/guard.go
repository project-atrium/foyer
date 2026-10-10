// Package guard keeps Foyer's own collections Foyer's. Their records are
// written by Foyer's code alone, and their shape by its migrations and
// templates: a record or a field changed in the dashboard would be undone by
// the next scan at best, and at worst leave items pointing at files that are
// not theirs.
//
// The guard stops requests — from the dashboard, the record API, a batch, an
// import. Foyer's own code saves at the model level, which no request hook
// sees, so it is never in the way. A superuser's SQL console still reaches
// past it: this prevents accidents, not malice.
package guard

import (
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
)

// Managed says whether a collection is Foyer's to write. It is asked on rare
// write requests only.
type Managed func(app core.App, name string) bool

const refusal = "Foyer keeps this collection itself — use the dashboard's Library and Adapters pages."

// Bind refuses every request that would write a managed collection's records
// or change the collection itself, superusers' included. `users` keeps its
// records writable, but its rules and Foyer's fields on it stay as the
// migrations made them: a rule loosened there would let one profile read
// another.
func Bind(app core.App, managed Managed) {
	refuseRecords := func(e *core.RecordRequestEvent) error {
		if managed(e.App, e.Collection.Name) {
			return e.ForbiddenError(refusal, nil)
		}
		return e.Next()
	}
	app.OnRecordCreateRequest().BindFunc(refuseRecords)
	app.OnRecordUpdateRequest().BindFunc(refuseRecords)
	app.OnRecordDeleteRequest().BindFunc(refuseRecords)

	app.OnCollectionCreateRequest().BindFunc(func(e *core.CollectionRequestEvent) error {
		if managed(e.App, e.Collection.Name) {
			return e.ForbiddenError(refusal, nil)
		}
		return e.Next()
	})
	app.OnCollectionUpdateRequest().BindFunc(func(e *core.CollectionRequestEvent) error {
		original, err := e.App.FindCachedCollectionByNameOrId(e.Collection.Id)
		if err != nil {
			return e.Next()
		}
		if managed(e.App, original.Name) || managed(e.App, e.Collection.Name) {
			return e.ForbiddenError(refusal, nil)
		}
		if original.Name == "users" && usersChanged(original, e.Collection) {
			return e.ForbiddenError("A user is a profile: its rules and Foyer's fields on it stay as Foyer made them.", nil)
		}
		return e.Next()
	})
	app.OnCollectionDeleteRequest().BindFunc(func(e *core.CollectionRequestEvent) error {
		if managed(e.App, e.Collection.Name) || e.Collection.Name == "users" {
			return e.ForbiddenError(refusal, nil)
		}
		return e.Next()
	})
	app.OnCollectionsImportRequest().BindFunc(func(e *core.CollectionsImportRequestEvent) error {
		for _, raw := range e.CollectionsData {
			name, _ := raw["name"].(string)
			if managed(e.App, name) || name == "users" {
				return e.ForbiddenError(refusal, nil)
			}
		}
		if e.DeleteMissing {
			return e.ForbiddenError(refusal, nil)
		}
		return e.Next()
	})

	// Truncating a collection has no request hook of its own.
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		se.Router.Bind(&hook.Handler[*core.RequestEvent]{
			Id: "foyerGuardTruncate",
			Func: func(e *core.RequestEvent) error {
				if e.Request.Method == http.MethodDelete && strings.HasPrefix(e.Request.URL.Path, "/api/collections/") &&
					strings.HasSuffix(e.Request.URL.Path, "/truncate") {
					name := strings.TrimSuffix(strings.TrimPrefix(e.Request.URL.Path, "/api/collections/"), "/truncate")
					if c, err := e.App.FindCachedCollectionByNameOrId(name); err == nil && (managed(e.App, c.Name) || c.Name == "users") {
						return e.ForbiddenError(refusal, nil)
					}
				}
				return e.Next()
			},
		})
		return se.Next()
	})
}

// usersChanged reports a change to what makes a user a profile: any rule, or
// one of Foyer's fields removed or retyped.
func usersChanged(before, after *core.Collection) bool {
	rules := func(c *core.Collection) []*string {
		return []*string{c.ListRule, c.ViewRule, c.CreateRule, c.UpdateRule, c.DeleteRule, c.AuthRule, c.ManageRule}
	}
	a, b := rules(before), rules(after)
	for i := range a {
		if (a[i] == nil) != (b[i] == nil) || (a[i] != nil && *a[i] != *b[i]) {
			return true
		}
	}
	for _, name := range []string{"username", "name", "avatar", "pin"} {
		old, now := before.Fields.GetByName(name), after.Fields.GetByName(name)
		if old == nil {
			continue
		}
		if now == nil || now.Type() != old.Type() {
			return true
		}
	}
	return false
}
