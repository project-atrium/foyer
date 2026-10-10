# CLAUDE.md — foyer

The household's own server: PocketBase, used as a Go framework — its users are
the household's profiles, and its library comes from a WebDAV share — Zurg's.

## Reading protocol — before you plan, edit or run anything

1. `../.claude/architecture.md`, then `../.claude/new_architecture.md` — what
   Foyer is growing into.
2. `../CLAUDE.md` — how this repository and the app relate.
3. `AGENTS.md` here — imported below.
4. `docs/api/` — every route, collection and rule.

@AGENTS.md

## Why this exists

A household that keeps its films on Real-Debrid runs a chain — Zurg,
CineSync, Jellyfin — and each link keeps its own idea of who is watching and
what they watched. Foyer is the one server the household runs itself for all
of it: its profiles, its library, what each person watched, and the streams.

It is PocketBase because PocketBase already is much of it: users, password
sign-in and sessions, rules per user, file storage with thumbnails, cron, a
dashboard, backups and rate limits. The Go code is the rest, and stays as
small as that allows.

## Decided

`docs/README.md` records each decision and why. Change one there first.

**A user is a profile.** One Foyer is one household. The account that held up
to ten profiles is gone, and with it the protocol Loge's `sync/custom-server`
spoke; Step 2 redesigns how Loge signs in — a device with two profiles signs
in as two users, and a user's PIN is on its own record.

**Secrets are plain text on this server.** The share's password, the TMDB key and
the PINs are stored as typed; a user's password is PocketBase's bcrypt hash.
So `pb_data`, its backups and a superuser login are as sensitive as every
secret the household gave it. Say so wherever it matters, and never quietly
weaken TLS, the dashboard's privacy or how backups are kept.

## The rule most likely to be broken

**Never edit a migration that has shipped**, and never let one sort out of
order: PocketBase compares file names as text, so they are zero-padded.

## Current state

**Users are profiles, and the library runs**, set up on the dashboard's
Adapters page. `README.md` and `AGENTS.md` say what runs. The image builds
and runs; the GPU's override is untried on a GPU.

## Git

This repository is `github.com/Loge-Foyer/foyer`: `origin`, on `main`.

- Commit here; never at the workspace root. Conventional commit style.
- Push only when asked, and never force-push `main`.
