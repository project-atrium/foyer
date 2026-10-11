# AGENTS.md — foyer

The household's own server: PocketBase, used as a Go framework. Read the
workspace root `AGENTS.md` and `../.claude/architecture.md` first, then
`../.claude/new_architecture.md` — what Foyer is growing into — and
`docs/api`, every route, collection and rule.

---

## What this is

**One Foyer is one household, and a PocketBase user is one profile** —
Alex, Sarah, Kids. The household's superuser makes them in the dashboard.

Foyer is becoming the household's media server: its library from a WebDAV
share — Zurg's above all — named by TMDB and probed by ffprobe, watch status
per user, and streams for Loge.
`README.md`'s "Current state" says how far that has got.

## What this is not

- **Not required.** Loge works without it: a device's account can be local.
- **Not where files are renamed, moved or stored.** Media stays on the
  share — in Zurg; Foyer reads it, names it in its own records, and streams
  it through.
- **Not a place for what belongs to a device.** Players, sync settings and
  caches never reach it.
- **Not an account that holds profiles.** That protocol went with Step 1, and
  Loge's `sync/custom-server` with it in Step 2. Loge's `sync/foyer` signs in
  as the household's users instead: a device with two profiles signs in as
  two users, each with a session of their own, and each user's PIN is on its
  record.

## Boundaries

- **PocketBase and Go. Nothing from TypeScript.** Go cannot import
  `@loge/api`. Where Foyer keeps something Loge also computes — a watch
  identity, a title key — it is ported, and its tests hold it to the same
  answers.
- **Never the app.**

## What the server promises

1. **A user reads and changes only itself.** `users`' rules are
   `id = @request.auth.id`; only a superuser makes or removes one.
2. **A PIN is its own user's.** Four digits or none, readable by that user so
   a device signed in as several users can ask for it, and by nobody else —
   another member sees a name, a picture and whether a PIN is set
   (`GET /api/foyer/household`), never the record.
3. **Sessions last 30 days**, and a password change ends every one.
4. **Signing in is throttled per address** — never per username: anyone who
   knows one could keep its owner out.
5. **`info` answers without a session.**

## What it must never do

- **Open a browser.** PocketBase opens a tab to make the first superuser
  whenever `serve` starts without one — on whoever started it, and once per
  test. `Bind` turns its installer off; the superuser comes from
  `FOYER_ADMIN_*` or `superuser upsert`. A test app skips the installer by
  itself, so the test that holds this arms it again first.
- **Lock a user out from everywhere.** PocketBase's limiter counts per
  address, in memory. Never add a count per username.
- **Tell which pairing codes exist.** Unknown, spent and expired codes are
  refused alike; a code is kept as its hash, in memory, for ten minutes, and
  spent the moment it is used.

---

## Shape

