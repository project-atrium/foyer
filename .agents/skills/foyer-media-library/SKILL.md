---
name: foyer-media-library
description: Foyer's media library, end to end — a WebDAV share read, Zurg's above all, release names parsed, TMDB asked once per title, a library's collections made from one template, artwork in PocketBase's storage, ffprobe through the loopback, signed streams, watch status kept by identity, and the dashboard's Library and Adapters pages. Use when changing a scan, the parser, matching or episode mapping, a library collection's template, probing, streams, watch status, an admin route or a dashboard page — or when finding out why a title is missing, misnamed, hidden or will not play.
---

# Foyer's media library

Read `AGENTS.md` first — its rules that break silently are the point — then
`docs/library/README.md` (how a scan works) and `docs/api/README.md` (every
route, collection and rule).

## When to use it

- a scan, a folder, a torrent or a file read wrongly
- the parser (`internal/parse`), matching (`library/match.go`) or where an
  episode lands (`library/mapping.go`)
- a field on a library's collections, or a new static collection
- probing, a version's fields, a title's aggregates
- the stream route, the play route, watch status
- an admin route, or a page of the dashboard (`internal/dashboard/ui`)

Not for deployment (`docs/deployment`) or for Loge: Foyer imports nothing of
the app, and what it shares with it — a title key, a watch identity — is
ported, with Loge's own test cases.

## How a file becomes a title

```
webdav.List / Walk        the folder, each entry's files             adapters/webdav
library/scan.go           media_files written, new ones pending      outside any transaction: network first
parse.Path / Name         title, year, numbers, what the file is     pure, table-tested
library/plan.go           files grouped into candidate titles
library/match.go          identities: TMDB asked once per key        sure, uncertain, none, manual
library/place.go          the title, its episodes, its files linked  one transaction, the library's lock
library/mapping.go        an episode's place: release, offset, absolute, cumulative, group, special
library/sweep.go          what the share stopped listing, after 72 hours
library/artwork.go        posters, backdrops, logos, stills          saved one record at a time
probe/queue.go            ffprobe through the loopback → versions    then the title's lists redrawn
library/play.go, stream/  a signed address; the Range proxy
watch/                    a user's state, by identity
```

## Changing something

- **A field on a library's collections**: `library/template.go`, and raise
  `TemplateVersion` — every library is brought up to it at start. Add under
  a new name; never change a field's type.
- **A static collection**: a new migration, zero-padded, one registration per
  file; then add it to `collections` in `internal/server`, or the guard lets
  the dashboard write it.
- **An admin route**: under `/api/foyer/admin`, superusers only. A
  provider's refusal is `200 { ok: false }` or a `4xx`/`502` — never `401`,
  which signs the dashboard's superuser out.
- **A route with a key in its address** skips the activity log
  (`apis.SkipSuccessActivityLog`); a stream goes further — `stream.Quiet`
  answers every outcome itself. A test reads `_logs`.
- **A new network call**: never inside `RunInTransaction`, never under a
  library's lock for longer than its writes.
- **The parser**: a row in `internal/parse`'s tables, made-up names only —
  the household's samples hold their Real-Debrid links — and `FuzzName` run
  for a while after a change to a regular expression.
- **A dashboard page**: plain JavaScript, written as PocketBase's own pages
  are. Read store values inside functions, give no `rid` to static content,
  and let `TestTheDashboardStillHasEverythingFoyerUses` tell you when a name
  is not PocketBase's.

## Verify

```bash
gofmt -l . && go vet ./... && go test ./...
```

TMDB for real, opt-in, the key read into the environment and never printed:

```bash
FOYER_TMDB_LIVE_KEY="$(sed -n 's/^API_Read_Access_Token=//p' ../tmdb.env)" go test ./internal/adapters/tmdb -run TestLive
```

End to end on this machine, against a pretend Zurg:

```bash
go run ./dev/fakezurg -addr 127.0.0.1:9999 <folder of .zurgtorrent records>
FOYER_ADMIN_EMAIL=… FOYER_ADMIN_PASSWORD=… go run . serve --dir <a temporary folder> --http 127.0.0.1:8090
```

Then the dashboard: Settings → Adapters — WebDAV at `http://127.0.0.1:9999/dav`,
TMDB's key, FFmpeg, each tested — **Make a library** of `movies` and
`shows`, and **Library**. Drive it headless — Chrome with its own profile and
a free debugging port — never in a window on the owner's screen. And check:

- a user's list leaves out every `uncertain` title, and `media_files` answers
  them `403`;
- `POST /api/foyer/play`, then a ranged read of its address: `206`, and
  ffprobe reads it;
- `_logs` holds no `/api/foyer/stream/`.

## Why a title is…

- **missing** — its `media_files` row: `role` (`version`, `extra`,
  `unmapped`, `pending`), `parsed`, `missing_since`. The `identities` row for
  its key: `state`, `reason`, `candidates`. A scan's notes:
  `GET /api/foyer/admin/status`.
- **hidden from users** — `match_state` is `uncertain`: Confirm it, or Set
  TMDB id, on the Library page.
- **on the wrong episode** — the file's `parsed`, and `place` in
  `mapping.go`, whose `by` says how it was placed. A special is matched by
  its name against TMDB's season 0, whole or by the part before TMDB's
  colon or dash.
- **not playing** — `probe_state` and `probe_error`; the stream answers
  `404` for a file the share no longer has and `502` while it fails.
