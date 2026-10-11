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
| `GET /api/foyer/info` | anyone | `{ serverVersion, protocol, household: { id, name } }`: the version, always the app's, the protocol Loge's Foyer adapters speak (1), and the household — its id is what a device keys this Foyer by |
| `GET /api/foyer/household` | users | `{ household, members: [{ id, username, name, avatar?, hasPin }], served: [{ key, type, name }] }`: who else is in the household — names and pictures, never a PIN or an email — and what this Foyer serves: `{ key: "library", type: "library", name: "Library" }` while it has a library — one switched off is served empty, since a device lets go of what it kept of a thing no longer served; each device in the household runs it as a connection of its own |
| `POST /api/foyer/pair` | anyone with a code — five tries a minute per address | `{ code, device: { installation, name, platform, appVersion } }` → `{ household, users: [{ member, pin, token }] }`: the sessions of the users the code names, each with its own PIN; the code is spent; any code that will not do is the same `400` |
| `GET /api/foyer/admin/household` | superusers | `{ household, members, devices: [{ id, name, platform, appVersion, users, paired, lastSeen }] }` |
| `PATCH /api/foyer/admin/household` | superusers | `{ name }` → the household |
| `POST /api/foyer/admin/pairings` | superusers | `{ users: [ids] }`, one to ten → `{ code, expiresAt }`: eight characters, `XXXX-XXXX`, good once for ten minutes |
| `DELETE /api/foyer/admin/devices/{id}` | superusers | `204`: off the list — its sessions end only when its users' passwords change |
| `POST /api/foyer/play` | users | `{ collection, id, file?, profile?, startMs? }` → `{ url, expiresAt, session, mode?, file?, remux?, version, versions, chapters }`: a new playing — `session`, what its reports name — and its signed address, for the title's best version or the file asked for — one of its versions, or of a series' `extras`; versions never say where on the share a file lies; `404` for a title the user may not see. The device the call says it came from (`X-Foyer-Device`) is where Live Activity shows it. With the player's `profile` (Loge's `PlayerProfile`), where the player plays the picture and not the file, `url` is the file remuxed to HLS (`mode: "remux"`), starting at `startMs`, with what it carries (`remux: { audio, subtitles }`) and the file's own address beside it (`file`) — see Remux |
| `POST /api/foyer/download/options` | users | `{ collection, id, file?, encode? }` → `{ options: [{ id, width?, height?, bitrate?, estimatedBytes?, container, videoCodec?, audioCodecs?, hdr?, transcoded }] }`: the ways to keep it — first a smaller copy made to the device's download settings (`encode`: `{ video: av1 \| hevc \| h264, preference: size \| balanced \| quality, maxHeight?, maxBitrate?, hdr, audio: opus \| aac, container: mkv \| mp4 }`), where this Foyer makes copies and one would be a fifth smaller at least — its size once made, an estimate till then — then the file as it is, `original`. The file is read by ffprobe first where it was not; `400` for settings no copy is made to. See Copies |
| `POST /api/foyer/download` | users | `{ collection, id, file?, option? }` → `{ url, expiresAt, version }`: the file as it is — or, for a copy's `option`, the copy once made, `version` what it is — at a signed address asked for afresh each time: a download that stopped asks again and carries on from the bytes it has, with a `Range`; each ask is a download playing. A copy not yet made answers `202 { preparing: { state: queued \| running, progress?, etaMs? } }`; one that could not be made `422`, until asked again a minute later; `409` where Foyer makes no copy of the file now; `400` for an option it never offers |
| `POST /api/foyer/watch/progress` | users | `{ collection, id, file?, kind: started \| progress \| stopped \| end, positionMs, durationMs, paused?, session? }` → `204`; `file` names one of the title's extras, which keeps its own; `collection: "youtube"` a video of the site's, by its id; `session` is the playing `/play` made — its reports keep it on Live Activity, and its stop ends it |
| `POST /api/foyer/watch/played` | users | `{ collection, id, file?, played }` → `204`; a series marks each of its episodes, `file` one of its extras |
| `POST /api/foyer/me/push` | users | `{ records }`, at most a thousand of the user's own — `profile` and `pin` onto their own record, `preference`, `subscription`, `playlist` and `favoriteChannel` onto their rows, tombstones among them — each judged as the app's `isAccountRecord` judges it → `{ seq }`, all stored, each write with the next seq; or `422 { message, refused: { index, reason } }`, nothing stored |
| `GET /api/foyer/me/changes` | users | `?after=<seq>&from=<seq>` → `{ records, seq, more?, reset? }`: the user's own records stored since `after`, in that order, tombstones too, 500 to a page — `more` while another follows, from `seq`, with the same `from`; `after=0` is everything kept; `reset` where deletes since the read began (`from`, the first page's `after`) were let go of (after 90 days), or where `after` is past what this Foyer has — gone back to an older copy of itself: read again from 0. A later page's cursor never resets: a read of everything pages by old rows' seqs |
| `GET /api/foyer/catalogue` | users | `?kind=movies \| shows&sort=title \| releaseDate \| addedAt \| rating&order=asc \| desc&limit&cursor&term&genre` → `{ items: [title], nextCursor? }`: every library of the kind that is switched on as one list, 50 to a page (100 at most), in exactly Loge's `compareItems` order, each title with this user's watch status — a series' from its episodes. `nextCursor` holds the last title's keys, so a scan between two pages moves nothing; a guess and a title whose files are gone are never listed; `term` finds every word in a title's names and year, case and accents aside; `genre` is one genre, as Loge keys it |
| `GET /api/foyer/catalogue/genres` | users | `?kind` → `{ genres }`: every genre a kind's listed titles are filed under, each once, as TMDB spells it |
| `GET /api/foyer/catalogue/resume` | users | `?limit` (20) → `{ items: [{ kind: "movie", title } \| { kind: "episode", episode, show }] }`: what this user began past 30 seconds and has not finished, the latest first; a series' extra comes as one of its episodes |
| `GET /api/foyer/catalogue/{collection}/{id}` | users | `{ title, tagline, versions, extras, seasons? }`: a title's page — its files still on the share, the one to play first first, never where on the share they lie; a series' seasons; `404` for a guess or a library switched off |
| `GET /api/foyer/catalogue/{collection}_episodes/{id}` | users | `{ episode, show, versions }`: an episode's page — the episode with this user's watch status, its series as a page lists it, and its files still on the share |
| `GET /api/foyer/catalogue/{collection}/{id}/seasons` | users | `{ seasons: [{ key, number?, name, airDate?, episodeCount, posterPath?, unwatched? }] }`: what the series holds, specials as season 0, named as TMDB names them, and its extras last as `extras` |
| `GET /api/foyer/catalogue/{collection}/{id}/seasons/{season}` | users | `{ episodes }`: a season's in order, each with this user's watch status — or, for `extras`, the extras played as episodes: `id` `extra:<file>`, by the series' record and the file |
| `GET`, `HEAD /api/foyer/stream/{file}?u&s&exp&sig` | whoever holds the address | the file's bytes, a `Range` passed on: `206` with `Content-Range`; `403` when the signature is wrong or six hours old, or its playing (`s`) was stopped |
| `POST /api/foyer/videos/play` | users | `{ id, startMs? }` → `{ session, expiresAt, live, durationMs?, sources: [{ url, protocol: hls \| progressive, container?, videoCodec?, audioCodecs?, height? }], subtitles: [{ url, language?, label, auto? }], chapters }`: a video of YouTube's, played through Foyer — a playing, and its sources best first: the site's HLS master, every rendition, dub and caption of it, then the file the site muxed where it still has one; the uploader's captions and the one the site heard in the video's own language. Every address is Foyer's own. `409 { message, reason }` where the site will not play it — `unavailable`, `age`, `members`, `upcoming`, `blocked`, `failed`. See YouTube |
| `GET /api/foyer/videos/search` | users | `?q&scope=all \| video \| channel \| playlist&cursor` → `{ items: [{ kind: video, video } \| { kind: channel, channel } \| { kind: playlist, playlist }], next? }`: the site's own search, in its order — never a short, which has its channel's own tab; `next` is the site's token for the page after, asked again as `cursor` |
| `GET /api/foyer/videos/v/{id}` | users | `{ video, related }`: a video's page — its description, its channel with its face and followers, its views and when it went up, its length where yt-dlp said it — and what the site suggests after it |
| `GET /api/foyer/videos/channels/{id}` | users | `{ channel: { id, name, handle?, avatar?, banner?, followers?, videoCount?, description? }, tabs }`: a channel, and which of its tabs — videos, shorts, live, playlists — it has |
| `GET /api/foyer/videos/channels/{id}/{tab}` | users | `?cursor` → a page of the tab: its videos, shorts or streams, or its playlists |
| `GET /api/foyer/videos/playlists/{id}` | users | `?cursor` → `{ playlist, items, next? }`: a playlist — whose, how many videos — and a page of them |
| `POST /api/foyer/videos/feed` | users | `{ channels, cursor?, limit? }` → `{ items, next?, incomplete? }`: the newest videos of the channels a user follows — five hundred at most — newest first, from each channel's own feed, kept a quarter of an hour; `incomplete` while some are still being read |
| `GET /api/foyer/videos/resume` | users | `?limit` (20) → `{ items }`: the videos the user began past half a minute and has not finished, the latest first, drawn from what each was when it was reported |
| `GET /api/foyer/stream/yt/{ticket}/p/{token}.m3u8`, `…/s/{token}.m3u8`, `…/x/{token}.{ext}` | whoever holds the address | the site's playlists, rewritten so every link in them is Foyer's own (`p`; `s`, captions, whose segments are WebVTT), and its bytes passed on by ranges (`x`): segments, a muxed file, a caption. `403` as a stream address; `404` for a token Foyer never sealed; `502` where the site refuses |
| `GET`, `HEAD /api/foyer/stream/copy/{key}?u&s&exp&sig` | whoever holds the address | a made copy's bytes by ranges, as a file's; `403` as a stream address; `404` once the copy is let go of — the download asks again, and it is made again |
| `GET /api/foyer/stream/hls/{ticket}/{part}` | whoever holds the address | a remux: `main.m3u8`, `v.m3u8`, `a{k}.m3u8` and `s{k}.m3u8`; `v.mp4` and `a{k}.mp4`, each rendition's initialization; `v{n}.m4s` and `a{k}-{n}.m4s`, its segments; `s{k}-{n}.vtt`, its subtitles. The ticket — the file, the user, the playing, when it expires and the plan, signed — is in the path, so a segment's relative address resolves without a query. `403` as a stream address; `503` for a segment not made in a minute; `502` for a file ffmpeg could not remux |
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
| `GET /api/foyer/admin/sessions` | superusers | `{ sessions: [{ id, user: { id, name }, device: { name, platform, appVersion? }, kind: play \| download, mode, client?, item: { collection, id, title, subtitle?, poster? }, started, lastSeen, quiet, positionMs, durationMs, paused, bytes, bitrate, streams }] }`: what plays now, the newest first — Live Activity, polled every two seconds and never in the request log |
| `POST /api/foyer/admin/sessions/{id}/stop` | superusers | `204`: the playing ends, its streams with it, and its addresses are refused until they would have expired; `404` for one not playing |
| `GET /api/foyer/admin/copies` | superusers | `{ making: [{ key, file, title, by?, encode, state, progress, speed?, etaMs?, estimatedBytes?, asked, started?, error? }], kept: [{ key, file, title, by?, encode, version, name, size, made, asked }] }`: the copies being made, the longest waiting first, and those kept, the latest asked for first — never in the request log |
| `POST /api/foyer/admin/copies/{key}/stop` | superusers | `204`: a copy being made, or waiting, stops; nothing of it is kept |
| `DELETE /api/foyer/admin/copies/{key}` | superusers | `204`: a kept copy goes; asked for again, it is made again |
| `GET /api/foyer/admin/sessions/history` | superusers | `?page&perPage` (30, 100 at most) → `{ page, perPage, items: [{ id, session, user, device, kind, mode, client, item, started, ended, positionMs, durationMs, bytes, revoked }] }`: playings that ended, the latest first |

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
- **An extra keeps its own** — a film, an OVA, a special TMDB lacks that came
  with a series — by its title's identity and what the extra is:
  `tmdb:tv:37854/extra:4f0e9c1d2b3a5867`. A better copy of it keeps what was
  watched, and it moves with its title.

