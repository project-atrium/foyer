// sources/foyer, the library adapter the app ships, against the real binary
// over a pretend Zurg: a user signed in through sync/foyer lends it their
// session, and it browses Foyer's catalogue in the app's order, plays a
// film's bytes by range, reports, resumes, marks it watched, and downloads.
import { compareItems, connectionId, type ConnectedHousehold, type ConnectedMediaProvider, type ItemQuery, type LentAccount, type MediaItem } from '@loge/api';
import { plugin as library } from '@loge/source-foyer';
import { plugin as foyer } from '@loge/sync-foyer';
import { afterEach, expect, it } from 'vitest';

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
async function household() {
  zurg = await startZurg([
    film('Some.Film.2022.1080p.BluRay.x264-GRP', '2026-10-01T10:00:00Z'),
    film('Another.Film.2019.2160p.WEB-DL.x265-GRP', '2026-10-03T10:00:00Z'),
    film('Zebra.Story.2001.720p.HDTV.x264-GRP', '2026-10-02T10:00:00Z'),
  ]);
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
  const first = await source.getDownloadDescriptor!({ key });
  const again = await source.getDownloadDescriptor!({ key });
  expect([first.container, first.expectedBytes, again.uri.startsWith(`${server!.url}/api/foyer/stream/`)]).toEqual(['mkv', 4096, true]);
  const rest = await fetch(again.uri, { headers: { Range: 'bytes=4000-' } });
  expect(rest.status).toBe(206);
  expect(new Uint8Array(await rest.arrayBuffer())).toEqual(share.sample.slice(4000));

  // Nothing of a stream's address in what the server printed.
  expect(server!.output()).not.toContain('/api/foyer/stream/');
});

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
});
