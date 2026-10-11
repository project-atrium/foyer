// sync/foyer, the household account the app ships, against the real binary:
// what one device sends of a user's own state, another of theirs reads back,
// in the order it was stored — and nobody else does.
import { connectionId, userId, type AccountRecord, type ConnectedHousehold } from '@loge/api';
import { plugin } from '@loge/sync-foyer';
import { afterEach, expect, it } from 'vitest';

import { context, fetchHttp, type SessionBox } from './support/host';
import { startServer, type Server } from './support/server';

let server: Server | undefined;
afterEach(async () => {
  await server?.stop();
  server = undefined;
});

/** A device signed in as one user, by their password. */
async function signedIn(username: string, password: string, installation: string): Promise<ConnectedHousehold> {
  const session: SessionBox = { value: undefined };
  const device = context({ name: installation, installation, http: fetchHttp().client, session });
  const target = { connectionId: connectionId('foyer'), fields: { serverUrl: server!.url }, settings: {} };
  const anyone = await plugin.household!.connect(target, device);
  session.value = (await anyone.signIn({ username }, { password })).session;
  return plugin.household!.connect(target, device);
}

it('carries a user’s own lists, follows and ★ between their devices, a page at a time, and to nobody else', async () => {
  server = await startServer();
  const alex = userId(await server.makeUser({ username: 'alex', name: 'Alex', password: 'alex has a long password' }));
  await server.makeUser({ username: 'kids', name: 'Kids', password: 'kids have a long password' });
  const phone = await signedIn('alex', 'alex has a long password', 'phone000000001');
  const tv = await signedIn('alex', 'alex has a long password', 'tv000000000002');
  const kids = await signedIn('kids', 'kids have a long password', 'tv000000000002');

  const served = connectionId('served-1f2e3d4c5b6a7980');
  const list: AccountRecord = {
    kind: 'playlist',
    key: 'p1l2a3y4l5i6s7t',
    deleted: false,
    data: { playlistId: 'p1l2a3y4l5i6s7t', userId: alex, title: 'Friday films', items: [{ connectionId: served, externalId: 'movies.k8m2x7q1w3e5r9t' }], createdAt: '2026-10-11T09:00:00.000Z', updatedAt: '2026-10-11T09:00:00.000Z' },
  };
  const follow: AccountRecord = {
    kind: 'subscription',
    key: 's1u2b3s4c5r6i7p',
    deleted: false,
    data: { subscriptionId: 's1u2b3s4c5r6i7p', userId: alex, connectionId: served, externalId: 'channel:3blue1brown', title: '3Blue1Brown', addedAt: '2026-10-11T09:00:00.000Z' },
  };
  const star: AccountRecord = {
    kind: 'favoriteChannel',
    key: 'f1a2v3o4r5i6t7e',
    deleted: false,
    data: { favoriteId: 'f1a2v3o4r5i6t7e', userId: alex, connectionId: served, externalId: 'ch:1', name: 'Das Erste', number: 1, addedAt: '2026-10-11T09:00:00.000Z' },
  };
  const rename: AccountRecord = { kind: 'profile', key: alex, deleted: false, data: { userId: alex, name: 'Alexandra' } };
  expect(await phone.push([rename, list, follow, star])).toEqual({ kind: 'stored' });
  expect((await tv.me()).name).toBe('Alexandra');

  // Everything, read on the TV, in the order it was stored.
  const everything = await tv.changes(0, 0);
  expect(everything.records).toEqual([list, follow, star]);
  expect(everything.more).toBe(false);
  // A delete reaches the TV as a tombstone, and nothing else with it.
  expect(await phone.push([{ kind: 'subscription', key: follow.key, deleted: true }])).toEqual({ kind: 'stored' });
  const since = await tv.changes(everything.cursor, everything.cursor);
  expect(since.records).toEqual([{ kind: 'subscription', key: follow.key, deleted: true }]);
  expect(since.cursor).toBeGreaterThan(everything.cursor);

  // Kids reads nothing of it, and cannot send a record of Alex's.
  expect((await kids.changes(0, 0)).records).toEqual([]);
  expect(await kids.push([star])).toEqual({ kind: 'refused', index: 0, reason: 'invalid' });
});