### The catalogue

- **What Loge reads of the library**, as `@loge/api`'s `foyer.ts` reads it:
  the app's `adapters/api/fixtures/foyer/routes/` hold an example of each
  answer, and the Go tests hold every answer to their keys.
- **In the app's own order.** Loge merges every source's pages by one
  comparison, so a page comes sorted as it sorts: by the value, a missing one
  first ascending and last descending, then the lowered title, then Loge's id
  for it, `collection.id`. The titles keep their keys for it (`docs/library`)
  and a page is the next few after the last one's keys — never an offset.
- **A field a title lacks is absent**, never empty or 0: an empty sort title
  would sort as it is, and a rating of 0 is nobody's.
- **A library switched off** on the dashboard is neither scanned nor in the
  catalogue.

### Streams

- **An address is its signature**, good for six hours: a player sends no
  headers. It is signed with a key drawn from the `users` collection's
  file-token secret — regenerating that secret in the dashboard (users →
  Options → Tokens) ends every address at once, and signs nobody out.
- **Bytes come through Foyer**, so the share's address and password never leave
  the server. A `Range`, `If-Range` or HEAD is passed on; Zurg's `200` with
  a `Content-Range` is a `206`; a player that leaves stops Zurg's answer.
- **No stream address is ever logged**, a refused one neither: it opens the
  file to whoever holds it. Nothing under `/api/foyer/stream/` is.
