# Deployment

Running the server for real: how it runs, where it keeps its data, how it is
reached, how it is backed up, and how the household's users are looked after.
`getting-started` has the quick version.

The binary is `foyer`. In the image it is on the `PATH`, the
data is `/pb_data`, and it listens on 8090. The Dockerfile and compose file
are written and not yet built — Docker was not there when they were — so try
them before you rely on them.

## The binary

```bash
CGO_ENABLED=0 go build -o foyer .
./foyer serve --http=0.0.0.0:8090 --dir=/var/lib/foyer
```

One file, with nothing to install beside it. `GOOS` and `GOARCH` build it for
another machine. Run it under whatever keeps your services up — systemd,
launchd — and stop it with `SIGTERM`.

## With Docker

```bash
cp .env.example .env
docker compose up -d
docker compose exec foyer foyer superuser upsert you@example.com 'a long password'
```

- The image is a two-stage Go build of this repository alone.
- The data lives in the `pb_data` volume.
- It listens on 8090, and reports its health from `/api/health`.
- `docker compose exec foyer foyer …` runs any command —
  `superuser`, `migrate` — beside the running server.

Without compose:

```bash
docker build -t foyer .
docker run -d --name foyer -p 8090:8090 -v pb_data:/pb_data --env-file .env foyer
```

## Settings

`.env`, which the server reads when it starts; compose passes it to the
container. Zurg, TMDB and ffmpeg are not here: they are set on the
dashboard's Adapters page.

| Variable | Default | |
| --- | --- | --- |
| `FOYER_ADMIN_EMAIL`, `FOYER_ADMIN_PASSWORD` | — | The first superuser, made on the first start. Take the password out of `.env` once it exists; `superuser update` changes it later. |
| `FOYER_TRUST_PROXY` | — | Behind a reverse proxy: the header it puts the caller's address in, such as `X-Forwarded-For`. Never without a proxy (below). |

- **Until 2026.10.1 these were `SC_…`.** A server started with an old name
  refuses to start and says the new one, rather than quietly running without
  its proxy header.
- **`FOYER_MAX_PROFILES` and `FOYER_SIGNUP` do nothing any more**: a user is a
  profile, and the superuser makes them. A server started with either says
  so once, and starts.

And PocketBase's own flags, after `serve`:

| Flag | Default | |
| --- | --- | --- |
| `--http` | `127.0.0.1:8090` | Where it listens. `0.0.0.0:8090` takes requests from other machines; the image does. |
| `--dir` | `pb_data`, beside the binary — or in the working directory, when the binary is called by its name | The data. |
| `--origins` | `*` | Which web origins may call it. If you narrow it, keep the web app's. |
| a domain: `serve foyer.example.com` | — | TLS of its own (below). |

**The migrations write the rest into PocketBase's own settings** on the first
start: the session length and the rate limits. The dashboard shows them.
Leave them as they are.

- **Sessions** last 30 days.
- **The rate limits**, per address:

  | What | At most |
  | --- | --- |
  | signing in | 5 a minute |
  | refreshing a session | 60 a minute |
  | anything else | 300 in 10 seconds |

`FOYER_TRUST_PROXY` is not a setting the migrations write: the server applies it
on every start, so `.env` stays the one place it is set.

## Storage

Everything is in the data directory, `pb_data`: the database, PocketBase's
logs, local backups, and certificates when it makes its own.

- **Keep it on a local disk.** SQLite's locking is not safe over NFS or SMB,
  and a Docker volume on one is no better.
- **One server per data directory.** Commands such as `superuser` can run
  beside it; a second `serve` cannot.
- **It is as sensitive as every secret the household gave it.** Keep it, and
  anything copied from it, where only you can read it.

## TLS

**Use TLS anywhere but a home network you trust.** On plain HTTP, anyone on
the network reads a password as a user signs in, and the session tokens that
open their profile.

### Behind a reverse proxy

Self-hosters mostly run one already. With Caddy on the same machine:

