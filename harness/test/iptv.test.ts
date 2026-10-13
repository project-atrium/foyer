// iptv/foyer, the IPTV adapter the app ships, against the real binary over a
// pretend portal (dev/fakeportal) that plays one stream at a time, as a real
// subscription can: users signed in through sync/foyer lend it their
// sessions; the lineup and guide come through Foyer; one channel on two
// devices is one stream from the portal; a second channel finds the line
// full and says who holds it; it is taken over, and the device that held it
// is told who took it — and the portal never plays two.
import { connectionId, isLineError, type ConnectedHousehold, type ConnectedMediaProvider, type LentAccount, type PlayerProfile } from '@loge/api';
import { plugin as iptv } from '@loge/iptv-foyer';
import { plugin as foyer } from '@loge/sync-foyer';
import { afterEach, expect, it } from 'vitest';

import { context, fetchHttp } from './support/host';
import { startPortal, type Portal } from './support/portal';
import { startServer, until, type Server } from './support/server';

const anyEngine: PlayerProfile = { protocols: ['mpegts'], containers: ['ts'], videoCodecs: [], audioCodecs: [], subtitleFormats: [] };

let server: Server | undefined;
let portal: Portal | undefined;
const reading: AbortController[] = [];
afterEach(async () => {
  for (const each of reading.splice(0)) each.abort();
  await server?.stop();
  await portal?.stop();
  server = undefined;
  portal = undefined;
});

/** A user signed in on a device of their own, running the served portal on their session. */
async function signedIn(on: Server, username: string, password: string, device: { readonly name: string; readonly installation: string }, served: string) {
  const http = fetchHttp();
  const session = { value: undefined as string | undefined };
  const host = context({ ...device, http: http.client, session });
  const household: ConnectedHousehold = await foyer.household!.connect({ connectionId: connectionId('foyer'), fields: { serverUrl: on.url }, settings: {} }, host);
  session.value = (await household.signIn({ username }, { password })).session;
  const user: ConnectedHousehold = await foyer.household!.connect({ connectionId: connectionId('foyer'), fields: { serverUrl: on.url }, settings: {} }, host);
  const lent: LentAccount = { serverUrl: user.serverUrl, device: user.device, token: (signal) => user.token(signal), renew: (stale, signal) => user.renew(stale, signal) };
  const provider: ConnectedMediaProvider = await iptv.media!.connect(
    { connectionId: connectionId('served-iptv'), fields: { served }, settings: {} },
    context({ ...device, http: http.client, session: { value: undefined }, account: lent }),
  );
  return provider;
}

/** A player reading a channel's stream: the first bytes, MPEG-TS from its first packet, then on until it is left. */
async function watch(provider: ConnectedMediaProvider, channel: string) {
  const descriptor = await provider.getPlaybackDescriptor!({ key: { connectionId: connectionId('served-iptv'), externalId: channel }, profile: anyEngine });
  const [source] = descriptor.sources;
  expect(source).toMatchObject({ protocol: 'mpegts', live: true });
  const leave = new AbortController();
  reading.push(leave);
  const response = await fetch(source!.uri, { signal: leave.signal, headers: { 'User-Agent': 'libmpv' } });
  expect(response.status).toBe(200);
  const reader = response.body!.getReader();
  let bytes = new Uint8Array(0);
  while (bytes.length < 188 * 8) {
    const { value, done } = await reader.read();
    if (done) break;
    bytes = new Uint8Array([...bytes, ...value]);
  }
  expect([bytes[0], bytes[188], bytes[376]]).toEqual([0x47, 0x47, 0x47]);
  let ended = false;
  const drained = (async () => {
    try {
      for (;;) if ((await reader.read()).done) break;
    } catch {
      // Left, or cut off.
    }
    ended = true;
  })();
  return { leave: () => leave.abort(), ended: () => ended, closed: drained };
}

it('shares a channel as one stream, says who holds a full line, and lets one be taken over', async () => {
  portal = await startPortal({ streams: 1 });
  server = await startServer();
  const connection = await server.portal(portal.address, portal.mac, { streams: 1 });
  await server.makeUser({ username: 'alex', name: 'Alex', password: 'alex has a long password' });
  await server.makeUser({ username: 'kids', name: 'Kids', password: 'kids have a long password' });
  const served = `iptv-${connection}`;
  const phone = await signedIn(server, 'alex', 'alex has a long password', { name: 'Alex’s phone', installation: 'a1b2c3d4e5f60708' }, served);
  const tv = await signedIn(server, 'kids', 'kids have a long password', { name: 'Living room', installation: 'f0e1d2c3b4a59687' }, served);

  // The lineup and the guide, through Foyer.
  const groups = await phone.listChannelGroups!();
  expect(groups.map((group) => group.name)).toEqual(['News', 'Sports', 'DE ✨ DEUTSCHLAND']);
  const lineup = await phone.listChannels!({ limit: 100 });
  expect(lineup.channels.map((channel) => channel.key.externalId)).toEqual(['ch:101', 'ch:102', 'ch:103', 'ch:104', 'ch:534', 'ch:18390%3A1']);
  expect(lineup.channels.find((channel) => channel.key.externalId === 'ch:534')?.country).toBe('DE');
  const now = Date.now();
  const guide = await phone.getGuide!({ channels: [{ key: lineup.channels[0]!.key }], from: new Date(now).toISOString(), to: new Date(now + 3_600_000).toISOString() });
  expect(guide.length).toBeGreaterThan(0);
  expect(guide[0]?.title).toMatch(/^Morning Wire at /);

  // One channel on two devices: one stream from the portal.
  const alex = await watch(phone, 'ch:101');
  const kids = await watch(tv, 'ch:101');
  expect((await portal.streams()).asked).toEqual({ '101': 1 });
  expect((await server.playings()).map((playing) => [playing.user.name, playing.device.name, playing.mode]).sort()).toEqual([
    ['Alex', 'Alex’s phone', 'relay'],
    ['Kids', 'Living room', 'relay'],
  ]);

  // The kids move on to another channel: the one line is Alex's.
  kids.leave();
  const full = await tv.getPlaybackDescriptor!({ key: { connectionId: connectionId('served-iptv'), externalId: 'ch:102' }, profile: anyEngine }).catch((error: unknown) => error);
  expect(isLineError(full) && full.line.kind === 'full' ? full.line.streams.map((stream) => [stream.channel, stream.viewers]) : full).toEqual([
    ['ch:101', [{ user: 'Alex', device: 'Alex’s phone' }]],
  ]);
  const line = isLineError(full) && full.line.kind === 'full' ? full.line.streams[0]!.id : '';

  // Taken over: Alex's stream ends, and the portal plays the kids' alone.
  const descriptor = await tv.getPlaybackDescriptor!({ key: { connectionId: connectionId('served-iptv'), externalId: 'ch:102' }, profile: anyEngine, line: { takeOver: line } });
  expect(descriptor.sources[0]?.protocol).toBe('mpegts');
  await alex.closed;
  await until(async () => {
    const streams = await portal!.streams();
    return streams.open['101'] === undefined && streams.open['102'] === 1;
  }, 10_000);
  // Asking again, Alex's phone is told who took it.
  await expect(phone.getPlaybackDescriptor!({ key: { connectionId: connectionId('served-iptv'), externalId: 'ch:101' }, profile: anyEngine })).rejects.toMatchObject({
    reason: 'taken-over',
    message: 'Kids on Living room took this stream.',
  });
  // The portal never played two at once.
  expect(portal.output()).not.toMatch(/ended: 1 at once is the most/);
});