- Streams have a rate limit of their own: 600 requests in 10 seconds per
  address, before the catch-all. So do the pictures PocketBase's file route
  serves — posters, backdrops, stills — so a TV scrolling its rows never
  starves its own reports.

### Playings

- **Every address belongs to a playing** — a film or an episode watched, a
  copy downloaded — made by `/play` or `/download` and signed into the
  address (`s`), with whose it is, the device that asked, and what it plays.
  The dashboard's **Live Activity** shows each: who, where, what, how far,
  the bytes and their rate, and the player's engine, by its User-Agent.
- **A playing lives while its bytes flow or its player reports.** A phone
  asleep under AirPlay or picture in picture sends nothing and still pulls
  bytes, so a playing that goes quiet is only shown as quiet, after two
  minutes; with no stream open for half an hour it is let go — never
  refused: its next request brings it back, as it was. A download, which
  has no player to report, ends two minutes after its bytes stop.
- **A player's stop ends its playing** once no stream of it is open — never
  its addresses, so a player that carries on is let on, and a stop sent
  under a playing's id while its stream runs waits for the stream to end:
  an older playing's sent late never ends a newer one.
- **Only a superuser's Stop refuses**: the playing's streams end at once,
  and its addresses answer `403` until they would have expired — across a
  restart too, since `play_history` holds which were stopped.
