# The library

How Foyer turns your Zurg folders into a library — named, dated and kept up
to date — without renaming or moving a single file.

## Libraries are Zurg's folders

Zurg sorts your Real-Debrid torrents into folders by its config — `movies`,
`shows`, `anime`, as you set them up. On the dashboard, Settings → Adapters
lists them; each one you choose becomes a library, of films or of series,
named as you like:

| A movies library called | is the collection | |
| --- | --- | --- |
| `movies` | `movies` | one record per film |
| `shows` | `shows` and `shows_episodes` | one record per series, and one per episode |

Folders you leave alone — `__all__`, `__unplayable__`, downloads — are never
read.

**More than one share.** Each WebDAV share is a connection of its own on the
Adapters page — Zurg, a NAS — and lists its own folders. A library is a
folder of one share; two shares may each have a `movies` folder, made into
two libraries of different names.

## A scan

Foyer looks at each library every 15 minutes (its share's interval), at
once when that share's `on_library_update` tells it, and when you
press Scan now. Once a day it walks every entry again — a torrent on Zurg, a
folder or a file elsewhere — to see inside them.

1. **Zurg's folder is listed**, over WebDAV. A torrent Foyer has not seen is
   walked — its folders too, however deep Zurg shows them — and its video
   files recorded. Samples are left out.
2. **Each file's name is read**: the title and year, the season and episode or
   the episode counted from the series' start, what the file is. A season
   pack's name gives its files' title; a season folder gives a number a file
   names without one — never a number counted from the start, since a pack's
   "Season 01" can hold episode 62. A pack's range — "(0001-1071)", "Black
   Clover 1-170", "Seasons 1 to 13" — ends its title, and so do the years a
   series ran ("1989-1998"). A file in a pack without the series' name in
   its own — "Start Credit (Part 1)" — is the pack's; another series in the
   same torrent needs numbered episodes of its own.
3. **Each title is asked of TMDB once**, and the answer remembered
   (`identities`): never asked again for a title it named.
4. **Each title is written**: a film with its files as its versions; a series
   with its episodes as TMDB numbers them, each with its files. Specials are
   matched to TMDB's season 0 by name — the whole name, or the part before
   TMDB's colon or dash: "3D2Y" is "3D2Y: Overcome Ace's Death!…" — and only
   where one episode answers; films and OVAs that came in a series' pack are
   its extras, and so is a special TMDB has no episode for — placed again by
   every scan, should TMDB add it. Extras play as versions do.
5. **Artwork is fetched** into PocketBase's own storage — local, or S3 as
   PocketBase's settings say: posters, backdrops, logos, episode stills.
6. **What Zurg stopped listing** is kept for three days — Zurg's repairs take
   files away for a while — then let go, and a title left with no file goes
   with it. What each of you watched stays, for when it comes back.

Zurg lists nothing while it starts: a scan that finds the folder empty takes
nothing away. And where most of a library vanishes at once, nothing is taken
away until the next scan, ten minutes or more later, agrees.

## In the app's order

Loge merges every source's pages by one comparison, so Foyer hands out its
titles in exactly that order. Each title keeps what it is sorted and searched
by, written on every save: its sort title, else its title, lowered as
JavaScript lowers it and in the order JavaScript compares text (`İ`, a Greek
word's last sigma and characters beyond the basic plane are where Go and
SQLite would differ); its release date, else the year; its rating to the
thousandth — a date or a rating it lacks sorts first; its names and year
with their accents folded away, for search; and its genres as the app keys
them. A title saved before these existed gets them when Foyer starts.

## When a title was added

The Library page shows the newest first. A title was added when its first
file was, and is updated when its newest was: a series rises when a new
episode comes. A file was added when Zurg dates its torrent's folder; where
those dates are all one second — Zurg's clock, not Real-Debrid's — when the
file says; else when Foyer first saw it.

## How sure TMDB is

| State | What it means | Seen by users |
| --- | --- | --- |
| matched | TMDB named it, and was sure | yes |
| uncertain | several titles share the name, and nothing told them apart: TMDB's first, as a guess | **no**, until you confirm or correct it |
| unmatched | TMDB had nothing: the title is its name's, without artwork | yes |
| manual | you said what it is | yes |

**Matching a name.** TMDB is searched with the title and year, as Loge
searches it, and a result taken where its name is the same once case,
accents and punctuation are set aside. Where none is, Foyer forgives what a
release's name drops: an apostrophe ("Dont", "Dead Mans Chest"), a
possessive ("Fred" for "Fred's"), an article, a superscript ("Accountant 2"
is "The Accountant²"). A title written "X - Y" is then asked as each half —
"Truman Show - The Truman Show" is The Truman Show. All of it within a year
of the one the name gives.

**Telling titles of one name apart.** Two series are called One Piece: the
anime, with over a thousand episodes, and the live action, with seventeen. A
pack running to episode 1071 can only be the first. Where the episodes do not
decide, an anime release — a group in brackets, episodes counted from the
start, dual audio — picks the anime. Where nothing decides, it is a guess.

**Correcting one.** On the Library page, open a title: **Confirm** takes the
guess; **Set TMDB id** names the title it is (the id in its themoviedb.org
address). Either is kept for good — a later scan never asks again — and where
another title already is that one, the two become one, with every file of
both.

## Episodes counted from the start

Anime packs number episodes from the series' first: `One Piece - 1071`. TMDB
numbers some series that way inside their seasons — One Piece's season 21 is
episodes 892 to 1088 — and others from one each season. Foyer looks for the
episode in the season that holds that number; else in TMDB's absolute
ordering of the series, where it has one; else counts on season by season.

A release's second season may be TMDB's first, run on: Solo Leveling's
`S02E01` is TMDB's `S01E13`, after the twelve the release holds of its
first. Where TMDB has no such season and the season before runs that far,
the episode is laid there.

## What each file is

With the FFmpeg adapter switched on, ffprobe reads every file — the newest
first, two at a time unless you say more, through Foyer itself, so it never
holds the share's address or password. It reads a few megabytes of each: its
picture and quality, its codec and HDR (Dolby Vision, HDR10, HDR10+, HLG),
its audio tracks with their languages and Atmos or DTS:X, its subtitles,
forced and SDH, and its chapters.

Until ffprobe has looked, a file is what its name says: `2160p · HDR ·
x265` is a 4K HEVC file with HDR. A title's lists — quality, encoding,
container, HDR, audio tracks, subtitles and their languages — are drawn from
its files', and its `versions` say each one apart, with its path in Zurg.

A file ffprobe cannot read is tried again after ten minutes, an hour and six
hours, then left; Zurg itself failing costs it nothing, and pauses the reading
a while. The GPU is tried on the Adapters page: transcoding comes later, and
that is where the household learns beforehand that it is reachable.

## Telling Foyer when the share changes

Settings → Adapters shows a line for Zurg's config, with a token of its own
— any other share's tools can post the folders that changed the same way:

```yaml
on_library_update: printf '%s\n' "$@" | curl -fsS -m 10 -X POST -H 'Authorization: Bearer <token>' --data-binary @- http://foyer:8090/api/foyer/webdav/updated
```

Zurg runs it with the folders that changed; Foyer scans those libraries at
once. The token travels in a header, never the address: PocketBase keeps a
log of addresses.
