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
| `GET /api/foyer/admin/adapters` | superusers | every adapter: `[{ key, name, description, notice?, enabled, fields, values, status? }]`, each secret as `"******"` when set and `""` when not |
| `PUT /api/foyer/admin/adapters/{key}` | superusers | `{ enabled, values }` → the adapter as `GET` shows it; `400` names each field that will not do, as `values.<key>` |
| `POST /api/foyer/admin/adapters/{key}/test` | superusers | `{ values? }`, laid over what is saved → `{ ok, message, details? }` |

### The Adapters page's routes

- **A field is one of** `text`, `url`, `password`, `number`, `select` or
  `bool`, with its label, default, choices and help: the page draws every
  form from them.
- **A secret** — a `password` field — is stored as typed and never sent back.
  A save that leaves it out, or sends `"******"` back, keeps it; one that
  sends `""` clears it.
- **A test answers `200`, whatever the service said.** A refused key is
  `{ ok: false, message }`: the dashboard signs its superuser out on any
  `401`, so no admin route ever answers one for a service's refusal. The last
  test's result is kept beside the settings, once they are saved.
- **Zurg** has its address (`http://zurg:9999`; its WebDAV is under
  `/dav/`), a username and a password if Zurg asks for them, and how often to
  look for new media, in minutes (15 unless set). Its test lists Zurg's
  folders.
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

**`adapters`** — one record per adapter that has been saved: `key`,
`enabled`, `config` (every value but the secrets), `secrets` (plain text, as
typed) and `status` (the last test). Written only through the Adapters page.

### Rules

| Collection | List, view | Create | Update | Delete |
| --- | --- | --- | --- | --- |
| `users` | its own record | superusers | its own record | superusers |
| `adapters` | superusers | Foyer | Foyer | Foyer |

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