- **Playings are kept in memory**, and an address of one Foyer forgot — it
  restarted — is taken up again on its first request, from its history where
  it has one.

### Remux

- **Plan z, off until the household turns it on**: Settings → Adapters →
  FFmpeg → Convert for players that cannot open a file. Off, `play` answers
  the file as it is with `convertible: true` where a remux would play, and
  Loge says converting could be turned on, or another player used — mpv and
  VLC open the file as it is.
- **The file as it is wherever the player opens it.** Asked with the
  player's profile, `play` judges the file as the app's `canPlay` does — its
  protocol, container, picture, one of its sounds, its height — and only
  where the player plays the picture and not the file (AVPlayer and
  Matroska, a browser) does it remux: to HLS of fragmented MP4, the picture
  copied, so never taller than the file's. What it answers for which profile
  and file is the app's `adapters/api/fixtures/foyer/decisions.json`, which
  Foyer's tests decide and the app's hold to `canPlay`. Where neither plays,
  the file as it is: the app says which player would.
- **Every sound a rendition, one codec for all of them**: each track as it
  is where all are one codec the player plays and a remux carries — AAC,
  AC-3, E-AC-3 — else the first track's where it is such, else E-AC-3 where
  the player plays it, else AAC; made anew at 5.1 at most. Text subtitles
  become WebVTT where the player shows WebVTT; pictures (PGS) wait for
  transcoding.
- **Only a file whose keyframes are known**, from its own index
  (`docs/library`), and only one ffprobe has read: a file it has not read is
  read at `play`, a few megabytes, so the remux carries every sound it has.
  Dolby Vision with no HDR10 layer beneath (profile 5) is never remuxed.
  The FFmpeg adapter is what turns remuxing on.
