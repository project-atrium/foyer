# AGENTS.md — foyer

The household's own server: PocketBase, used as a Go framework. Read the
workspace root `AGENTS.md` and `../.claude/architecture.md` first, then
`../.claude/new_architecture.md` — what Foyer is growing into — and
`docs/api`, every route, collection and rule.

---

## What this is

**One Foyer is one household, and a PocketBase user is one profile** —
Alex, Sarah, Kids. The household's superuser makes them in the dashboard.

Foyer is becoming the household's media server: its library from Zurg, named
by TMDB and probed by ffprobe, watch status per user, and streams for Loge.
`README.md`'s "Current state" says how far that has got.

## What this is not

- **Not required.** Loge works without it: a device's account can be local.
- **Not where files are renamed, moved or stored.** Media stays in Zurg;
  Foyer reads it, names it in its own records, and streams it through.
- **Not a place for what belongs to a device.** Players, sync settings and
  caches never reach it.
- **Not the account Loge's `sync/custom-server` speaks to — not any more.**
  That protocol was removed with the account that held profiles, and Loge's
  side is redesigned in Step 2: a device with two profiles signs in as two
  users, and each user's PIN is on its record.

## Boundaries

- **PocketBase and Go. Nothing from TypeScript.** Go cannot import
  `@loge/api`. Where Foyer keeps something Loge also computes — a watch
  identity, a title key — it is ported, and its tests hold it to the same
  answers.
- **Never the app.**

## What the server promises

1. **A user reads and changes only itself.** `users`' rules are
   `id = @request.auth.id`; only a superuser makes or removes one.
2. **A PIN is its own user's.** Four digits or none, readable by that user so
   a device signed in as several users can ask for it, and by nobody else.
3. **Sessions last 30 days**, and a password change ends every one.
4. **Signing in is throttled per address** — never per username: anyone who
   knows one could keep its owner out.
5. **`info` answers without a session.**

## What it must never do

- **Open a browser.** PocketBase opens a tab to make the first superuser
  whenever `serve` starts without one — on whoever started it, and once per
  test. `Bind` turns its installer off; the superuser comes from
  `FOYER_ADMIN_*` or `superuser upsert`. A test app skips the installer by
  itself, so the test that holds this arms it again first.
- **Lock a user out from everywhere.** PocketBase's limiter counts per
  address, in memory. Never add a count per username.

---

## Shape

```
main.go              .env, then pocketbase.New(); migratecmd, automigrate off; server.Bind
go.mod               PocketBase pinned to v0.40.4, which needs Go 1.27
internal/config/     FOYER_ADMIN_*, FOYER_TRUST_PROXY, and the .env reader; the old SC_* names refused, MAX_PROFILES and SIGNUP named as doing nothing
internal/routes/     GET /api/foyer/info
internal/server/     Bind: the guard, the routes, the trusted proxy, no installer, the UI extension — and the HTTP tests
internal/guard/      Foyer's collections refuse every write request, a superuser's too; users' rules stay
internal/adapters/   what Foyer connects to: settings records, fields, masked secrets, the Adapters page's routes
  tmdb/              TMDB: Loge's matcher ported (Find), a film's and a series' details, seasons, episode groups, artwork
  zurg/              Zurg over WebDAV: its folders, a torrent's files, a file's bytes with ranges
  zurg/zurgtest/     a pretend Zurg, for the tests
internal/titles/     Loge's titleKey and bareTitle, ported to the byte
internal/parse/      release names: title, year, season and episode, numbers counted from the start, what the file is
internal/library/    libraries: their collections from one template, the scan — walk, name, identify, place,
                     sweep, artwork — corrections, and the dashboard's library routes
internal/media/      what a file is: Loge's MediaVersion, from a release's name or ffprobe
internal/stream/     signed stream addresses, the Range proxy, the quiet log, the loopback for tools
internal/identity/   Loge's watchIdentity and identityHash, ported
internal/dashboard/  the dashboard's icon: a PocketBase UI extension, embedded
internal/version/    the version, always the app's: its `npm run release` moves both
migrations/          0001 users are profiles; 0002 the name, the rate limits, the first superuser; 0003 adapters;
                     0004 libraries, media_torrents, media_files, identities; 0005 streams' rate limit
Dockerfile           two stages: a Go build, then the binary alone
docker-compose.yml   the server, and pb_data in a volume
```

Go tests sit beside what they test. Everything else is PocketBase's: `serve`,
`superuser`, `migrate`, users and sessions, the record APIs, the dashboard at
`/_/`, backups and the rate limiter.

## Rules that break silently

- **Foyer's collections are Foyer's.** The guard refuses every request that
  would write one — the dashboard's, a superuser's, an import, a truncate.
  Foyer's code saves at the model level, which no request hook sees. A new
  collection of Foyer's joins the guard's list in `internal/server`.
- **A library's collections are Foyer's, from one template.** `x`, or `x` and
  `x_episodes`, made and kept by `library.Ensure` — never by a migration,
  which cannot see them, and never in the dashboard. Change the template,
  raise `TemplateVersion`, and every library is brought up to it at start.
  Never change a field's type: add one under a new name. A new collection
  takes a random id, because PocketBase removes a deleted one's files in the
  background, under its id.
