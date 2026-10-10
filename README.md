# Foyer — your household's own server

A small server you run yourself for [Loge](https://github.com/Loge-Foyer/loge):
your household's profiles, and — as it grows — your library, what each of you
watched, and the streams.

It is [PocketBase](https://pocketbase.io), with Foyer's collections and code
added: one program, one database, and a dashboard to see and set what it holds.

---

## The problem it solves

A household that keeps its films on Real-Debrid usually runs a chain of
things to watch them: Zurg to reach the files, CineSync to give them names a
media server understands, Jellyfin to turn them into a library with profiles
and watch status, and a few more beside it. Each keeps its own idea of who is
watching and what they watched.

Foyer is growing into the one place for that:

- **Profiles are its users.** One Foyer is one household; each person signs
  in as themselves, with a PIN as a child lock if they want one.
- **Your library, from Zurg.** Each Zurg folder you choose — `movies`,
  `shows` — becomes a collection of films or series, named by TMDB, with
  posters, the right release dates, and what each file is: its quality, its
  codecs, its audio and subtitle languages. Nothing is renamed or moved: the
  files stay where Zurg has them.
- **What you watched**, per person, kept by Foyer.
- **Streams for Loge**, through Foyer, so Zurg's address and password never
  leave the server.

## What it will and will not do

**Will:** keep the household's profiles and their PINs; read your Zurg
folders, name what is in them with your own TMDB key, and keep the artwork;
probe each file with ffprobe; hand Loge a stream it may play; show all of it
on its dashboard, where you set it up.

**Will not:** rename, move or download your files; keep your passwords from
whoever runs it.

That last one deserves plain words. **Zurg's password and your TMDB key are
stored as you typed them**, and so are the PINs. Whoever has the server's data
folder, a backup of it, or its dashboard login can read them. Your profiles'
passwords are different: the server keeps only a hash of each. So run it
yourself, put it behind TLS if it leaves your home network, keep the
dashboard to yourself, and treat its backups like a list of passwords.

## Running it

With Go 1.26 or later:

```bash
go run . serve --http=0.0.0.0:8090
```

Then make the dashboard's superuser, and a user for each person.
`docs/getting-started` walks through both, and through Docker.

## Current state

**2026.10.1.** PocketBase v0.40.4, used as a Go framework.

- **A user is a profile**: a username, a name, an avatar and an optional
  four-digit PIN, made by the superuser in the dashboard. Each signs in with
  their own password; sessions last 30 days.
- **The account that held up to ten profiles is gone**, with the protocol
  Loge's `sync/custom-server` adapter spoke. Loge signs in to Foyer anew in
  Step 2 — a device with two profiles signs in as two users.
- **Adapters**: Zurg's and TMDB's settings kept on the server, their
  secrets masked; Zurg read over WebDAV; a TMDB client that names films and
  series as Loge does, with their details, seasons and artwork.
- **The library**: each Zurg folder you choose is a library, scanned every
  15 minutes and when Zurg says: films and series named by TMDB, episodes laid
  as TMDB numbers them — anime packs counted from the start included — with
  their artwork, dates and what each file's name says it is. A guess stays
  hidden from users until you confirm it; corrections stick.
- **Being built**: ffprobe, streams, watch status, and the dashboard's
  Library and Adapters pages.
- **The dashboard** wears Foyer's icon.
- **`go test ./...` proves it.**
- **The Dockerfile and compose file are written**, and not yet built.

## Documentation

`docs/` covers getting started, the routes and collections, deployment and
development.

`CLAUDE.md` and `AGENTS.md` are written for AI coding assistants — denser, and
full of rules. This file is the one written for you.

---

## Licence

**AGPL-3.0-or-later**, like the app: one project, one licence. Anyone may
run Foyer, change it, and host it for others — for money too — as long as
they give those users its complete source, under the same licence. A renamed
copy owes it just the same, and none may be closed.

One additional term (`NOTICE`, under the licence's section 7(b)): a work
based on Foyer keeps "Based on Foyer" and a link to this repository.