- **Segments at the file's own keyframes**, about six seconds, named in the
  playlists before one exists; one ffmpeg per playing cuts exactly there, on
  the file's own clock — a seek far from what it makes starts it again
  there, on the same clock. It runs until its slowest rendition is eight
  segments ahead of the player, then waits, and lets go of what the player
  has passed; a player
  that stops, a superuser's Stop, or a playing long quiet ends it, and its
  files go. They are kept in PocketBase's own temporary place, never in a
  backup and emptied at every start. Live Activity shows the playing as
  remuxed, and how fast ffmpeg makes it.
- **Known, still to mend:** where a remux carries text subtitles and makes a
  sound anew, a slow share holds that sound back until ffmpeg reaches the
  file's end — ffmpeg keeps its encoders in step with the subtitles — so a
  player waits for it.

### YouTube

- **The site's links are bound to the address that asked for them.** A
  video's streams, as yt-dlp finds them, open only from the house that
  asked: a phone away from home, or on another network at home, is
  refused on every player — as it was with Yattee Server, whose links are
  bound to the house. So a household's devices play YouTube through Foyer:
  yt-dlp asks from home, and Foyer fetches every playlist, segment and
  caption for the device, over IPv4 as yt-dlp asked.
- **Every link a device holds is sealed**: the site's own, deflated and
  sealed (AES-GCM) under a key drawn from the users' file-token secret, in
  an address under the stream prefix that carries the playing's signed
  ticket. Nothing to keep per playing, a restart forgets nothing, a sealed
  link opens only to the site's hosts, and a device never learns the house's
  address, which the site writes into each link.
- **Each address ends in what it is** — `.m3u8`, `.ts`, `.m4s`, `.mp4`,
  `.vtt`: ffmpeg's HLS reader, mpv's too, refuses a segment whose address
  names no media extension, or one that is not what it finds inside.
- **yt-dlp, here** — with Deno for the site's challenges — says what a
  video is, once for every user asking within twenty minutes, never within
  an hour of its links expiring. What it says on failure becomes a reason,
  and no address of the site's reaches a log, an error or a user.
- **Browsing is the site's own API** (InnerTube), as its web pages call
  it, signed out: searches, a video's page and what it suggests, channels
  and their tabs, playlists — the web client's version read from the site's
  own page once a day, in English always, since a video's "3 days ago" is
  read as English, for the adapter's region. Its answers change shape
  often, its older renderers beside its newer view models: each is read
  loosely, by what it is, wherever it sits. A mix — a radio the site makes
  as it plays — is no playlist a page opens, and is left out.
- **Following** is each channel's own Atom feed: its newest videos, each
  with exactly when it went up, read eight channels at a time and kept a
  quarter of an hour.
- **Watch status** is the user's, kept by the video's id
  (`youtube:<id>`) as a library title's is, with what the video is — its
  title, channel and picture — so Continue watching needs nothing of the
  site's (migration 0013).
- **Served while switched on**: the household's answer lists
  `{ key: "videos", type: "videos" }`, and its devices make a connection of
  it.
- **Settings → Adapters → YouTube**: yt-dlp's path, the region the site is
  asked for; its Test reads a short video the site has kept since its first
  days, as play would.

### Copies

- **Downloads made smaller are what the household feels most**: a phone's
  room, a film over the air. A device asks with its download settings, and
  Foyer makes a copy to them — by default AV1 (SVT-AV1, ten-bit, a constant
  quality by the preference and the picture's class, under the device's
  ceiling) and Opus in Matroska, 1080p and SDR — a few GB where the file is
  tens — and mpv and VLC, first on every device, play it. HEVC (x265)
  and H.264 (x264), AAC and MP4 where a device asks.
- **What it keeps**: one sound for each language — the file's first of it,
  no commentary — made 7.1 at most, a sound already that small copied;
  subtitles for each language, forced or not, for the hearing-impaired or
  not — text where the file has it, pictures where it has none, and in an
  MP4 text alone, as `mov_text`; the fonts a styled subtitle draws with; the
  chapters; none of mkvmerge's statistics, which would be wrong.
- **HDR is tone-mapped to SDR** where the device asks for SDR and this
  ffmpeg can — jellyfin-ffmpeg's `tonemapx`, or `zscale` with `tonemap` —
  else kept, ten-bit, with its colours and mastering metadata. Dolby Vision
  over HDR10 is made from its HDR10; Dolby Vision alone (profile 5) gets no
  copy.
