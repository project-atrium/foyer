# Documentation — Foyer

The household's own server, on PocketBase: its profiles, and the library it
is growing into.

| Folder | What is there |
| --- | --- |
| `getting-started/` | Run it, make the superuser and the household's users |
| `api/` | The routes, the collections and their rules, errors |
| `deployment/` | Running it for real: the binary or Docker, `.env`, TLS, backups, the dashboard, updating |
| `development/` | Go and PocketBase, migrations, the Go tests |

## Decisions

Each recorded with why. The account server of Phases 5 to 10 — accounts that
held profiles, records synced in batches, invites — is retired, and its
decisions are in git.

| Topic | Decision | Why |
| --- | --- | --- |
| Runtime and storage | PocketBase, used as a Go framework: one binary around one SQLite database in `pb_data`. Pinned to v0.40.x. | It already is much of the server: users, sign-in and sessions, rules per user, file storage with thumbnails, cron, a dashboard, backups, rate limits. Pinned because it is pre-1.0, and minor versions break. |
| Tenancy | One Foyer is one household. A PocketBase user is one profile; the superuser makes them. | Profiles of one household share a library and see each other's names. A server shared by several households would show its owner everyone's watch status and favourites, so it is not a goal. |
| A profile's PIN | On the user's own record: four digits or none, readable by that user. | A device signed in as several users asks for the PIN of the one being picked, and a child lock is not a password. |
| Auth and sessions | PocketBase's own: a username and a password, bcrypt on the server. Sessions of 30 days; a password change ends them all. | Nothing to invent. Cutting off a lost device is changing the password. |
| Secrets | Plain text: Zurg's password, the TMDB key, PINs. Readable to the server and its superuser. | The server is the household's own. Sealing them stays possible later. |
| Throttling | PocketBase's rate limiter on sign-in: per address, in memory. | Never a per-username lock: that would hand anyone who knows a username a way to keep its owner out. |
| Backups | PocketBase's: a ZIP of `pb_data`, on demand or on a schedule, locally or to S3. | Consistent while it runs. |
| Deployment | One binary, or Docker and compose. TLS from a reverse proxy, or from PocketBase's own certificates (`serve <domain>`). | Self-hosters mostly run a proxy already; for those who do not, PocketBase can do it itself. |
