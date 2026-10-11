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
- **The dashboard has Foyer's icon and pages.** `internal/dashboard/ui` is
  a PocketBase UI extension, embedded in the binary: plain JavaScript, no
  build step, so a change shows after the server is started again and the
  page reloaded. `main.js` is folded into `/_/extensions.js`; it registers
  `#/library` and `#/settings/adapters`, and each page's module —
  `library.js`, `adapters.js` — loads when first opened, beside `foyer.css`
  and the icon under `/_/extensions/foyer/`.
  - The pages are written as PocketBase's own: Shablon's `t`, `store` and
    `watch`, `app.components`, `app.pb.send`, its CSS classes and variables
    (`--smSpacing`, `--fieldsGap`). PocketBase's own source is the only
    documentation: read its bundle under `ui/dist` in the module cache.
  - Read store values inside functions, never while building elements, and
    give no `rid` to an element whose content is not reactive: `AGENTS.md`
    says why.
  - Foyer's files are served `no-cache`; the rest of `/_/` PocketBase lets a
    browser keep for two weeks outside `--dev`.

## A pretend Zurg

`dev/fakezurg` serves Zurg's WebDAV on this machine, from a folder of
`.zurgtorrent` files — Zurg's own records of its torrents, from its data
folder:

```bash
go run ./dev/fakezurg -addr 127.0.0.1:9999 <folder>
```

- Torrents go where Zurg's default config puts them: a CRC in a name to
  `anime`, a file numbered as an episode to `shows`, the rest to `movies`,
  where only a torrent's biggest file shows; and all of them to `__all__`.
- Names, sizes and dates come from the records — a torrent's folder dated
  when Real-Debrid added it. Their links and hashes are never read.
- It answers as Zurg does (`webdavtest`'s `AsZurg`, which the server's tests
  use too): relative hrefs, dates in `UTC`, every file sent as a part in
  chunks with no length.
- Every file is the same four seconds of 1080p made with ffmpeg — Japanese
  audio, English subtitles, two chapters — so ffprobe has something to read;
  `-sample` serves a video of your own instead.
- Add a WebDAV connection on the Adapters page at `http://127.0.0.1:9999/dav`. `-user`
  and `-password` make it ask for them.
- **Records from a real Zurg hold the household's Real-Debrid links.** Keep
  them out of the repository: the tests' names are made up.

## The version

`internal/version/version.go` is Foyer's version, and it is always Loge's:
`YEAR.MONTH.BUILD`, where `BUILD` counts every release of the pair and never
starts again. The app's `npm run release` moves both, and refuses when they
disagree; `internal/version`'s test fails when the app beside this
repository says another. `info` answers it, and `foyer --version` prints it.
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
- **The dashboard:** the extension is served and never kept stale; each page
  exports what `main.js` loads; PocketBase's bundle still has every `app.…`
  name the pages use.
- **Adapters:** the page's routes are the superusers'; a secret is masked,
  kept when a save leaves it out, and readable by no profile; a save that
  will not do names its fields.
- **Libraries:** a movies folder is one collection and a shows folder two; a
  name that will not do is refused; an `uncertain` title is hidden from a
  user, its episodes too, and the files from everyone but superusers;
  `Ensure` is idempotent and repairs; removing a library takes its
  collections and files, and one made again gets an id of its own; Zurg's
  folders come with their libraries.
- **A scan, end to end**, against a pretend Zurg and a pretend TMDB, with
  names shaped like the household's: one film in two torrents is one film
  with two versions, dated by its first and newest file; a guess is uncertain
  and a title TMDB never heard of its name's; a pack counted from its start
  is laid on the series TMDB numbers so, past its "Season 01" folder, three
  copies of one episode its versions, its special matched by name and its
  film an extra; two series of one name told apart by their episodes; the
  grid newest first; a second scan walks only what changed; a torrent gone
  for the grace period takes its film; a correction sticks, and merges;
  Zurg's webhook wants its token. And the episode mapping and the matching
  policy on their own.
- **Streams:** a user plays a title through Foyer — a part, the whole, a
  version by its file; a forged, lengthened or expired signature is refused,
  and a new file-token secret ends every address; nothing plays that a user
  may not see; no stream address reaches the request log, a refused one
  neither; the loopback serves a tool one file until revoked. And the proxy
  on its own: ranges, HEAD, a source's failures, a stream outlasting the
  server's deadlines, a player leaving.
- **ffprobe:** answers mapped as Loge says a version — Dolby Vision with its
  HDR10 fallback, HLG, Atmos, DTS:X, PGS, forced and SDH subtitles, cover art
  left out, chapters — and the real ffprobe reading a file ffmpeg makes,
  through an address; a failure said without it. End to end, a file behind
  the pretend Zurg read through the loopback, its film's lists redrawn, and a
  file that is no video tried again later. FFmpeg's detection, and a missing
  render device. These skip where ffmpeg is not installed.
- **Watch status:** Loge's rules on their own — a report at 0, 89 % and 90 %,
  the end sent twice, played again, marked and unmarked — and end to end:
  each user keeps their own, reads only their own and writes none past the
  rules; resume lists what was begun; a series is marked by its episodes;
  nothing is kept for a guess; a correction takes what was watched with it,
  and two titles become one with both playings.
- **The guard:** Foyer's collections refuse a superuser's record writes, a
  change, a truncate, an import and a delete; `users`' rules stay.
- **TMDB**, against a fake of its routes: Loge's matcher's own cases, ported —
  the key as a bearer header or in the query, a refused key latched, a year
  either side, names in other languages, several of one name handed over —
  and a film's details, seasons twenty to a call, episode groups, artwork.
- **WebDAV**, against `webdavtest`, a pretend share that can answer as Zurg
  does: its folders, an entry's files flat and nested, hrefs however they
  are escaped or relative, a name listed twice, a refused sign-in kept,
  ranges — and Zurg's `200` with a `Content-Range` read as `206`.
- **Titles:** Loge's own cases for `titleKey` and `bareTitle`.
- **What both sides must answer alike**, read from the app beside Foyer —
  its `adapters/api/fixtures/foyer/`, the files its own tests read:
  `titles.json`, `identity.json`, and each route's shape (`routes/info.json`
  against `info`). `internal/fixtures` finds them through the workspace; a
  checkout of Foyer alone has no app beside it, and those tests skip.
- **Release names**, a table of some fifty: scene names, dotted and dashed;
  numeric titles and years inside titles; season packs and ranges; anime
  numbered from the start, with versions, ranges and recaps; specials, OVAs
  and films inside a pack; what a file is — resolution, source, codecs, HDR,
  audio, edition. And a file read where it lies: its season folder, a
  specials or extras folder, its torrent's name, Zurg's ` (id)`. `FuzzName`
  never panics.

## The harness: the app's code against the real server

`harness/` is Node and vitest, test-only: it builds the binary once, starts
it per test on a temporary directory and a free port with an environment of
its own, makes users as the superuser does, and drives the Loge code that
speaks Foyer — `@loge/api`'s protocol module now, the Foyer adapters as they
come — aliased from the app beside it, never installed. The Go tests prove
the server; the harness proves the pair.

```bash
cd harness && npm install && npm run typecheck && npm test
```

**Live TMDB**, opt-in and local only — the key from the workspace's
`tmdb.env`, never committed and never printed:

```bash
FOYER_TMDB_LIVE_KEY="$(sed -n 's/^API_Read_Access_Token=//p' ../tmdb.env)" go test ./internal/adapters/tmdb -run TestLive -v
```
