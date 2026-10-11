# API reference

Foyer's own routes, PocketBase's routes as Foyer uses them, the collections
and their rules, and how they fail.

- Bodies are JSON; Foyer's own answers are camelCase, its collections' fields
  snake_case.
- The session token goes in `Authorization`; PocketBase takes it with or
  without `Bearer `.
- Errors are PocketBase's: `{ "status", "message", "data" }`, where `data`
  holds field errors, each `{ "code", "message" }`.

## Foyer's routes

| Route | Who | Answer |
| --- | --- | --- |
| `GET /api/foyer/info` | anyone | `{ serverVersion }` |
| `POST /api/foyer/play` | users | `{ collection, id, file? }` → `{ url, expiresAt, version, versions, chapters }`: a signed address for the title's best version, or the file asked for — one of its versions, or of a series' `extras`; `404` for a title the user may not see |
| `POST /api/foyer/watch/progress` | users | `{ collection, id, kind: started \| progress \| stopped \| end, positionMs, durationMs, paused?, session? }` → `204` |
| `POST /api/foyer/watch/played` | users | `{ collection, id, played }` → `204`; a series marks each of its episodes |
| `GET /api/foyer/watch/resume` | users | `?limit` (20) → `{ items: [{ collection, id, identity, title, showTitle?, season?, episode?, positionMs, durationMs, watched, lastPlayedAt }] }`, the latest first |
| `GET`, `HEAD /api/foyer/stream/{file}?u&exp&sig` | whoever holds the address | the file's bytes, a `Range` passed on: `206` with `Content-Range`; `403` when the signature is wrong or six hours old |
| `GET /api/foyer/admin/adapters` | superusers | every adapter, by category: `[{ key, category, name, description, notice?, single, planned, fields, defaults, connections: [{ id, adapter, name, enabled, values, status? }] }]`, each secret as `"******"` when set and `""` when not |
| `PUT /api/foyer/admin/adapters/{key}` | superusers | a single adapter's settings — TMDB's, FFmpeg's: `{ enabled, values }` → its one connection, made by the first save; `400` names each field that will not do, as `values.<key>`, and refuses an adapter set up as connections |
| `POST /api/foyer/admin/adapters/{key}/test` | superusers | `{ values? }`, values not yet saved, over the adapter's defaults — or a single adapter's saved ones → `{ ok, message, details? }` |
| `POST /api/foyer/admin/connections` | superusers | `{ adapter, name, enabled, values }` → the connection; `400` for a planned adapter, a second connection of a single one, or a field that will not do |
| `PUT /api/foyer/admin/connections/{id}` | superusers | `{ name, enabled, values }` → the connection |
| `DELETE /api/foyer/admin/connections/{id}` | superusers | `204`; `409` while a share's folders are libraries |
| `POST /api/foyer/admin/connections/{id}/test` | superusers | `{ values? }`, laid over what is saved → `{ ok, message, details? }`, kept as the connection's status |
| `GET /api/foyer/admin/connections/{id}/folders` | superusers | `{ ok, message?, folders: [{ folder, library? }] }`: a share's folders, each with the library it is, and libraries whose folder the share no longer lists |
| `POST /api/foyer/admin/libraries` | superusers | `{ connection, folder, name, kind: "movies" \| "shows", enabled }` → the library, its collections made; `connection` may be left out where the household has one share; `400` names what will not do |
| `PATCH /api/foyer/admin/libraries/{id}` | superusers | `{ enabled }` → the library |
| `GET /api/foyer/admin/status` | superusers | `{ scans: [{ library, name, full, phase, running, torrents, walked, newFiles, missing, removed, titles, artwork, error?, notes? }] }`, kept in memory |
| `POST /api/foyer/admin/scan` | superusers | `{ library?, full? }` → `202`: a scan of that library, or of all |
| `GET /api/foyer/admin/library` | superusers | `?page&perPage&library&state&q` → `{ page, perPage, totalItems, counts, items: [{ collectionId, collectionName, library, kind, id, title, year, poster, matchState, lastAdded, versionCount, quality }] }`, every library's titles newest added first |
| `GET /api/foyer/admin/library/{collection}/{id}` | superusers | `{ item, library, files, seasons, identity }`: a title, its files and what TMDB answered |
| `POST /api/foyer/admin/library/{collection}/{id}/confirm` | superusers | `204`: a guess confirmed |
| `POST /api/foyer/admin/library/{collection}/{id}/match` | superusers | `{ tmdbId }` → `204`: the title is that one, merged into a title already holding it; `502` when TMDB cannot say |
| `GET /api/foyer/admin/connections/{id}/hook` | superusers | `{ token, command }`: the line for that share's `on_library_update` — Zurg's — or any script |
| `POST /api/foyer/webdav/updated` | a share — Zurg's `on_library_update` — with `Authorization: Bearer <token>` | the folders that changed, one per line → `202`: that share's libraries for those folders, all of them for none; the token says which share called; `403` without one |
| `DELETE /api/foyer/admin/libraries/{id}` | superusers | `204`: its collections, artwork, torrents, files and identities gone; watch status stays. `409` when a collection of your own still points at it |

