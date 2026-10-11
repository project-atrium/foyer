package library

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/dbx"
	validation "github.com/pocketbase/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"foyer/internal/adapters"
	"foyer/internal/adapters/tmdb"
	"foyer/internal/adapters/webdav"
)

// Engine is the library: its collections, its scans, and the routes the
// dashboard reaches them by.
type Engine struct {
	app      core.App
	adapters *adapters.Registry
	status   status
	images   *http.Client
	queue    chan request
	now      func() time.Time

	mu          sync.Mutex
	scanning    map[string]bool
	locks       map[string]*sync.Mutex
	vanishing   map[string]time.Time
	tmdbClient  *tmdb.Client
	tmdbKey     string
	tmdbOptions []tmdb.Option
	// shares are a client per WebDAV connection, kept while its settings
	// stand: a client holds the share's transport, its pace and its refusal.
	shares map[string]share
	// dirty are series whose episodes a probe changed: collection and id,
	// with their library.
	dirty map[[2]string]string
	// scanned is told when a scan ends: there may be new files to probe.
	scanned func()
	// moved gives watch status from one identity to another.
	moved func(from, to string) error
}

// OnIdentityMoved calls fn where a title's identity changed — TMDB named it
// at last, or a superuser did — so what was watched under the old one
// follows.
func (e *Engine) OnIdentityMoved(fn func(from, to string) error) { e.moved = fn }

// moveIdentities hands each old identity's watch status to the new one,
// unless a title still goes by the old.
func (e *Engine) moveIdentities(pairs map[string]string) {
	if e.moved == nil {
		return
	}
	for from, to := range pairs {
		if from == "" || to == "" || from == to || e.identityInUse(from) {
			continue
		}
		if err := e.moved(from, to); err != nil {
			e.app.Logger().Warn("Watch status could not follow a title", "error", err.Error())
		}
	}
}

// identityInUse says whether any title of any library goes by an identity.
func (e *Engine) identityInUse(identity string) bool {
	libs, err := e.app.FindAllRecords(LibrariesCollection)
	if err != nil {
		return true
	}
	for _, lib := range libs {
		names := []string{lib.GetString("name")}
		if lib.GetString("kind") == Shows {
			names = append(names, Episodes(lib.GetString("name")))
		}
		for _, name := range names {
			if _, err := e.app.FindFirstRecordByFilter(name, "identity = {:identity}", dbx.Params{"identity": identity}); err == nil {
				return true
			}
		}
	}
	return false
}

// OnScanned calls fn after every scan.
func (e *Engine) OnScanned(fn func()) { e.scanned = fn }

func New(app core.App, registry *adapters.Registry) *Engine {
	e := &Engine{
		app:       app,
		adapters:  registry,
		images:    &http.Client{Timeout: 30 * time.Second},
		queue:     make(chan request, 16),
		now:       time.Now,
		scanning:  map[string]bool{},
		locks:     map[string]*sync.Mutex{},
		vanishing: map[string]time.Time{},
		shares:    map[string]share{},
	}
	registry.OnChange(func(c adapters.Connection) {
		e.mu.Lock()
		delete(e.shares, c.ID)
		e.mu.Unlock()
	})
	// A share whose folders are libraries stays: their collections are
	// Foyer's to remove, one library at a time.
	registry.OnDelete(func(c adapters.Connection) error {
		n, err := app.CountRecords(LibrariesCollection, dbx.HashExp{"connection": c.ID})
		if err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%d of its folders are libraries: remove them first", n)
		}
		return nil
	})
	return e
}

// errOff is a connection the household has not switched on.
var errOff = errors.New("switched off on the Adapters page")

// errNoShare is a library no share is set for.
var errNoShare = errors.New("this library is a folder of no WebDAV share")

type share struct {
	settings string
	client   *webdav.Client
}

// shareOf is a client for the WebDAV share a library is a folder of.
func (e *Engine) shareOf(lib *core.Record) (*webdav.Client, error) {
	return e.shareFor(lib.GetString("connection"))
}

