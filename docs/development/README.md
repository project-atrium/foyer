# Development

```bash
go run . serve                    # the server, on 127.0.0.1:8090; data in ./pb_data
go run . superuser upsert you@example.com 'a long password'
go test ./...                     # every Go test
go vet ./... && gofmt -l .        # nothing to report
go build -o foyer .               # one static binary
```

## Go and PocketBase

- **PocketBase is pinned** in `go.mod`, to v0.40.x. It is pre-1.0, and its
  minor versions break its Go API: update deliberately, one version at a time,
  reading its release notes, and let the Go tests find what moved.
- **v0.40 needs Go 1.27.** A Go 1.26 install is enough to start: with
  `GOTOOLCHAIN=auto`, the default, the go command fetches the toolchain
  `go.mod` asks for on the first build. `go env GOTOOLCHAIN` says which; some
  distributions set `local`, and then it is Go 1.27 or nothing.
- **No cgo.** PocketBase's SQLite is pure Go, so `CGO_ENABLED=0` builds a
  static binary.
- **`go run . serve`** keeps its data in `./pb_data`, in the working directory;
  a built binary keeps it beside itself, and `--dir` puts it anywhere. Delete
  `pb_data` to start again from nothing.
- **`.env`** is read when the server starts: `FOYER_ADMIN_EMAIL`,
  `FOYER_ADMIN_PASSWORD`, `FOYER_TRUST_PROXY`.
- **Dev mode is off unless asked for** (`--dev`). PocketBase turns it on by
  itself under `go run`, and it prints every SQL statement to stdout.
  `main.go` keeps it off by default; `go run . serve --dev` still shows them.
- **The dashboard wears Foyer's icon.** `internal/dashboard` is a PocketBase
  UI extension, embedded in the binary. Its `main.js` is folded into
  `/_/extensions.js`, and its icon is served at
  `/_/extensions/foyer/icon.png`. The API is experimental in v0.40, and a
  test fails when the dashboard stops reading what it sets.
  - Outside `--dev`, `/_/` and its extensions are cached for two weeks. A
    changed icon shows after a hard reload, or in a fresh browser profile.

## The version

`internal/version/version.go` is Foyer's version, and it is always Loge's:
`YEAR.MONTH.BUILD`, where `BUILD` counts every release of the pair and never
starts again. The app's `npm run release` moves both, and refuses when they
disagree. `info` answers it, and `foyer --version` prints it.
`../loge/docs/development` has the whole scheme.

## Migrations

`migrations/` holds Foyer's own collections and settings, as Go, one file
each. PocketBase applies the ones a server has not run at every start, and
remembers them by file name.

- **Names are zero-padded** — `0001_users.go` — because PocketBase sorts them
  as text, and `10_x.go` would run before `1_x.go`.
- **One registration per file.** PocketBase names a migration after the file
  that registered it, so a second one in the same file collides.
- **Never edit one that has shipped.** A server that ran it never runs it
  again: fix forward with a new one.
- **Automigrate is off**, and the collections are never changed in the
  dashboard: what is made there exists on one machine only.
- **A new field is optional.** Older clients never send it.

## Tests

The Go tests use PocketBase's `tests` package. Each runs on a temporary data
directory, with the migrations applied, through PocketBase's real router
(`internal/server/harness_test.go`):

- **Users:** only a superuser makes or removes one; a user sees and changes
  only itself; a PIN is four digits or none, and its own user's; usernames
  are unique without regard to case.
- **Sessions:** 30 days; a password change ends every one; signing in is
  throttled per address.
- **The server:** its name and rate limits; `info` without a session;
  PocketBase's installer never opens a browser.
- **The dashboard:** the extension is served, and PocketBase's bundle still
  reads what it sets.
