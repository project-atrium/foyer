# AGENTS.md — harness/

**The one exception to "nothing here is TypeScript".** This folder is Node
and vitest, test-only, and nothing in it ships: not in the binary, not in the
image (`.dockerignore` leaves `*.md` and the build context only takes what
the Dockerfile copies).

It exists because Go cannot import `@loge/api`. The shared fixtures in the
app's `adapters/api/fixtures/foyer/` keep the two sides' answers alike; this
keeps the *adapters* in step: the Loge code that speaks Foyer — `@loge/api`'s
protocol module now, and the Foyer adapters as they come — against the real
binary: `sync/foyer` pairs a device, signs users in, renews a session and
is refused once a password changes (`test/household.test.ts`).

## How it works

- `test/support/build.ts` builds the binary once per run (`go build`), in a
  temporary directory.
- `test/support/server.ts` starts it for each test on a temporary data
  directory and a free port, with an environment of its own — nothing from
  your shell's `FOYER_*` — so every test starts with no users and a fresh
  rate limiter. The superuser comes from the binary's own `superuser upsert`,
  and makes users as the dashboard does.
- `test/support/host.ts` is a device as far as an adapter can tell: the
  app's host context rebuilt on Node — `fetch`, node:crypto, a session that
  outlives a provider — with every exchange recorded, so a test can count
  the sign-ins.
- `@loge/api` and the adapters are aliased to their source in
  `../../loge/adapters` (`vitest.config.ts`, `tsconfig.json`), never
  installed: the workspace has the app beside Foyer. A new adapter is a new
  alias in both. The app's version is
  read from `../../loge/package.json`; the server must answer it.

## Rules

- **Never import the app.** An adapter, through its public `plugin` export,
  and `@loge/api`: nothing else.
- **Never reach into an adapter's internals** — its session format, its ids.
  What the harness checks is what a device would see.
- **Mind the rate limits.** They are on, as in production: five password
  sign-ins a minute from one address. Each test has its own server; keep a
  test under that, or make throttling its subject.
- **The Go tests prove the server; this proves the pair.** A rule belongs in
  the Go tests first. Add a scenario here when an adapter and the server
  could disagree about it.

## Run

```bash
npm install
npm run typecheck
npm test          # builds the binary, then every scenario against it
```
