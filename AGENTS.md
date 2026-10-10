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
internal/server/     Bind: the routes, the trusted proxy, no installer, the UI extension — and the HTTP tests
internal/dashboard/  the dashboard's icon: a PocketBase UI extension, embedded
internal/version/    the version, always the app's: its `npm run release` moves both
migrations/          0001 users are profiles; 0002 the name, the rate limits, the first superuser
Dockerfile           two stages: a Go build, then the binary alone
docker-compose.yml   the server, and pb_data in a volume
```

Go tests sit beside what they test. Everything else is PocketBase's: `serve`,
`superuser`, `migrate`, users and sessions, the record APIs, the dashboard at
`/_/`, backups and the rate limiter.

## Rules that break silently

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
