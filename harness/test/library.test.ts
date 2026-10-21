// sources/foyer, the library adapter the app ships, against the real binary
// over a pretend Zurg: a user signed in through sync/foyer lends it their
// session, and it browses Foyer's catalogue in the app's order, plays a
// film's bytes by range, reports, resumes, marks it watched, and downloads —
// the file, or a smaller copy Foyer makes.
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import {
  compareItems,
  connectionId,
  isPreparing,
  type ConnectedHousehold,
  type ConnectedMediaProvider,
  type DownloadDescriptor,
  type DownloadPreparing,
  type ItemQuery,
  type LentAccount,
  type MediaItem,
} from '@loge/api';
import { plugin as library } from '@loge/source-foyer';
import { plugin as foyer } from '@loge/sync-foyer';
import { afterEach, expect, it } from 'vitest';

import { PROFILES } from '../../../../loge/src/adapters/players/system/src/profiles';
import { context, fetchHttp } from './support/host';
import { startServer, type Server } from './support/server';
import { startZurg, type Zurg } from './support/zurg';

const INSTALLATION = 'c2f1a9e0b7d34c58';
const PASSWORD = 'alex has a long password';

let server: Server | undefined;
let zurg: Zurg | undefined;
afterEach(async () => {
  await server?.stop();
  await zurg?.stop();
  server = undefined;
  zurg = undefined;
});

const film = (name: string, added: string) => ({ name, added, files: [`${name}.mkv`] });

/** A household with a films library of three, scanned, and Alex signed in on a device. */
async function household(options: { readonly video?: Uint8Array } = {}) {
  zurg = await startZurg(
    [
      film('Some.Film.2022.1080p.BluRay.x264-GRP', '2026-10-01T10:00:00Z'),
      film('Another.Film.2019.2160p.WEB-DL.x265-GRP', '2026-10-03T10:00:00Z'),
      film('Zebra.Story.2001.720p.HDTV.x264-GRP', '2026-10-02T10:00:00Z'),
    ],
    options,
  );
  server = await startServer();
  const share = await server.share(zurg.url);
  const movies = await server.addLibrary(share, 'movies', 'movies', 'movies');
  await server.scan(movies, 3);
  await server.makeUser({ username: 'alex', name: 'Alex', password: PASSWORD });

  const http = fetchHttp();
  const session = { value: undefined as string | undefined };
  const device = context({ name: 'Phone', installation: INSTALLATION, http: http.client, session });
  const signedIn: ConnectedHousehold = await foyer.household!.connect({ connectionId: connectionId('foyer'), fields: { serverUrl: server.url }, settings: {} }, device);
  session.value = (await signedIn.signIn({ username: 'alex' }, { password: PASSWORD })).session;
  // The user's instance, with the session the app keeps for them, as the household lends it.
  const alex: ConnectedHousehold = await foyer.household!.connect({ connectionId: connectionId('foyer'), fields: { serverUrl: server.url }, settings: {} }, device);
  const lent: LentAccount = { serverUrl: alex.serverUrl, device: alex.device, token: (signal) => alex.token(signal), renew: (stale, signal) => alex.renew(stale, signal) };
  const source: ConnectedMediaProvider = await library.media!.connect(
    { connectionId: connectionId('served-library'), fields: {}, settings: {} },
    context({ name: 'Phone', installation: INSTALLATION, http: http.client, session: { value: undefined }, account: lent }),
  );
  return { alex, source, http, zurg };
}

async function everything(source: ConnectedMediaProvider, query: ItemQuery): Promise<readonly MediaItem[]> {
  const items: MediaItem[] = [];
  let cursor: string | undefined;
  do {
    const page = await source.listItems!({ ...query, ...(cursor === undefined ? {} : { cursor }) });
    items.push(...page.items);
    cursor = page.nextCursor;
  } while (cursor !== undefined);
  return items;
}

