# Getting started

Foyer is the household's own server: each person in the household is one of
its users, and its library comes from Zurg. It is PocketBase, with
Foyer's collections added.

## Run it

With Go 1.26 or later:

```bash
go run . serve --http=0.0.0.0:8090
```

The first build fetches the Go toolchain PocketBase needs. The server listens
on port 8090 and keeps everything in `./pb_data`. Without `--http` it listens
on this computer only: enough for a browser, the iOS simulator and the Android
emulator here, but not for a phone.

With Docker:

```bash
cp .env.example .env
docker compose up -d
```

and `docker compose exec foyer foyer …` wherever `go run . …`
appears below. `docs/deployment` has the settings, TLS and backups.

## The superuser

The dashboard, at `http://localhost:8090/_/` and wearing Foyer's icon, needs
a superuser: the household's administrator. Either of these makes it:

- `FOYER_ADMIN_EMAIL` and `FOYER_ADMIN_PASSWORD` in `.env`, before the first start
- `go run . superuser upsert you@example.com 'a long password'`

Foyer never opens a browser to ask for one, as plain PocketBase does when it
starts without a superuser.

A superuser sees everything the server holds, every secret included. Keep
that login to yourself.

## The household's users

Each person is a user — a profile. In the dashboard, open **users** and add
one for each:

- **username** — what they sign in with; case does not matter
- **name** — what the household calls them
- **password** — at least eight characters
- **pin** — four digits, if they want a child lock on their profile; empty
  for none
- **avatar** — a picture, if they like

A user can change their own name, avatar, PIN and password; only the
superuser adds or removes one. A forgotten password is set again in the
dashboard, which ends every session that user had.

## The library

Everything else is set on the dashboard, under **Settings → Adapters**. Each
adapter is switched on with **Enabled**, tried with **Test**, and kept with
**Save changes**.

1. **Zurg** — its address, such as `http://zurg:9999`, and its username and
   password if it asks for them.
2. **TMDB** — your own key, from themoviedb.org → Settings → API: the API
   Read Access Token or the API key. And the language titles and artwork
   are asked in.
3. **FFmpeg** — where ffprobe and ffmpeg are, if not on the PATH. Its test
   says what it found, and whether the GPU answers.
4. **Libraries**, under Zurg: its folders. **Make a library** of each you
   want, as movies or as shows; it is scanned at once. A folder that is not
   one — `__all__`, a download folder — is never read.
5. **Scan as soon as Zurg changes**: paste the `on_library_update` shown
   there into Zurg's `config.yml`, with the address Zurg reaches Foyer by.

Then **Library**, in the header: every title as a poster, the newest added
first. A title TMDB could only guess at is marked, and hidden from the
household until you open it and **Confirm** — or **Set TMDB id** to the one
it is. `docs/library` says how a scan reads Zurg.

## Addresses

- A browser on this computer, and the iOS simulator: `http://localhost:8090`.
- The Android emulator: `http://10.0.2.2:8090`.
- A phone on the same network: the computer's address,
  `http://192.168.x.x:8090`, with the server started with
  `--http=0.0.0.0:8090`.
- Anywhere beyond a trusted home network: behind TLS (`docs/deployment`).