- **An `uncertain` title is hidden from users**, its episodes with it, by the
  collections' rules — and its files by `media_files` being the superusers'
  alone. Whatever a user reads of a title — its versions, their paths — is on
  the title itself.
- **No network inside a transaction.** Zurg, TMDB and the artwork servers are
  asked first; then one transaction writes. A file field's save uploads to
  storage — S3 too — so artwork is saved on its own, outside any. A library's
  writes — its scan, its artwork, a probe's results — take its lock, so none
  overwrites what another changed.
- **TMDB is asked once per title**, and the answer kept in `identities` by the
  title's key: a superuser's word (`manual`) is never asked again, a guess
  (`uncertain`) waits for one, and "nothing" is asked again after a week.
  TMDB off, or its key refused, is no answer: the title is its name's until
  TMDB can be asked.
- **What Zurg stops listing is kept three days**, then let go with any title
  left with nothing — never on a listing that came back empty, nor when most
  of a library vanished, until the next scan agrees. Watch status stays.
- **A stream outlives PocketBase's deadlines.** Its server reads and writes
  with five-minute deadlines, which would end a film five minutes in; the
  proxy lifts both, and a write that stalls half an hour ends it instead.
- **A stream address is a key, and never logged.** It opens its file to
  whoever holds it for six hours. `stream.Quiet` runs inside PocketBase's
  activity logger and answers every stream request itself — a refusal, a
  429, a panic — so the logger has nothing to keep. A test reads the log.
- **A tool reads files through the loopback**, never from Zurg: a grant on
  127.0.0.1 opens one file for minutes, so ffprobe's arguments and output
  never hold Zurg's address or password.
- **A rate limit's place matters.** PocketBase takes the first rule whose
  prefix matches, in order: a narrower prefix goes before `/api/`.
- **One scan at a time**, from one loop that runs only beside `serve`
  (`Background`): SQLite has one writer, and nothing a test binds starts it.
- **No admin route answers 401 for a service's refusal.** The dashboard takes
  any 401 for its own session ending and signs the superuser out: a test that
  failed is `200 { ok: false }`.
- **A key in an address never reaches a log or an error.** TMDB's API key
  travels in the query, so the client's errors drop the address Go's
  `url.Error` would print.
- **Zurg is read over WebDAV only.** Its folders — the ones its config sorts
  torrents into — exist only there. A torrent's files may be listed flat,
  with ` (id)` added where two share a name, or under folders of their own:
  walk both. Zurg answers a part with `200` and a `Content-Range` at times —
  that is a `206` — and a whole file answered for a part is refused, or a
  player would start again from the beginning. Never ask Zurg for gzip.
- **A release's name is read, never trusted.** `internal/parse` reads what
  the uploader typed; the library checks it against TMDB, and a superuser
  corrects the rest. A season folder never takes a number counted from the
  series' start — a pack's "Season 01" can hold episode 62 — and Zurg's
  ` (id)` suffix is stripped before anything is read.
- **Titles are keyed as Loge keys them.** `internal/titles` is `@loge/api`'s
  `titleKey` and `bareTitle`, and its tests are Loge's: change one, change
  both.

- **Migrations sort by file name, as text.** `10_x.go` runs before `1_x.go`,
  so names are zero-padded: `0001_…`, `0002_…`. A server that ran one never
  runs it again: never edit one that has shipped — fix forward with a new
  one. Automigrate stays off, and nobody changes the collections in the
  dashboard: a field added there exists on one server only, and a rule
  loosened there opens the data.
- **Two registrations in one migration file collide.** PocketBase names a
  migration after the file that registered it.
- **A new field is optional.** An older client never sends it.
- **`FOYER_TRUST_PROXY` only behind a proxy.** Without one, anyone can claim
  any address and slip the limiter. Behind one without it, every caller has
  the proxy's address, and one stranger's tries throttle the whole household.
- **The names from before Foyer are refused.** `SC_TRUST_PROXY` and the rest
  became `FOYER_…`; read silently, an old `.env` would drop its proxy header,
  so `FromEnv` refuses to start and names the new one. `FOYER_MAX_PROFILES`
  and `FOYER_SIGNUP` are only named: ignoring them weakens nothing.
- **PocketBase takes a bad token for a guest.** A missing, expired or invalid
  token is no error to it: the request goes on as a guest, and a guest's list
  under owner rules is empty. Where a client must tell "nothing" from
  "signed out", it refreshes the session.
- **PocketBase is pinned.** It is pre-1.0, and minor versions break its Go API.
  Update deliberately, one version at a time, with the Go tests.
- **The dashboard wears Foyer's icon through a UI extension**, which
  PocketBase marks experimental (`core.ServeEvent.UIExtensions`).
  `internal/dashboard` sets the dashboard's `favicon`, `mainLogo` and
  `headerLogo`. A test checks that PocketBase's bundled dashboard still reads
  them, so an upgrade that renames them fails instead of quietly showing
  PocketBase's icon again.

## Current state

**Users are profiles; the library is being built.** PocketBase v0.40.4, used
as a Go framework: `users` as profiles with their PINs, the rate limits, the
first superuser from `.env`, `info`, and the dashboard's icon. `go test ./...`
proves it. The account-sync protocol, its harness and the shared fixtures are
gone — git has them. The Dockerfile and compose file are written, and not yet
built.
