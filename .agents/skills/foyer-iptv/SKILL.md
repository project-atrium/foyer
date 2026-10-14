---
name: foyer-iptv
description: A household's IPTV through Foyer, the portal's only client — the Stalker client ported from Loge (one sign-in, the gate, pauses), each portal's lineup kept and its guide held in memory, lines (one upstream per channel for every viewer, within the subscription's streams, taken over or forced), the MPEG-TS relay under the stream prefix, the routes, the dashboard's Channels section, and dev/fakeportal. Use when changing anything in internal/adapters/stalker or internal/iptv, a line's rules, the relay, a guide's times, or when finding out why a channel will not play, says every stream is in use, or shows the wrong programme.
---

# A household's IPTV through Foyer

Read `AGENTS.md` first — "Foyer is a portal's only client", "A line is a
stream of the subscription" and "A portal's command and its stream's address
never leave Foyer" are the rules this part exists to keep — then
`docs/api/README.md`'s IPTV section.

## When to use it

- the portal client: signing in, the gate, a refusal, a 429, reading the
  lineup or the guide (`internal/adapters/stalker`)
- what Foyer keeps of a portal — its lineup (`iptv_lineups`), its guide in
  memory — and when it reads them again
- lines: who holds which, a take-over, a forced line, a device that zaps
- the relay a device plays (`/api/foyer/stream/iptv/{ticket}/live.ts`)
- the Stalker adapter's fields, the dashboard's Channels section

Not for Loge's own `iptv/stalker` — the app's adapter for a local account —
except where both read a guide alike: the countries and zones are held to the
app's `countries.json` and `zones.json`, and a change to one is a change to
both.

## How a channel plays

```
adapters/stalker/portal.go     one Portal per connection: shared sign-in, refusal latched, ended-elsewhere said
adapters/stalker/gate.go       one request at a time, ¼ s apart; a channel first; the guide stopped, resumed 8 s on
adapters/stalker/provider.go   the lineup (genres + every channel at once), the guide window, a link
iptv/iptv.go                   connections, iptv_lineups merged (a channel gone twice is gone), the guide held
iptv/lines.go                  Claim: join, a new line, take over, force, or Full — who holds what, where
iptv/broadcast.go              one upstream, a ring of 16 MB, every viewer from a packet boundary
iptv/routes.go                 lineup, guide, play, refresh; the relay; the admin status and read
```

A device asks `POST /api/foyer/iptv/{conn}/play {channel, takeOver?, force?}`
with its `X-Foyer-Device` header; Foyer claims a line for it and answers a
ticket in the path, or `409 line-full` with every stream in use and who
watches each — or `taken-over`, once, to the device whose line was taken.

## Changing something

- **Never a second `Portal` for a connection, never a request outside the
  gate**: each sign-in ends the token before it, and the provider counts
  devices. The MAG User-Agent and the connection's MAC and box ids, always.
- **A refusal is latched; a 429 or a 503 that says when is a pause.** Never a
  verdict — the app's adapter once latched "no series" and "no portal here"
  from one.
- **A device watches one channel at a time**, by its installation id: a zap
  lets go of the last line before it claims the next.
- **Nothing of a stream reaches a log**, nor a portal's `cmd` a device: the
  relay lives under the stream prefix, and the lineup a device reads has no
  command in it.
- **A refusal from the portal is never a 401** to a device — Loge takes any
  401 for its own session ending.
- **The real portal only with the owner's go-ahead**, as the one device, one
  stream at a time, when nobody at home is watching. Everything else is
  `stalkertest` or `dev/fakeportal`.

## Verify

```bash
gofmt -l . && go vet ./... && go test -race ./internal/adapters/stalker/... ./internal/iptv/... ./internal/server/
cd harness && npm test -- iptv
```

`internal/server/iptv_test.go` runs IPTV through Foyer's real router with a
pretend stream server; the harness runs the app's real `iptv/foyer` against
the real binary over `dev/fakeportal`, which plays as many streams at once as
it allows (`-streams`) and ends the oldest past that, as a subscription does.

By hand:

```bash
go run ./dev/fakeportal -addr 127.0.0.1:9988 -streams 1
```

then a Stalker connection on Settings → Adapters at
`http://127.0.0.1:9988/c/` with MAC `00:1A:79:00:00:01`. In Docker, give it
`-link` with the address Foyer reaches it by.

## Why a channel…

- **will not play** — the connection's Test, then the dashboard's Channels
  section (`GET /api/foyer/admin/iptv/{conn}`): whether the sign-in is
  latched, when the lineup was read, the lines in use. An upstream's error is
  said in Foyer's words, never with its address. A channel the portal streams
  as HLS is not relayed yet.
- **says every stream is in use** — a line per channel, `streams` of them; a
  line stays a little after its last viewer leaves, and one claimed and never
  opened is let go of after a while (`Lines.StayAfter`, `Lines.FirstOpen`).
  Live Activity lists each line's playings, and a Stop frees one.
- **shows the wrong programme** — the guide's time zone on the connection,
  and "Put other countries' guides right": a channel's country (its guide id,
  group or name) decides whether its `time` is read as UTC.
- **is missing** — the lineup is read every few days (`lineupDays`), when a
  device first asks, and on a Read from the dashboard; a channel goes only
  when two reads agree it is gone, never on an empty answer.