```
main.go              .env, then pocketbase.New(); migratecmd, automigrate off; server.Bind
go.mod               PocketBase pinned to v0.40.4, which needs Go 1.27
internal/config/     FOYER_ADMIN_*, FOYER_TRUST_PROXY, and the .env reader; the old SC_* names refused, MAX_PROFILES and SIGNUP named as doing nothing
internal/routes/     GET /api/foyer/info — the version, the protocol, the household
internal/household/  the household: its record, members as others see them, devices, pairing codes (memory)
internal/server/     Bind: the guard, the routes, the trusted proxy, no installer, the UI extension — and the HTTP tests
internal/guard/      Foyer's collections refuse every write request, a superuser's too; users' rules stay
internal/adapters/   what Foyer connects to: adapters by category, the connections set up of each, fields, masked
                     secrets, the Adapters page's routes; the adapters still to come, planned
  tmdb/              TMDB: Loge's matcher ported (Find), a film's and a series' details, seasons, episode groups, artwork
  webdav/            a WebDAV share — Zurg's above all: its folders, an entry's files, a file's bytes with ranges
  webdav/webdavtest/ a pretend share, answering as Zurg does where asked, for the tests
internal/titles/     Loge's titleKey and bareTitle, ported to the byte; compareItems' keys — JavaScript's lower case,
                     UTF-16 order — and its fold
internal/parse/      release names: title, year, season and episode, numbers counted from the start, what the file is
internal/library/    libraries: their collections from one template, the scan — walk, name, identify, place,
                     sweep, artwork — corrections, the dashboard's library routes, and play and download
internal/media/      what a file is: Loge's MediaVersion, from a release's name or ffprobe
internal/stream/     signed stream addresses, the Range proxy, the quiet log, the loopback for tools
internal/hls/        remuxing to HLS: a file's keyframe index — Matroska's Cues, an MP4's sync samples, read by
                     ranges — its codec's RFC 6381 name, what play decides for a player's profile, the playlists,
                     the signed ticket in their path, and a job per playing running one ffmpeg
internal/copies/     smaller copies for downloads: a device's settings read for a file, the recipe — AV1 and Opus
                     by default — ffmpeg's command, the jobs, the copies kept in pb_data/foyer_copies, their
                     addresses under the stream prefix
internal/sessions/   playings: the one every address belongs to, what plays now and what played, a superuser's Stop —
                     Live Activity's routes
internal/ffmpeg/     the tools: where, which versions, the GPU tried; the FFmpeg adapter
internal/probe/      ffprobe's answers as versions, and the queue of files waiting for it
internal/watch/      each user's watch status: Loge's rules, the routes, an identity's history moved, extras with it
internal/userstate/  what each user keeps for themselves — preferences, follows, lists, ★ — pushed whole and read back
                     by seq, their name and PIN onto their own record; tombstones let go of after 90 days
internal/catalogue/  the library as Loge reads it: keyset pages in compareItems' order, a title's page, seasons,
                     episodes and extras, genres, what was begun — each with its user's watch status; what is served
internal/identity/   Loge's watchIdentity and identityHash, ported
internal/dashboard/  the dashboard's icon and pages — Library, Live Activity (playings, and the copies being made
                     and kept), Settings → Adapters and Household: a PocketBase UI extension, plain JavaScript,
                     embedded
internal/version/    the version, always the app's: its `npm run release` moves both; the protocol Loge's adapters speak
internal/fixtures/   reads what both sides must answer alike from the app beside Foyer; tests only, skipping without it
migrations/          0001 users are profiles; 0002 the name, the rate limits, the first superuser; 0003 adapters;
                     0004 libraries, media_entries, media_files, identities; 0005 streams' rate limit;
                     0006 watch_states; 0007 adapters become connections, a library a folder of one;
                     0008 the household's record, devices, pairing's rate limit; 0009 pictures' rate limit;
                     0010 each user's preferences, subscriptions, playlists, ★, and the seq; 0011 the history of playings;
                     0012 each file's keyframes
dev/fakezurg/        a pretend Zurg for running Foyer here, from Zurg's .zurgtorrent records; never in the image
harness/             Node and vitest, test-only: the app's code that speaks Foyer against the real binary
Dockerfile           two stages: a Go build, then Debian with Jellyfin's ffmpeg and the binary
docker-compose.yml   the server, and pb_data in a volume
compose.gpu.yaml     /dev/dri passed in, and the host's render group
```

Go tests sit beside what they test. Everything else is PocketBase's: `serve`,
`superuser`, `migrate`, users and sessions, the record APIs, the dashboard at
`/_/`, backups and the rate limiter.

## Rules that break silently

- **Foyer's collections are Foyer's.** The guard refuses every request that
  would write one — the dashboard's, a superuser's, an import, a truncate.
  Foyer's code saves at the model level, which no request hook sees. A new
  collection of Foyer's joins the guard's list in `internal/server`.
- **An adapter is set up as connections.** A connection is one share, one
  key: `connections`, keyed by id, never by the adapter. Two WebDAV shares
  are two connections, and a library names its own (`libraries.connection`);
  read a share through `Engine.shareFor`, which keeps one client per
  connection while its settings stand — a client is a transport, a pace and
  a refusal latch, and a new one per request loses all three. A single
  adapter — TMDB, FFmpeg — is the household's once (`Registry.Settings`); a
  planned one is listed and cannot be set up. A share whose folders are
  libraries is never removed from under them (`Registry.OnDelete`).