### Watch status

- **Foyer keeps each user's watch status** for its library, as a media server
  keeps its own: one record per user and thing watched in `watch_states`,
  keyed by `identity` — a film's every file, in any library, is one.
- **The rules are Loge's.** A stop at 90 % of the length or more, or `end`,
  is watched, and watched holds while it is played again; a report at 0 never
  lowers where it got to; a playing's end sent twice counts once, by its
  `session`. Unwatched starts again from the beginning; how often it was
  played stays.
- **A title the user may not see** — a guess not yet confirmed — keeps none.
- **A title's identity that changes takes its watch status with it**: TMDB
  naming it at last, or a superuser. Two titles become one: watched if either
  was, played as often as both, and where the later playing left off.

### Streams

- **An address is its signature**, good for six hours: a player sends no
  headers. It is signed with a key drawn from the `users` collection's
  file-token secret — regenerating that secret in the dashboard (users →
  Options → Tokens) ends every address at once, and signs nobody out.
- **Bytes come through Foyer**, so the share's address and password never leave
  the server. A `Range`, `If-Range` or HEAD is passed on; Zurg's `200` with
  a `Content-Range` is a `206`; a player that leaves stops Zurg's answer.
- **No stream address is ever logged**, a refused one neither: it opens the
  file to whoever holds it.
- Streams have a rate limit of their own: 600 requests in 10 seconds per
  address, before the catch-all.

### The Adapters page's routes

- **An adapter is a kind of thing Foyer reaches; a connection is one set
  up.** Two WebDAV shares are two connections of one adapter, each named and
  switched on its own. A **single** adapter — TMDB, FFmpeg — is the
  household's once, and saved by its key. A **planned** adapter is listed so
  the household sees what is coming, and cannot be set up.
- **Adapters come by category**, in the page's order: Sources, IPTV,
  Metadata, Tracking, Tools.
- **A field is one of** `text`, `url`, `password`, `number`, `select` or
  `bool`, with its label, default, choices and help: the page draws every
  form from them. A password marked `visible` — a portal's MAC address — is
  shown as typed and sent back, and kept with the secrets all the same.
- **A secret** — a `password` field — is stored as typed and never sent back.
  A save that leaves it out, or sends `"******"` back, keeps it; one that
  sends `""` clears it.
- **A test answers `200`, whatever the service said.** A refused key is
  `{ ok: false, message }`: the dashboard signs its superuser out on any
  `401`, so no admin route ever answers one for a service's refusal. The last
  test's result is kept beside the settings, once they are saved.
- **WebDAV** has the share's address — its WebDAV root, as Zurg's is
  `http://zurg:9999/dav` — a username and a password if the share asks for
  them, and how often to look for new media, in minutes (15 unless set). Its
  test lists the share's folders, and names Zurg's `/dav` where nothing
  answers as WebDAV. A share whose folders are libraries is not removed until
  they are; each share has a webhook token of its own.
- **FFmpeg** has the paths of `ffprobe` and `ffmpeg` (their names on the PATH
  unless set), the render device (`/dev/dri/renderD128`) and how many files
  are read at once (2). Switched on, ffprobe reads what each file is. Its test
  says which versions are installed and what the GPU encoded: a few frames
  of H.264 and HEVC through VAAPI, and through QSV on Intel.
- **TMDB** has two fields: `key` — a Read Access Token or an API key — and
  `language`, `en-US` unless set. Its notice is the one TMDB's terms ask for.

## PocketBase's routes

| Call | Route | Answer |
| --- | --- | --- |
| sign in | `POST /api/collections/users/auth-with-password`, `{ identity, password }` with the username as `identity` | `{ token, record }`; `400` for a wrong name or password alike |
| keep the session | `POST /api/collections/users/auth-refresh` | `{ token, record }`, good for another 30 days; `401` when the session ended |
| a user's own record | `GET` / `PATCH /api/collections/users/records/{id}` | the user itself; a password change needs `oldPassword` |
| health | `GET /api/health` | `200` while it serves |

**Signing out** has no route. PocketBase keeps no sessions; the device forgets
its token.

## The collections

**`users`** — a profile: PocketBase's own auth collection.