it('browses the household’s library in the app’s own order, a page at a time', async () => {
  const { alex, source } = await household();
  expect((await alex.household()).served).toEqual([{ key: 'library', type: 'library', name: 'Library' }]);
  for (const sort of [
    { by: 'title', order: 'asc' },
    { by: 'title', order: 'desc' },
    { by: 'releaseDate', order: 'desc' },
    { by: 'addedAt', order: 'desc' },
  ] as const) {
    const items = await everything(source, { kind: 'movies', sort, limit: 1 });
    expect(items.map((item) => item.title)).toHaveLength(3);
    expect([...items].sort(compareItems(sort)).map((item) => item.key.externalId)).toEqual(items.map((item) => item.key.externalId));
  }
  expect((await everything(source, { kind: 'movies', sort: { by: 'title', order: 'asc' }, limit: 2 })).map((item) => item.title)).toEqual([
    'Another Film',
    'Some Film',
    'Zebra Story',
  ]);
  expect((await source.listItems!({ kind: 'movies', sort: { by: 'title', order: 'asc' }, limit: 10, term: 'zebra' })).items.map((item) => item.title)).toEqual([
    'Zebra Story',
  ]);
  const another = (await source.listItems!({ kind: 'movies', sort: { by: 'title', order: 'asc' }, limit: 1 })).items[0];
  const detail = await source.getItem!(another!.key.externalId);
  expect(detail.versions?.map((version) => [version.container, version.sizeBytes])).toEqual([['mkv', 4096]]);
});

it('plays a film by range, reports, resumes, is marked watched, and downloads it afresh', async () => {
  const { source, zurg: share } = await household();
  const [some] = (await source.listItems!({ kind: 'movies', sort: { by: 'title', order: 'asc' }, limit: 10, term: 'some film' })).items;
  if (!some) throw new Error('no film');
  const key = some.key;

  const descriptor = await source.getPlaybackDescriptor!({ key, profile: { protocols: ['progressive'], containers: ['mkv'], videoCodecs: [], audioCodecs: [], subtitleFormats: [] } });
  const [stream] = descriptor.sources;
  expect(stream).toMatchObject({ protocol: 'progressive', container: 'mkv', transcoded: false, live: false });
  const ranged = await fetch(stream!.uri, { headers: { Range: 'bytes=100-163' } });
  expect(ranged.status).toBe(206);
  expect(new Uint8Array(await ranged.arrayBuffer())).toEqual(share.sample.slice(100, 164));

  await source.reportPlayback!({ kind: 'started', key, positionMs: 0, durationMs: 120_000 });
  await source.reportPlayback!({ kind: 'progress', key, positionMs: 60_000, paused: false, durationMs: 120_000 });
  await source.reportPlayback!({ kind: 'stopped', key, positionMs: 60_000, durationMs: 120_000 });
  expect((await source.getResume!(10)).map((item) => [item.key.externalId, item.watch?.positionMs])).toEqual([[key.externalId, 60_000]]);

  await source.setPlayed!(key, true);
  expect(await source.getResume!(10)).toEqual([]);
  const [watched] = (await source.listItems!({ kind: 'movies', sort: { by: 'title', order: 'asc' }, limit: 10, term: 'some film' })).items;
  expect(watched?.watch?.played).toBe(true);

  // A download asks for its address afresh, and carries on from the bytes it has.
  const first = fetched(await source.getDownloadDescriptor!({ key }));
  const again = fetched(await source.getDownloadDescriptor!({ key }));
  expect([first.container, first.expectedBytes, again.uri.startsWith(`${server!.url}/api/foyer/stream/`)]).toEqual(['mkv', 4096, true]);
  const rest = await fetch(again.uri, { headers: { Range: 'bytes=4000-' } });
  expect(rest.status).toBe(206);
  expect(new Uint8Array(await rest.arrayBuffer())).toEqual(share.sample.slice(4000));

  // Nothing of a stream's address in what the server printed.
  expect(server!.output()).not.toContain('/api/foyer/stream/');
}, 60_000);