```
foyer.example.com {
	@dashboard path /_/*
	respond @dashboard 404
	reverse_proxy 127.0.0.1:8090
}
```

- Start the server with `--http=127.0.0.1:8090`, or publish it with compose on
  `127.0.0.1:8090:8090`, so the proxy is the only way in.
- Set `FOYER_TRUST_PROXY=X-Forwarded-For`.
- The dashboard stays off the proxy. Reach it on the machine itself, or
  through a tunnel: `ssh -L 8090:127.0.0.1:8090 you@server`, then
  `http://localhost:8090/_/`.

A base path works too: `handle_path /foyer/* { reverse_proxy 127.0.0.1:8090 }`,
and the app uses `https://example.com/foyer`.

### Its own certificates

```bash
./foyer serve foyer.example.com
```

PocketBase gets a certificate from Let's Encrypt, keeps it in `pb_data`, and
listens on 80 and 443. The name must point at this machine, both ports must be
open to the internet — Let's Encrypt checks from outside — and the server
needs the right to bind them: root, or `setcap cap_net_bind_service=+ep` on
the binary. With Docker, publish 80 and 443 instead of 8090.

The app runs on the web from a secure page, so a browser can reach only an
`https` server — or one on `localhost`.

## The trusted proxy

The rate limiter counts per address, and the superusers' allowed addresses
(below) go by address too. PocketBase takes the address from the connection,
unless `FOYER_TRUST_PROXY` names a header to take it from.

- **Behind a proxy, set it.** Otherwise every caller has the proxy's address:
  one stranger's tries throttle the whole household, and an address allowed
  for superusers lets anyone in through the proxy.
- **Without a proxy, never.** Anyone could send the header, and claim any
  address.

With `X-Forwarded-For`, PocketBase takes the last address in it: the one your
proxy added.

## Keep the dashboard private

`/_/` and a superuser login read everything: every user, every PIN, and every
secret, in plain text.

- **Keep it off the proxy**, as above.
- **Allow superusers only from your own network:** in the dashboard's
  settings, or with `foyer superuser ips 192.168.1.0/24`. The
  command writes the settings to the database, and a running server keeps its
  own copy: restart it afterwards. Behind a proxy, this works only with
  `FOYER_TRUST_PROXY`.
- **A long superuser password**, and none left in `.env`.
- **Never change Foyer's collections, rules or settings there.** The
  migrations own them, and a rule loosened by hand opens one person's data to
  another.

## Backups

PocketBase makes them, under Settings → Backups in the dashboard.

- **A backup is a ZIP of `pb_data`**, taken while the server runs, on demand
  or on a schedule that keeps the last few.
- **It goes to `pb_data/backups`, or to S3.** A copy on the same disk does not
  survive the disk: keep one somewhere else. S3's keys sit in PocketBase's
  settings; `--encryptionEnv` keeps those settings encrypted.
- **It holds every secret** the household gave the server, in plain text.
  Keep it like a password file: encrypted, and nowhere shared.
- **Restoring** from the dashboard replaces `pb_data` and restarts the server.
  By hand: stop it, put the backup's contents in place of `pb_data`, start it.
  Everything goes back to the moment of the backup: a user made since is
  gone, and a password changed since is the old one again.

## Updating

**Update the server before the apps.** A newer app may ask for something an
older server does not have yet.

**Back up first.** A new release may bring migrations. They run at its first
start, and going back to the old binary does not undo them.

PocketBase is inside the binary, pinned: updating this server is how
PocketBase is updated, never on its own.

## Users

The dashboard's `users` collection is the household: one user per person.

- **A forgotten password.** Open the user, and set a new one. Every session
  that user had ends; each of their devices asks for it once.
- **A lost device.** Change the password of each user signed in on it, the
  same way.
- **A forgotten PIN.** Clear the user's `pin` in the dashboard, or set
  another.
- **Removing a user.** Delete them, and everything that is theirs goes with
  them.