// shareFor is a client for a WebDAV connection as the Adapters page has it
// set: the one made before, while its settings stand.
func (e *Engine) shareFor(id string) (*webdav.Client, error) {
	if id == "" {
		return nil, errNoShare
	}
	c, err := e.adapters.Connection(id)
	if err != nil {
		if errors.Is(err, adapters.ErrNoConnection) {
			return nil, errNoShare
		}
		return nil, err
	}
	if c.Adapter != webdav.Key {
		return nil, errNoShare
	}
	if !c.Enabled {
		return nil, fmt.Errorf("%s is %w", c.Name, errOff)
	}
	settings := c.String("url") + "\x00" + c.String("username") + "\x00" + c.String("password")
	e.mu.Lock()
	defer e.mu.Unlock()
	if cached, ok := e.shares[id]; ok && cached.settings == settings {
		return cached.client, nil
	}
	client, err := webdav.New(c.String("url"), c.String("username"), c.String("password"))
	if err != nil {
		return nil, err
	}
	e.shares[id] = share{settings: settings, client: client}
	return client, nil
}

// EnsureAll brings every library's collections to the template, at start.
// A library that cannot be is left as it was, and says why.
func (e *Engine) EnsureAll() {
	libs, err := e.app.FindAllRecords(LibrariesCollection)
	if err != nil {
		e.app.Logger().Error("The libraries could not be read", "error", err)
		return
	}
	for _, lib := range libs {
		if lib.GetInt("template") >= TemplateVersion {
			continue
		}
		if err := Ensure(e.app, lib); err != nil {
			e.app.Logger().Error("A library's collections could not be brought up to date", "library", lib.GetString("name"), "error", err)
			lib.Set("scan_error", "Its collections could not be brought up to date: "+err.Error())
			_ = e.app.Save(lib)
		}
	}
}

// libraryView is a library as the dashboard shows it.
type libraryView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Connection string `json:"connection"`
	Folder     string `json:"folder"`
	Enabled    bool   `json:"enabled"`
	LastScanAt string `json:"lastScanAt,omitempty"`
	ScanError  string `json:"scanError,omitempty"`
}

func viewOf(r *core.Record) libraryView {
	v := libraryView{
		ID: r.Id, Name: r.GetString("name"), Kind: r.GetString("kind"), Connection: r.GetString("connection"),
		Folder: r.GetString("folder"), Enabled: r.GetBool("enabled"), ScanError: r.GetString("scan_error"),
	}
	if t := r.GetDateTime("last_scan_at"); !t.IsZero() {
		v.LastScanAt = t.Time().UTC().Format(time.RFC3339)
	}
	return v
}