- **A collection name Foyer will make is reserved before its migration
  lands.** A library takes its collection's name, and a migration that meets
  a library of the name it makes stops — and `serve` with it. `reserved` and
  `reservedPrefixes` in `internal/library/template.go` hold every name Step 2
  makes, and `iptv_` and `youtube_`; a migration making a new collection adds
  its name there first, in an earlier release.
- **A library's collections are Foyer's, from one template.** `x`, or `x` and
  `x_episodes`, made and kept by `library.Ensure` — never by a migration,
  which cannot see them, and never in the dashboard. Change the template,
  raise `TemplateVersion`, and every library is brought up to it at start.
  Never change a field's type: add one under a new name. A new collection
  takes a random id, because PocketBase removes a deleted one's files in the
  background, under its id.
- **An `uncertain` title is hidden from users**, its episodes with it, by the
  collections' rules — and its files by `media_files` being the superusers'
  alone. Whatever a user reads of a title — its versions, their paths — is on
  the title itself.
- **No network inside a transaction.** The share, TMDB and the artwork
  servers are asked first; then one transaction writes. A file field's save uploads to
  storage — S3 too — so artwork is saved on its own, outside any. A library's
  writes — its scan, its artwork, a probe's results — take its lock, so none
  overwrites what another changed.
- **TMDB is asked once per title**, and the answer kept in `identities` by the
  title's key: a superuser's word (`manual`) is never asked again, a guess
  (`uncertain`) waits for one, and "nothing" is asked again after a week.
  TMDB off, or its key refused, is no answer: the title is its name's until
  TMDB can be asked.
- **One torrent never stops a scan.** One that will not be kept is told in
  the scan's notes and left for the next scan, and the rest of the library
  is placed.
- **What the share stops listing is kept three days** — Zurg's repairs take
  files away for a while — then let go with any title left with nothing;
  never on a listing that came back empty, nor when most of a library
  vanished, until the next scan agrees. Watch status stays.
- **A stream outlives PocketBase's deadlines.** Its server reads and writes
  with five-minute deadlines, which would end a film five minutes in; the
  proxy lifts both, and a write that stalls half an hour ends it instead.
- **A stream address is a key, and never logged.** It opens its file to
  whoever holds it for six hours. `stream.Quiet` runs inside PocketBase's
  activity logger and answers every stream request itself — a refusal, a
  429, a panic — so the logger has nothing to keep. A test reads the log for
  anything under `/api/foyer/stream/`, where every signed address lives.
- **Every address belongs to a playing, and only a superuser's Stop refuses
  one.** `/play` and `/download` make a playing and sign its id into the
  address (`s`); the stream route opens each request under the playing, so a
  Stop cancels what streams. A playing lives while its bytes flow or its
  player reports — a phone asleep under AirPlay or picture in picture
  reports nothing and still pulls bytes — so quiet is only shown, and a
  playing quiet with nothing open is let go, never refused: its next request
  brings it back as it was. A player's stop ends a playing only once no
  stream of it is open, since a stop under its id while one runs may be an
  older playing's, sent late. Playings are in memory; `play_history` keeps
  which were stopped, and `Load` reads them at start, so a restart refuses
  them still.
- **A file's keyframes are read from its own index, never by reading it
  through.** Matroska's Cues, found through its SeekHead — usually at the
  end — and an MP4's sync samples, decode times, composition offsets and
  edit list, by ranged reads (`hls.ReadIndex`); behind Zurg every byte is
  Real-Debrid's. A file without one is kept as `none` and plays as it is:
  never ffprobe over the whole file to make one. Kept on the file
  (`Engine.Keyframes`, once however many ask), and read again when the file
  changes under its name. Raise `hls.IndexVersion` when what is kept changes.
