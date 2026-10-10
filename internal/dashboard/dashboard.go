// Package dashboard puts Foyer on PocketBase's dashboard: its icon in the
// browser's tab, on the sign-in page and in the header, and its pages —
// Library, and Settings → Adapters.
//
// PocketBase calls this a UI extension, and marks the API experimental. The
// server's tests hold the dashboard to every name ui/ uses, so an upgrade
// that renames one fails there rather than quietly breaking a page.
package dashboard

import (
	"embed"
	"io/fs"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

//go:embed ui
var files embed.FS

// Extension is served at /_/extensions/foyer/, with its main.js folded into
// /_/extensions.js.
func Extension() (core.UIExtension, error) {
	// PocketBase looks for main.js at the root of the extension: without Sub it
	// finds none, and skips the extension without a word.
	ui, err := fs.Sub(files, "ui")
	if err != nil {
		return core.UIExtension{}, err
	}
	return core.UIExtension{Name: "foyer", FS: ui}, nil
}

// Fresh has a browser ask for the extension's files every time. Outside
// --dev PocketBase lets it keep everything under /_/ for two weeks, so after
// an upgrade the dashboard would run the last version's pages against this
// version's routes. PocketBase sets its own header only where none is set.
func Fresh(e *core.RequestEvent) error {
	if p := e.Request.URL.Path; p == "/_/extensions.js" || strings.HasPrefix(p, "/_/extensions/") {
		e.Response.Header().Set("Cache-Control", "no-cache")
	}
	return e.Next()
}
