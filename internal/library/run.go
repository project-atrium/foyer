package library

import (
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/pocketbase/pocketbase/tools/security"

	"foyer/internal/adapters/tmdb"
	"foyer/internal/adapters/webdav"
)

const (
	// defaultInterval is how often a library is looked at, in minutes, where
	// the WebDAV adapter says nothing.
	defaultInterval = 15
	// fullEvery is how often every entry is walked again, to catch files
	// that changed inside one.
	fullEvery = 24 * time.Hour
	// hookSecret is the token the share's webhook sends: Zurg's
	// on_library_update, or any script.
	hookSecret = "webhook"
)

// tmdb is a client for TMDB as the Adapters page has it set, and the
// language asked for: nil when it is off or has no key. One client per key,
// so a refused key stays refused and the pace holds across scans.
func (e *Engine) tmdb() (*tmdb.Client, string) {
	s, err := e.adapters.Settings(tmdb.Key)
	if err != nil || !s.Enabled || strings.TrimSpace(s.String("key")) == "" {
		return nil, ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tmdbClient == nil || e.tmdbKey != s.String("key") {
		e.tmdbClient, e.tmdbKey = tmdb.New(s.String("key"), e.tmdbOptions...), s.String("key")
	}
	return e.tmdbClient, s.String("language")
}

// UseTMDB points TMDB somewhere else: a test's fake.
func (e *Engine) UseTMDB(options ...tmdb.Option) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tmdbOptions, e.tmdbClient = options, nil
}

// SetClock sets what the library takes for now: a test's clock.
func (e *Engine) SetClock(now func() time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = now
}

type request struct {
	library string
	full    bool
}

// Trigger asks for a library's scan — all enabled libraries' for "" —
// without waiting: Run takes it in turn, and a scan already asked for is
// asked for once.
func (e *Engine) Trigger(library string, full bool) {
	select {
	case e.queue <- request{library: library, full: full}:
	default:
	}
}

// Run scans as asked and as due — each library at the WebDAV adapter's
// interval, walking everything once a day — until ctx ends. One scan at a
// time: SQLite has one writer, and a household's watch status must not
// wait behind two.
func (e *Engine) Run(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	e.due(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.due(ctx)
		case r := <-e.queue:
			e.scanAsked(ctx, r)
		}
	}
}

func (e *Engine) enabled() []*core.Record {
	libs, err := e.app.FindAllRecords(LibrariesCollection)
	if err != nil {
		return nil
	}
	return slices.DeleteFunc(libs, func(l *core.Record) bool { return !l.GetBool("enabled") })
}

func (e *Engine) scanAsked(ctx context.Context, r request) {
	for _, lib := range e.enabled() {
		if r.library == "" || r.library == lib.Id {
			e.scanLogged(ctx, lib, r.full)
		}
	}
}

func (e *Engine) due(ctx context.Context) {
	now := e.now()
	for _, lib := range e.enabled() {
		last := lib.GetDateTime("last_scan_at").Time()
		if !last.IsZero() && now.Sub(last) < time.Duration(e.intervalOf(lib))*time.Minute {
			continue
		}
		full := now.Sub(lib.GetDateTime("last_full_scan_at").Time()) >= fullEvery
		e.scanLogged(ctx, lib, full)
	}
}

func (e *Engine) scanLogged(ctx context.Context, lib *core.Record, full bool) {
	if err := e.Scan(ctx, lib.Id, full); err != nil && !errors.Is(err, ErrScanning) && ctx.Err() == nil {
		e.app.Logger().Warn("A library's scan failed", "library", lib.GetString("name"), "error", err.Error())
	}
}

// intervalOf is how often a library's share is looked at, in minutes.
func (e *Engine) intervalOf(lib *core.Record) int {
	if c, err := e.adapters.Connection(lib.GetString("connection")); err == nil && c.Int("interval") > 0 {
		return c.Int("interval")
	}
	return defaultInterval
}

// hookToken is the token a share's webhook sends, made once per share.
func (e *Engine) hookToken(connection string) (string, error) {
	token, err := e.adapters.Secret(connection, hookSecret)
	if err != nil || token != "" {
		return token, err
	}
	token = security.RandomString(32)
	return token, e.adapters.SetSecret(connection, hookSecret, token)
}

// caller is the share whose webhook token came with a call: the one route
// serves every share, and the token says which.
func (e *Engine) caller(got string) (string, bool) {
	shares, err := e.adapters.Connections(webdav.Key)
	if err != nil || got == "" {
		return "", false
	}
	for _, c := range shares {
		want, err := e.adapters.Secret(c.ID, hookSecret)
		if err == nil && want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
			return c.ID, true
		}
	}
	return "", false
}

// HookRoutes mounts the route a share's webhook calls — Zurg's
// on_library_update, or any script that posts the folders changed: a scan
// of the libraries whose folders changed, at once instead of at the next
// interval.
// The token goes in a header — in the address, PocketBase's request log
// would keep it.
func (e *Engine) HookRoutes(r *router.Router[*core.RequestEvent]) {
	r.POST("/api/foyer/webdav/updated", func(re *core.RequestEvent) error {
		got, _ := strings.CutPrefix(re.Request.Header.Get("Authorization"), "Bearer ")
		connection, ok := e.caller(got)
		if !ok {
			return re.ForbiddenError("", nil)
		}
		// One updated folder per line, as the share names them.
		changed := map[string]bool{}
		scanner := bufio.NewScanner(http.MaxBytesReader(re.Response, re.Request.Body, 1<<20))
		for scanner.Scan() {
			first, _, _ := strings.Cut(strings.Trim(strings.TrimSpace(scanner.Text()), "/"), "/")
			if first != "" {
				changed[first] = true
			}
		}
		var mine []*core.Record
		for _, lib := range e.enabled() {
			if lib.GetString("connection") == connection {
				mine = append(mine, lib)
			}
		}
		matched := false
		for _, lib := range mine {
			if changed[lib.GetString("folder")] {
				e.Trigger(lib.Id, false)
				matched = true
			}
		}
		if !matched {
			for _, lib := range mine {
				e.Trigger(lib.Id, false)
			}
		}
		return re.NoContent(http.StatusAccepted)
	}).Bind(apis.SkipSuccessActivityLog())
}

// hookRoute is the superusers' route that shows a share's token, and the
// line to put in Zurg's config, or any script's.
func (e *Engine) hookRoute(admin *router.RouterGroup[*core.RequestEvent]) {
	admin.GET("/connections/{id}/hook", func(re *core.RequestEvent) error {
		id := re.Request.PathValue("id")
		if c, err := e.adapters.Connection(id); err != nil || c.Adapter != webdav.Key {
			return re.NotFoundError("", nil)
		}
		token, err := e.hookToken(id)
		if err != nil {
			return re.InternalServerError("", err)
		}
		scheme := "http"
		if re.Request.TLS != nil {
			scheme = "https"
		}
		address := scheme + "://" + re.Request.Host + "/api/foyer/webdav/updated"
		return re.JSON(http.StatusOK, map[string]string{
			"token": token,
			"command": `printf '%s\n' "$@" | curl -fsS -m 10 -X POST -H 'Authorization: Bearer ` + token +
				`' --data-binary @- ` + address,
		})
	})
}