| Field | |
| --- | --- |
| `username` | how the user signs in: 3 to 50 of `A–Z a–z 0–9 . _ -`, starting with a letter or digit; unique without regard to case, and sign-in ignores case too |
| `name` | what the household calls them, up to 50 characters |
| `avatar` | a picture — JPEG, PNG, WebP or GIF, up to 5 MB — with thumbnails `128x128` and `256x256` |
| `pin` | four digits, or empty: a child lock, not a password |
| `email` | optional |
| password | a bcrypt hash; at least 8 characters |

Password sign-in only: OAuth2, OTP, MFA and sign-in alerts are off.

**`connections`** — one record per connection: `adapter`, `name`,
`enabled`, `config` (every value but the secrets), `secrets` (plain text, as
typed — a webhook's token among them), `status` (the last test) and
`position`. Written only through the Adapters page. Until 2026.10 it was
`adapters`, one record per adapter; migration 0007 renamed it in place.

**`watch_states`** — what a user watched: `user`, `identity`, the title it
was last played as (`item_collection`, `item_id`), `external_ids`, `watched`,
`position_ms`, `duration_ms`, `play_count`, `last_played_at`.

### The library

**A library is a folder of a share** — `libraries.connection` says which —
and its collections are named after it: a movies library `movies` is the
collection `movies`; a shows library `shows` is `shows`, with
`shows_episodes`. Foyer makes and keeps them from one template; a name is
lower-case letters, digits and underscores, never ends in `_episodes`, and is
never Foyer's or PocketBase's own — nor one Foyer's coming migrations make
(`playlists`, `devices`…, and anything starting `iptv_` or `youtube_`), which
would otherwise stop at the library and keep the server from starting.

| Collection | One record per |
| --- | --- |
| `libraries` | library: `name`, `kind`, its share (`connection`) and `folder` — unique together — `enabled`, its last scans and their error |
| `media_entries` | entry the share lists in a library's folder — a torrent on Zurg, a folder or a file elsewhere — and whether it is a file lying there loose |
| `media_files` | file of an entry (`entry`): its `path` under the folder, what its name says (`parsed`), which title took it (`item_collection`, `item_id`) and as what (`role`), what ffprobe found (`probe`) |
| `identities` | title a library read: what TMDB was asked (`key`), its answer (`tmdb_id`), and how sure (`state`: sure, uncertain, none, manual) |
| `<movies library>` | film: TMDB's names, dates, ids, rating and artwork; `versions`, one per file; `quality`, `encoding`, `container`, `hdr`, `audio_tracks`, `subtitles` and the languages, over its versions; `added` and `last_added`; `match_state` |
| `<shows library>` | series: the same, its seasons as TMDB counts them, its lists over its episodes, and `extras` — `{ file, path, kind, title, size }`: the films in its pack, and specials and OVAs TMDB has no episode for |
| `<shows library>_episodes` | episode: its `show`, `season`, `episode`, TMDB's title, overview and still, its `versions` and lists |

- **`match_state`** is `matched` (TMDB was sure), `uncertain` (a best guess),
  `unmatched` (TMDB had nothing: the title is its name's) or `manual` (a
  superuser decided). **An `uncertain` title is hidden from users** — its
  episodes too — until a superuser confirms or corrects it.
- **`identity`** is what watch status is kept by, as Loge keys it:
  `tmdb:movie:937278`, `tmdb:tv:63926/s3e11`, or the title and year where
  TMDB said nothing.

### Rules

| Collection | List, view | Create | Update | Delete |
| --- | --- | --- | --- | --- |
| `users` | its own record | superusers | its own record | superusers |
| `connections` | superusers | Foyer | Foyer | Foyer |
| `libraries` | users | Foyer | Foyer | Foyer |
| `media_entries`, `media_files`, `identities` | superusers | Foyer | Foyer | Foyer |
| `watch_states` | its own user's | Foyer | Foyer | Foyer |
| a library's collections | users, but not an `uncertain` title or its episodes | Foyer | Foyer | Foyer |

### The guard

Foyer's own collections are written by Foyer's code alone. A request that
would write their records, or change, truncate, import or delete one of them,
is refused `403` — a superuser's from the dashboard too. `users` keeps its
records writable by the superuser, but its rules and Foyer's fields on it
stay as the migrations made them: loosened, one profile would read another.
The superuser's SQL console still reaches past the guard: it prevents
accidents, not malice.

## Errors

| Status | When |
| --- | --- |
| `400` | a refused sign-in; a value the rules or fields refuse, such as a PIN that is not four digits |
| `401` | no valid session where one is needed: it expired, or the password changed |
| `403` | creating or removing a user without being a superuser |
| `404` | a record that is not the caller's |
| `429` | throttled |

## Rate limits

PocketBase's rate limiter, switched on by the migrations, counts per address,
in memory, and never per user. `docs/deployment` lists its rules. Behind a
proxy it needs `FOYER_TRUST_PROXY` (`docs/deployment`).