- **A remux keeps the file's own clock, in every output.** One ffmpeg per
  playing writes the picture, each sound and each subtitle track to outputs
  of their own, and ffmpeg shifts each output's timestamps apart to keep
  B-frames from going below zero — 83 ms of sound out of step. So every
  output is `-avoid_negative_ts disabled` with `-copyts`, and the segments
  are fragmented MP4 with signed composition offsets
  (`negative_cts_offsets`) and absolute decode times (`frag_discont`); the
  segmenter counts its cut times from a run's first packet, so they are
  given from there; and ffmpeg seeks 3/23 s before what it is asked in a file
  with B-frames, so a run is asked for just past its keyframe
  (`seekLeadMs`). Each run writes files of its own: one that passes
  segments an earlier one made never writes over one being read. Held by
  tests on the files ffmpeg makes, AVPlayer and hls.js having played them.
- **A remux's renditions do not keep pace with each other.** ffmpeg copies
  a picture far faster than it makes a sound anew, so a run waits only once
  its slowest rendition is far enough ahead (`slowest`), keeps what it made
  ahead, and only the picture's requests move it: a player asks for a sound
  it is not playing too, ahead or behind. **Known, still to mend:** a
  subtitle output in the same ffmpeg — copied or made WebVTT — holds back
  every sound made anew until the file ends when the share is slow, because
  ffmpeg keeps its encoders in step with the sparse subtitle stream; copies
  are not held. The mend planned: each sound made anew in an ffmpeg of its
  own, fed the copied track through a pipe.
- **A remux's address carries its ticket in the path**, signed — AVPlayer
  resolves a segment's relative address without the playlist's query — and
  lives under `/api/foyer/stream/`, so `stream.Quiet` keeps it from the log.
  The plan is in the ticket, so any Foyer — this one after a restart —
  makes the same remux of it.
- **Downloads made smaller come first.** A copy is made to a device's
  settings, once for whoever asks the same (`copies.Key`: the file as it is
  now and the settings as they matter for it), from the file read whole
  through the loopback — behind Zurg every byte is Real-Debrid's, so it is
  made only when asked for, never ahead. Its options are offered only where
  the copy comes out a fifth smaller at least. Copies live in
  `pb_data/foyer_copies`, left out of every backup by `OnBackupCreate`'s and
  `OnBackupRestore`'s `Exclude`; never put them anywhere a backup reads.
  Raise `copies.RecipeVersion` when what ffmpeg is told changes what comes
  out: copies made before are made again.
- **A copy keeps what is worth keeping, and nothing twice**: one sound per
  language and no commentary, text subtitles over pictures of the same
  kind, the chapters and a styled subtitle's fonts — and never mkvmerge's
  statistics, which would be wrong. Dolby Vision alone gets no copy: made
  anew without its own processing it shows in the wrong colours.
