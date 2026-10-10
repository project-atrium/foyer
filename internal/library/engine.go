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

	validation "github.com/pocketbase/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"foyer/internal/adapters"
	"foyer/internal/adapters/tmdb"
	"foyer/internal/adapters/zurg"
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
}

func New(app core.App, registry *adapters.Registry) *Engine {
	return &Engine{
		app:       app,
		adapters:  registry,
		images:    &http.Client{Timeout: 30 * time.Second},
		queue:     make(chan request, 16),
		now:       time.Now,
		scanning:  map[string]bool{},
		locks:     map[string]*sync.Mutex{},
		vanishing: map[string]time.Time{},
	}
}

// errOff is an adapter the household has not switched on.
var errOff = errors.New("switched off on the Adapters page")

// zurg is a client for Zurg as the Adapters page has it set.
func (e *Engine) zurg() (*zurg.Client, error) {
	s, err := e.adapters.Settings(zurg.Key)
	if err != nil {
		return nil, err
	}
	if !s.Enabled {
		return nil, fmt.Errorf("Zurg is %w", errOff)
	}
	return zurg.New(s.String("url"), s.String("username"), s.String("password"))
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
	Folder     string `json:"folder"`
	Enabled    bool   `json:"enabled"`
	LastScanAt string `json:"lastScanAt,omitempty"`
	ScanError  string `json:"scanError,omitempty"`
}

func viewOf(r *core.Record) libraryView {
	v := libraryView{
		ID: r.Id, Name: r.GetString("name"), Kind: r.GetString("kind"), Folder: r.GetString("folder"),
		Enabled: r.GetBool("enabled"), ScanError: r.GetString("scan_error"),
	}
	if t := r.GetDateTime("last_scan_at"); !t.IsZero() {
		v.LastScanAt = t.Time().UTC().Format(time.RFC3339)
	}
	return v
}

// Routes mounts the dashboard's library routes on the superusers' group.
// Zurg's refusal is an answer, never a 401: the dashboard signs its
// superuser out on any.
func (e *Engine) Routes(admin *router.RouterGroup[*core.RequestEvent]) {
	e.hookRoute(admin)
	e.gridRoutes(admin)
	admin.GET("/zurg/folders", func(re *core.RequestEvent) error {
		libs, err := e.app.FindAllRecords(LibrariesCollection)
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
		client, err := e.zurg()
		if err == nil {
			ctx, cancel := context.WithTimeout(re.Request.Context(), 30*time.Second)
			defer cancel()
			var entries []zurg.Entry
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
			answer.Message = err.Error() + "."
		}
		// A library whose folder Zurg no longer lists is shown all the same.
		for _, v := range byFolder {
			answer.Folders = append(answer.Folders, folder{Folder: v.Folder, Library: &v})
		}
		slices.SortFunc(answer.Folders, func(a, b folder) int { return strings.Compare(a.Folder, b.Folder) })
		return re.JSON(http.StatusOK, answer)
	})

	admin.POST("/libraries", func(re *core.RequestEvent) error {
		var body struct {
			Folder  string `json:"folder"`
			Name    string `json:"name"`
			Kind    string `json:"kind"`
			Enabled bool   `json:"enabled"`
		}
		if err := re.BindBody(&body); err != nil {
			return re.BadRequestError("The library could not be read.", err)
		}
		lib, err := e.Add(body.Folder, body.Name, body.Kind, body.Enabled)
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

// Add makes a library of a Zurg folder, and its collections.
func (e *Engine) Add(folder, name, kind string, enabled bool) (*core.Record, error) {
	folder, name = strings.TrimSpace(folder), strings.TrimSpace(name)
	errs := validation.Errors{}
	if folder == "" || strings.Contains(folder, "/") {
		errs["folder"] = validation.NewError("validation_required", "A library is one of Zurg's folders.")
	} else if _, err := e.app.FindFirstRecordByData(LibrariesCollection, "folder", folder); err == nil {
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