it('shows a playing on Live Activity, with whose and where, and refuses its address once stopped', async () => {
  const { source } = await household();
  const [some] = (await source.listItems!({ kind: 'movies', sort: { by: 'title', order: 'asc' }, limit: 10, term: 'some film' })).items;
  if (!some) throw new Error('no film');
  const key = some.key;
  const descriptor = await source.getPlaybackDescriptor!({ key, profile: { protocols: ['progressive'], containers: ['mkv'], videoCodecs: [], audioCodecs: [], subtitleFormats: [] } });
  const address = descriptor.sources[0]!.uri;
  expect((await fetch(address, { headers: { Range: 'bytes=0-99' } })).status).toBe(206);
  await source.reportPlayback!({ kind: 'started', key, positionMs: 0, durationMs: 120_000 });
  await source.reportPlayback!({ kind: 'progress', key, positionMs: 30_000, paused: false, durationMs: 120_000 });

  const [playing, ...more] = await server!.playings();
  expect(more).toEqual([]);
  expect(playing).toMatchObject({ user: { name: 'Alex' }, device: { name: 'Phone' }, kind: 'play', item: { title: 'Some Film' }, positionMs: 30_000, bytes: 100 });

  await server!.stopPlaying(playing!.id);
  expect(await server!.playings()).toEqual([]);
  const refused = await fetch(address, { headers: { Range: 'bytes=0-99' } });
  expect(refused.status).toBe(403);
  expect(((await refused.json()) as { message: string }).message).toContain('stopped');
  // A new playing of it plays.
  const again = await source.getPlaybackDescriptor!({ key, profile: { protocols: ['progressive'], containers: ['mkv'], videoCodecs: [], audioCodecs: [], subtitleFormats: [] } });
  expect((await fetch(again.sources[0]!.uri, { headers: { Range: 'bytes=0-99' } })).status).toBe(206);
  expect(server!.output()).not.toContain('/api/foyer/stream/');
}, 60_000);

/** A copy to fetch: the file as it is never waits to be made. */
function fetched(answer: DownloadDescriptor | DownloadPreparing): DownloadDescriptor {
  if (isPreparing(answer)) throw new Error('Foyer is still making a copy of the file as it is');
  return answer;
}

/** ffmpeg on this machine, or nothing: the remux is tested where it can run. */
function ffmpegHere(): boolean {
  try {
    execFileSync('ffmpeg', ['-version'], { stdio: 'ignore' });
    execFileSync('ffprobe', ['-version'], { stdio: 'ignore' });
    return true;
  } catch {
    return false;
  }
}

/** Forty seconds of a Matroska film AVPlayer cannot open as it is: H.264 and AC-3 5.1. */
function matroska(): Uint8Array {
  const directory = mkdtempSync(join(tmpdir(), 'foyer-harness-video-'));
  try {
    const out = join(directory, 'film.mkv');
    execFileSync('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-y', '-f', 'lavfi', '-i', 'testsrc2=size=640x360:rate=24',
      '-f', 'lavfi', '-i', 'sine=frequency=440:sample_rate=48000', '-t', '40', '-c:v', 'libx264', '-preset', 'ultrafast', '-g', '48', '-bf', '2',
      '-pix_fmt', 'yuv420p', '-c:a', 'ac3', '-ac', '6', out]);
    return new Uint8Array(readFileSync(out));
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
}