- **Converting for players is plan z, off until the household turns it on**
  (the FFmpeg adapter's `convert`). mpv and VLC, first on every device, open
  a file as it is; off, `play` answers the file with `convertible`, and the
  app says converting could be turned on. Never turn it on by default, and
  never spend on it what downloads and YouTube need first.
- **A remux carries every sound the file holds, so only a file ffprobe has
  read is remuxed** — `play` reads one it has not, at once — and only one
  whose keyframes are known. Never remux by the name's guess: a silent film
  is worse than "this needs another player".
- **A tool reads files through the loopback**, never from the share: a grant
  on 127.0.0.1 opens one file for minutes, so ffprobe's arguments and output
  never hold the share's address or password.
- **ffprobe reads through the loopback, with nothing of Foyer's
  environment** but the PATH and libva's `LIBVA_` settings — ffmpeg reads
  `http_proxy` — and only http: a file that is really a playlist must not
  send it elsewhere. What it says on failure is cleared of addresses before
  it is kept. The share failing is no file's fault: it costs no attempt, and
  five in a row pause the queue. A pack's probes redraw its series' lists
  once a batch, never once a file.
- **Watch status is kept by identity, never by a record.** A title's record
  comes and goes with its files; what was watched stays, under
  `tmdb:movie:603` or `tmdb:tv:1396/s1e3`, and comes back with it. Where a
  title's identity changes, `watch.Move` gives its history to the new one —
  only once the change is kept, and never while another title still goes by
  the old one. A guess has no identity, so nothing is kept for it.
- **Watch status follows Loge's rules, on the server**: a stop at 90 % or the
  end is watched and watched holds; a report at 0 never lowers a position; a
  playing's end sent twice counts once. Nothing decides by a device's clock.
- **A rate limit's place matters.** PocketBase takes the first rule whose
  prefix matches, in order: a narrower prefix goes before `/api/`.
- **One scan at a time**, from one loop that runs only beside `serve`
  (`Background`): SQLite has one writer, and nothing a test binds starts it.
- **No admin route answers 401 for a service's refusal.** The dashboard takes
  any 401 for its own session ending and signs the superuser out: a test that
  failed is `200 { ok: false }`.
- **A key in an address never reaches a log or an error.** TMDB's API key
  travels in the query, so the client's errors drop the address Go's
  `url.Error` would print.
- **A library is read from a WebDAV share** — Zurg's above all, whose
  folders, the ones its config sorts torrents into, exist only there. The
  address is the share's WebDAV root, Zurg's `/dav` included: nothing is
  added to it. An entry — a torrent on Zurg — may have its files listed flat,
  with ` (id)` added where two share a name, or under folders of their own:
  walk both. Zurg writes a child's href as its bare escaped name — read
  against the folder asked for, a colon and all — and its dates in `UTC`, not
  HTTP's `GMT`. It answers a part with `200` and a `Content-Range` at times —
  that is a `206` — and a whole file answered for a part is refused, or a
  player would start again from the beginning. It sends every file as a
  part, one not asked for too, in chunks with no length: the proxy answers a
  player that asked for no part `200`, and gives every part its length, or a
  player cannot seek. A pack holding a file of one name in several of its
  folders is listed with that name once per folder, and Zurg serves one of
  them by it, not saying which: one name is one file, its size unknown where
  the copies differ. Never ask Zurg for gzip.
- **A release's name is read, never trusted.** `internal/parse` reads what
  the uploader typed; the library checks it against TMDB, and a superuser
  corrects the rest. A season folder never takes a number counted from the
  series' start — a pack's "Season 01" can hold episode 62 — and Zurg's
  ` (id)` suffix is stripped before anything is read.
- **PocketBase answers in `encoding/json/v2`**, where `omitempty` leaves out
  only what is empty as JSON — `""`, `null`, `{}`, `[]` — and a `0` or a
  `false` goes out. An optional number or flag in an answer is `omitzero`:
  a rating of 0 sent is a rating the app sorts by.
- **A user's own state is pushed whole and read by seq.** A push stores all
  its records or none, each judged as the app's `isAccountRecord` judges it,
  and every write takes the next seq inside its transaction — SQLite's one
  writer makes that commit order. The seq lives in `_foyer_seq`, never on a
  record: nothing else may ever write it back. A read never pages past the
  seq it began at, and answers `reset` rather than skip what it cannot show.
- **The catalogue is what Loge reads, and pages by keys.** Its order is
  `compareItems`' to the byte, a page is what sorts after the last one's keys
  — never `OFFSET`, which a scan between two pages would shift — and what a
  user may not see is never in it: a guess, a title whose files are gone, a
  library switched off. A field a title lacks is absent, never empty.
- **Titles are keyed as Loge keys them.** `internal/titles` is `@loge/api`'s
  `titleKey` and `bareTitle`, and its tests are Loge's: change one, change
  both.
- **A title's keys are a hook's, never its writer's.** `sort_title_key`,
  `sort_release`, `sort_rating`, `search_text` and `genre_keys` are set on
  every save of a title (`library/keys.go`), and `Backfill` gives them at
  start, 500 at a time, to a title saved before they existed. They are
  `compareItems`' order as bytes — JavaScript's lower case, `İ` and a final
  sigma included, in UTF-16 order — held to the app's `order.json`. Never
  order what Loge reads by `title` or `rating` themselves: SQLite's order is
  not JavaScript's, and the app merges pages by its own.

- **Migrations sort by file name, as text.** `10_x.go` runs before `1_x.go`,
  so names are zero-padded: `0001_…`, `0002_…`. A server that ran one never
  runs it again: never edit one that has shipped — fix forward with a new
  one. Automigrate stays off, and nobody changes the collections in the
  dashboard: a field added there exists on one server only, and a rule
  loosened there opens the data.
- **Two registrations in one migration file collide.** PocketBase names a
  migration after the file that registered it.
- **A new field is optional.** An older client never sends it.
- **`FOYER_TRUST_PROXY` only behind a proxy.** Without one, anyone can claim
  any address and slip the limiter. Behind one without it, every caller has
  the proxy's address, and one stranger's tries throttle the whole household.
- **The names from before Foyer are refused.** `SC_TRUST_PROXY` and the rest
  became `FOYER_…`; read silently, an old `.env` would drop its proxy header,
  so `FromEnv` refuses to start and names the new one. `FOYER_MAX_PROFILES`
  and `FOYER_SIGNUP` are only named: ignoring them weakens nothing.
- **PocketBase takes a bad token for a guest.** A missing, expired or invalid
  token is no error to it: the request goes on as a guest, and a guest's list
  under owner rules is empty. Where a client must tell "nothing" from
  "signed out", it refreshes the session.
- **PocketBase is pinned.** It is pre-1.0, and minor versions break its Go API.
  Update deliberately, one version at a time, with the Go tests.
- **The dashboard's icon and pages are a UI extension**, which PocketBase
  marks experimental (`core.ServeEvent.UIExtensions`) and documents not at
  all. `internal/dashboard/ui` is plain JavaScript with no build step,
  written as PocketBase's own pages are — Shablon's `t`, `store` and `watch`,
  `app.components`, `app.pb.send`. A test holds PocketBase's bundle to every
  `app.…` name the pages use, so an upgrade that renames one fails there
  instead of quietly breaking a page.
  - **`main.js` is pasted into a function** in `/_/extensions.js`: no
    `import` or `export`. It registers Foyer's routes at once — the router
    starts as soon as it has run — and each page's module loads with
    `import()` when first opened.
  - **Shablon redraws a reactive child for every store value read while it
    builds.** Read values inside functions (`value: () => form.x`), never
    while making elements, or a form is redrawn as it is typed in and loses
    its focus.
  - **No `rid` on an element whose content is not reactive**: Shablon keeps
    the element it drew before, and drops the new one — a poster that
    arrived later never showed.
  - **A form's own setup reads nothing back from its store.** A panel built
    inside a reactive child that reads `form.name` while it is built makes
    that child redraw — and the form with it, a fresh store under the typed
    name — at every keystroke. Write the store from the values given; read
    it only inside functions and handlers.
  - **Never register `#/settings`**: it would replace PocketBase's own
    Application page. Foyer's settings come first by building
    `settingsNavGroups` as `{Foyer, ...groups}`, and the header's Settings
    link points at Foyer's page with an `isActive` for any `#/settings`.
  - **A page that polls redraws nothing but what changed.** Live Activity
    keeps each playing as it first came and a store of what a poll changes,
    written in place: rebuilt every two seconds, every poster would be
    fetched again.
  - **`--fieldsGap` is not PocketBase's everywhere** — only inside some of
    its forms. `foyer.css` gives it its 10px wherever it uses it.
  - **Foyer's files are served `no-cache`** (`dashboard.Fresh`): outside
    `--dev` PocketBase lets a browser keep `/_/` two weeks, and last
    version's pages would call this version's routes.

## Current state

**Users are profiles, and the library runs.** PocketBase v0.40.4, used as a
Go framework: `users` as profiles with their PINs; a WebDAV share's folders
— Zurg's — as libraries, named by TMDB, read by ffprobe and streamed through signed
addresses; each user's watch status; the catalogue Loge reads it by, in
the app's own order; the dashboard's Library and Adapters
pages — adapters set up as connections, several shares among them, Settings
opening on Foyer's own. `go test ./...` proves it, and the dashboard was driven headless in
Chrome against a pretend Zurg and the real TMDB. The account-sync protocol is
gone — git has it; its successor's fixtures and harness are back: the app's
`adapters/api/fixtures/foyer/` read by the Go tests, and `harness/` driving
the app's code against the binary. The image —
Debian with Jellyfin's ffmpeg — was built and run on arm64 under Colima,
against the household's real Zurg; the GPU's override is untried on a GPU.