// Routes mounts the dashboard's library routes on the superusers' group.
// The share's refusal is an answer, never a 401: the dashboard signs its
// superuser out on any.
func (e *Engine) Routes(admin *router.RouterGroup[*core.RequestEvent]) {
	e.hookRoute(admin)
	e.gridRoutes(admin)
	// A share's folders, each with the library it is, and libraries whose
	// folder the share no longer lists.
	admin.GET("/connections/{id}/folders", func(re *core.RequestEvent) error {
		id := re.Request.PathValue("id")
		if c, err := e.adapters.Connection(id); err != nil || c.Adapter != webdav.Key {
			return re.NotFoundError("", nil)
		}
		libs, err := e.app.FindAllRecords(LibrariesCollection, dbx.HashExp{"connection": id})
		if err != nil {
			return re.InternalServerError("", err)
		}
		byFolder := map[string]libraryView{}
		for _, lib := range libs {
			byFolder[lib.GetString("folder")] = viewOf(lib)
		}
		type folder struct {
			Folder  string       `json:"folder"`
			Library *libraryView `json:"library,omitempty"`
		}
		answer := struct {
			OK      bool     `json:"ok"`
			Message string   `json:"message,omitempty"`
			Folders []folder `json:"folders"`
		}{Folders: []folder{}}
		client, err := e.shareFor(id)
		if err == nil {
			ctx, cancel := context.WithTimeout(re.Request.Context(), 30*time.Second)
			defer cancel()
			var entries []webdav.Entry
			if entries, err = client.List(ctx, ""); err == nil {
				answer.OK = true
				for _, entry := range entries {
					if !entry.Dir {
						continue
					}
					f := folder{Folder: entry.Name}
					if v, ok := byFolder[entry.Name]; ok {
						f.Library = &v
						delete(byFolder, entry.Name)
					}
					answer.Folders = append(answer.Folders, f)
				}
			}
		}
		if err != nil {
			answer.Message = sentence(err)
		}
		// A library whose folder the share no longer lists is shown all the
		// same.
		for _, v := range byFolder {
			answer.Folders = append(answer.Folders, folder{Folder: v.Folder, Library: &v})
		}
		slices.SortFunc(answer.Folders, func(a, b folder) int { return strings.Compare(a.Folder, b.Folder) })
		return re.JSON(http.StatusOK, answer)
	})

	admin.POST("/libraries", func(re *core.RequestEvent) error {
		var body struct {
			Connection string `json:"connection"`
			Folder     string `json:"folder"`
			Name       string `json:"name"`
			Kind       string `json:"kind"`
			Enabled    bool   `json:"enabled"`
		}
		if err := re.BindBody(&body); err != nil {
			return re.BadRequestError("The library could not be read.", err)
		}
		lib, err := e.Add(body.Connection, body.Folder, body.Name, body.Kind, body.Enabled)
		if err != nil {
			return re.BadRequestError("The library will not do.", err)
		}
		return re.JSON(http.StatusOK, viewOf(lib))
	})

	admin.PATCH("/libraries/{id}", func(re *core.RequestEvent) error {
		lib, err := e.app.FindRecordById(LibrariesCollection, re.Request.PathValue("id"))
		if err != nil {
			return re.NotFoundError("", nil)
		}
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := re.BindBody(&body); err != nil {
			return re.BadRequestError("The library could not be read.", err)
		}
		lib.Set("enabled", body.Enabled)
		if err := e.app.Save(lib); err != nil {
			return re.BadRequestError("The library will not do.", err)
		}
		return re.JSON(http.StatusOK, viewOf(lib))
	})

	admin.DELETE("/libraries/{id}", func(re *core.RequestEvent) error {
		lib, err := e.app.FindRecordById(LibrariesCollection, re.Request.PathValue("id"))
		if err != nil {
			return re.NotFoundError("", nil)
		}
		if err := Remove(e.app, lib); err != nil {
			return re.Error(http.StatusConflict, "The library could not be removed: "+err.Error(), nil)
		}
		return re.NoContent(http.StatusNoContent)
	})
}

// Add makes a library of one of a share's folders, and its collections. A
// household with one share need not name it.
func (e *Engine) Add(connection, folder, name, kind string, enabled bool) (*core.Record, error) {
	folder, name = strings.TrimSpace(folder), strings.TrimSpace(name)
	errs := validation.Errors{}
	if connection == "" {
		if shares, err := e.adapters.Connections(webdav.Key); err == nil && len(shares) == 1 {
			connection = shares[0].ID
		}
	}
	if c, err := e.adapters.Connection(connection); err != nil || c.Adapter != webdav.Key {
		errs["connection"] = validation.NewError("validation_required", "A library is a folder of one of the WebDAV shares.")
	}
	if folder == "" || strings.Contains(folder, "/") {
		errs["folder"] = validation.NewError("validation_required", "A library is one of the share's folders.")
	} else if _, err := e.app.FindFirstRecordByFilter(LibrariesCollection, "connection = {:connection} && folder = {:folder}",
		dbx.Params{"connection": connection, "folder": folder}); err == nil {
		errs["folder"] = validation.NewError("foyer_taken", "That folder is a library already.")
	}
	if kind != Movies && kind != Shows {
		errs["kind"] = validation.NewError("validation_invalid_value", "A library holds movies or shows.")
	}
	if err := CheckName(e.app, name, kind, ""); err != nil {
		var fields validation.Errors
		if errors.As(err, &fields) {
			for k, v := range fields {
				errs[k] = v
			}
		} else {
			return nil, err
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	collection, err := e.app.FindCollectionByNameOrId(LibrariesCollection)
	if err != nil {
		return nil, err
	}
	lib := core.NewRecord(collection)
	lib.Set("connection", connection)
	lib.Set("folder", folder)
	lib.Set("name", name)
	lib.Set("kind", kind)
	lib.Set("enabled", enabled)
	if err := e.app.Save(lib); err != nil {
		return nil, err
	}
	if err := Ensure(e.app, lib); err != nil {
		_ = e.app.Delete(lib)
		return nil, err
	}
	return lib, nil
}

// sentence is an error as the dashboard shows it: a sentence of its own.
func sentence(err error) string {
	s := err.Error()
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:] + "."
}