it.skipIf(!ffmpegHere())('remuxes a Matroska film to HLS for AVPlayer: its playlists, its segments, a seek among them', async () => {
  const { source } = await household({ video: matroska() });
  await server!.ffmpeg(true);
  const [some] = (await source.listItems!({ kind: 'movies', sort: { by: 'title', order: 'asc' }, limit: 10, term: 'some film' })).items;
  if (!some) throw new Error('no film');
  const avPlayer = PROFILES.ios;
  if (!avPlayer) throw new Error('no profile');
  // Converting is plan z, off until the household turns it on: the file, and that it could be.
  const off = await source.getPlaybackDescriptor!({ key: some.key, profile: avPlayer });
  expect(off.convertible).toBe(true);
  expect(off.sources.map((each) => each.protocol)).toEqual(['progressive']);
  await server!.ffmpeg(true, true);
  const descriptor = await source.getPlaybackDescriptor!({ key: some.key, profile: avPlayer, startMs: 12_000 });
  const [hls, file] = descriptor.sources;
  // The remux first; the file after it, for a player that opens it.
  expect(hls).toMatchObject({ protocol: 'hls', videoCodec: 'h264', audioCodecs: ['ac3'], transcoded: true, live: false });
  expect(file).toMatchObject({ protocol: 'progressive', container: 'mkv', transcoded: false });
  const master = await fetch(hls!.uri);
  expect(master.headers.get('content-type')).toBe('application/vnd.apple.mpegurl');
  const text = await master.text();
  expect(text).toMatch(/CODECS="avc1\.[0-9A-F]{6},ac-3"/);
  expect(text).toContain('RESOLUTION=640x360');
  const base = hls!.uri.replace(/main\.m3u8$/, '');
  const playlist = await (await fetch(`${base}v.m3u8`)).text();
  const segments = playlist.split('\n').filter((line) => line.endsWith('.m4s'));
  expect(segments.length).toBeGreaterThan(4);
  const init = new Uint8Array(await (await fetch(`${base}v.mp4`)).arrayBuffer());
  expect(new TextDecoder().decode(init.slice(4, 8))).toBe('ftyp');
  // Where it starts, the next, and a seek to the end: each a segment of fragments.
  for (const name of [segments[2], segments[3], segments[segments.length - 1], 'a0-1.m4s']) {
    const response = await fetch(`${base}${name}`);
    expect(response.status, name).toBe(200);
    const bytes = new Uint8Array(await response.arrayBuffer());
    expect(new TextDecoder().decode(bytes.slice(4, 8)), name).toBe('moof');
  }
  const [playing] = await server!.playings();
  expect(playing).toMatchObject({ user: { name: 'Alex' }, device: { name: 'Phone' }, mode: 'remux' });
  expect(server!.output()).not.toContain('/api/foyer/stream/');
}, 60_000);

it.skipIf(!ffmpegHere())('makes a smaller copy to the device’s download settings, says how far along it is, then hands it over', async () => {
  const { source } = await household({ video: matroska() });
  await server!.ffmpeg(true);
  const [some] = (await source.listItems!({ kind: 'movies', sort: { by: 'title', order: 'asc' }, limit: 10, term: 'some film' })).items;
  if (!some) throw new Error('no film');
  const options = await source.listDownloadOptions!(some.key, undefined, { maxHeight: 240, hdr: false, videoCodec: 'av1', preference: 'size', audioCodec: 'opus', container: 'mkv' });
  expect(options.map((option) => [option.id, option.transcoded, option.width, option.height])).toEqual([
    ['copy.av1.size.240.0.sdr.opus.mkv', true, 426, 240],
    ['original', false, 640, 360],
  ]);
  const chosen = options[0]!;
  let answer = await source.getDownloadDescriptor!({ key: some.key, optionId: chosen.id });
  expect(answer).toMatchObject({ kind: 'preparing' });
  for (const until = Date.now() + 120_000; isPreparing(answer); ) {
    if (Date.now() > until) throw new Error('the copy was never made');
    await new Promise((resolve) => setTimeout(resolve, 250));
    answer = await source.getDownloadDescriptor!({ key: some.key, optionId: chosen.id });
  }
  expect(answer).toMatchObject({ container: 'mkv', videoCodec: 'av1', audioCodecs: ['opus'], height: 240, transcoded: true });
  expect(answer.expectedBytes).toBeLessThan(options[1]!.estimatedBytes!);
  // Fetched as any download is: by ranges, from where it stopped.
  const start = await fetch(answer.uri, { headers: { Range: 'bytes=0-3' } });
  expect(start.status).toBe(206);
  expect([...new Uint8Array(await start.arrayBuffer())]).toEqual([0x1a, 0x45, 0xdf, 0xa3]);
  const rest = await fetch(answer.uri, { headers: { Range: 'bytes=4-' } });
  expect((await rest.arrayBuffer()).byteLength).toBe(answer.expectedBytes! - 4);
  expect(server!.output()).not.toContain('/api/foyer/stream/');
}, 180_000);
