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
- **Your library, from Zurg** — or any WebDAV share: rclone's, a NAS's.
  Each folder you choose — `movies`, `shows` — becomes a collection of films
  or series, named by TMDB, with posters, the right release dates, and what
  each file is: its quality, its codecs, its audio and subtitle languages.
  Nothing is renamed or moved: the files stay where the share has them.
- **What you watched**, per person, kept by Foyer.
- **Streams for Loge**, through Foyer, so the share's address and password
  never leave the server.

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
- **The household**: its name, its members as the others see them — a name,
  a picture, never a PIN — and the devices they are signed in on; a pairing
  code, made on the dashboard's Household page, signs chosen users in on a
  device at once, once, within ten minutes.
- **The account that held up to ten profiles is gone**, with the protocol
  Loge's `sync/custom-server` adapter spoke. Loge's `sync/foyer` signs in as
  the household's users — a device with two profiles signs in as two users,
  by password or with a pairing code.
- **Adapters, set up as connections**: as many WebDAV shares as the household
  has — Zurg's, its ways included, or any other — each named and switched on
  its own, and TMDB's key and FFmpeg once; their secrets masked; a TMDB client
  that names films and series as Loge does, with their details, seasons and
  artwork. The adapters still to come are listed, planned.
- **The library**: each share's folders you choose are libraries,
  scanned every 15 minutes and when Zurg says: films and series named by
  TMDB, episodes laid as TMDB numbers them — anime packs counted from the
  start included — with their artwork, dates and what each file's name says
  it is. A guess stays
  hidden from users until you confirm it; corrections stick.
- **Streams**: a user's player is handed a signed address for a title's
  file, good for six hours; the bytes come through Foyer, so the share's
  address and password never leave the server, and no address reaches a log.
  Every address belongs to a playing — who, on which device, what, how far —
  and a playing you stop ends at once, its addresses refused from then on.
  A player that cannot open a file as it is — an iPhone's own player and a
  Matroska film, a browser — can be handed it remuxed to HLS, cut at the
  file's own keyframes, every sound and text subtitle a rendition, made as it
  watches: off until you turn it on under FFmpeg, since mpv and VLC open the
  file as it is.
- **YouTube through Foyer**: a household's devices play the site's videos
  through Foyer — yt-dlp, here, says where a video's streams are, and Foyer
  relays every playlist, segment and caption, since the site binds its
  links to the address that asked. So a phone away from home plays them,
  on every player. Browsing, and each user signed in for their own
  recommendations, come next.
- **Downloads made smaller**: a device keeping a film asks for a copy made
  to its own download settings — AV1 and Opus in Matroska by default, 1080p,
  a few GB where the file is tens — and Foyer makes it on the processor,
  once for whoever asks the same, tells the device how far along it is, then
  hands it over by ranges like any download. Copies are kept a couple of
  weeks after they were last asked for, within the room you give them, and
  never in a backup.
- **What each file is**, read by ffprobe through Foyer: its picture, codec,
  HDR, audio and subtitle tracks with their languages, chapters. The GPU is
  tried for the transcoding to come.
- **Watch status**, each user's own, kept by Foyer by Loge's rules — with
  resume, a series marked by its episodes, and its extras kept apart. A
  title TMDB names later, or you correct, takes what was watched with it.
- **What each of you keeps for yourselves** — preferences, the channels you
  follow, your lists, your ★ — on Foyer, and on every device you sign in on,
  each change in the order it was made; deletes are kept 90 days for a device
  that was away to learn of.
- **The catalogue Loge reads**: each kind across every library as one list,
  in the app's own order and paged by it, so a scan between two pages moves
  nothing; a title's page, a series' seasons and episodes and its extras,
  the genres, and what each of you began — and the household's answer says
  Foyer serves its library.
- **The dashboard** wears Foyer's icon, and is where Foyer is set up:
  **Settings → Adapters**, which Settings opens on — the shares, TMDB and
  FFmpeg by category, each tried where it is set, and which of each share's
  folders are libraries; **Library** for every
  title as a poster, the newest added first, a title's files and what TMDB
  said of it, and Confirm or Set TMDB id where it guessed; **Live Activity**
  for what plays now — who, on which device, what, how far, at what rate —
  with Stop; the copies being made for downloads, how far along and how
  fast, with Stop, and those kept, to let go of; and what played before.
- **`go test ./...` proves it.**
- **The image** — Debian with Jellyfin's ffmpeg, for VAAPI and Quick Sync —
  builds and runs, with its compose files; the GPU's override is untried
  on a GPU.

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