- **Made once, by key**: the file as it is now — its id, size and date —
  and the settings as they matter for it (a ceiling it fits under is none),
  so whoever asks for the same is handed the same copy. One at a time by
  default, the longest waiting first, below whatever a player needs, read
  through the loopback as ffprobe reads a file — behind Zurg, the whole file
  from Real-Debrid.
- **Kept in `pb_data/foyer_copies`**, each beside a record of what it is —
  never in a backup, and a restore leaves them where they are — for the
  household's days after it was last asked for; past the room it allows the
  least lately asked go first, and the disk always keeps 10 GB free. Nothing
  asked for within the hour is let go of: it may be coming down. A copy half
  made when Foyer stops is let go of, and made again when asked.
- **Settings → Adapters → FFmpeg**: Make smaller copies for downloads (on),
  Effort for copies (SVT-AV1's preset 8, 6 or 4; x265's and x264's alike),
  Copies made at once, Keep copies for, Room for copies.

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

**`household`** — one record, made by migration 0008: its id is the
household's, and its `name` is what Loge calls this Foyer.

**`devices`** — each device a user paired or signed in on: `installation`
(the device's id for Foyer — a hash of its own key, the same for every user
and adapter on it), `name`, `platform`, `app_version`, `users`, `paired`,
`last_seen`. A device says which it is in an `X-Foyer-Device` header — base64url
of `{ installation, name, platform, appVersion }` — when it signs a user in or
refreshes a session.

**`watch_states`** — what a user watched: `user`, `identity`, the title it
was last played as (`item_collection`, `item_id`), `external_ids`, `watched`,
`position_ms`, `duration_ms`, `play_count`, `last_played_at`.

**What a user keeps for themselves** — migration 0010, one row per user and
record key (`user`, `key`, unique together), `deleted` for a tombstone, and
the `seq` its last write took. A user lists and reads their own; only
Foyer's routes write them.

| Collection | The app's record | Its fields |
| --- | --- | --- |
| `preferences` | `preference` — key `<user>/<name>` | `name`, `value` |
| `subscriptions` | `subscription`, a channel followed | `connection`, `external_id`, `title`, `added_at` |
| `playlists` | `playlist`, a list | `title`, `description`, `items`, `source`, `created_at`, `updated_at` |
| `favorite_channels` | `favoriteChannel`, a ★ | `connection`, `external_id`, `name`, `number`, `logo`, `added_at` |

**`play_history`** — migration 0011, one row per playing that ended:
`session`, `user`, the device (`device_name`, `device_platform`,
`app_version`), `kind`, the item (`item_collection`, `item_id`, `title`,
`subtitle`), `file`, `mode`, `client`, `started`, `ended`, `expires` — the
last address's — `position_ms`, `duration_ms`, `bytes`, and `revoked`, for a
superuser's Stop: the rows still to expire are what keeps a stopped
playing's addresses refused after a restart.

**`_foyer_seq`** — a table of Foyer's own, outside PocketBase's collections:
`state`, the last seq a write took, and `pruned`, the newest a tombstone let
go of had. Never on a record: a record anything else saves would write back
the value it read, and a seq that went back would make a device miss a
write.

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
| `media_files` | file of an entry (`entry`): its `path` under the folder, what its name says (`parsed`), which title took it (`item_collection`, `item_id`) and as what (`role`), what ffprobe found (`probe`), and where its keyframes lie (`keyframes`, read from its own index the first time a remux needs it; `keyframes_state` `done`, or `none` for a file that keeps no index) |
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
| `household`, `devices` | superusers | Foyer | Foyer | Foyer |
| `libraries` | users | Foyer | Foyer | Foyer |
| `media_entries`, `media_files`, `identities` | superusers | Foyer | Foyer | Foyer |
| `watch_states` | its own user's | Foyer | Foyer | Foyer |
| `play_history` | superusers | Foyer | Foyer | Foyer |
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
| `403` | creating or removing a user without being a superuser; a stream address that is wrong, expired, or of a playing stopped |
| `404` | a record that is not the caller's |
| `429` | throttled |

## Rate limits

PocketBase's rate limiter, switched on by the migrations, counts per address,
in memory, and never per user. `docs/deployment` lists its rules. Behind a
proxy it needs `FOYER_TRUST_PROXY` (`docs/deployment`).
