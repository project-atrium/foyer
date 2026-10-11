// sync/foyer, the household account the app ships, against the real
// binary: a device paired for two users, a third signing in, each session
// renewed and — once a password changes — refused, once.
import { connectionId, isAppError, type ConnectedHousehold } from '@loge/api';
import { plugin } from '@loge/sync-foyer';
import { afterEach, expect, inject, it } from 'vitest';

import { context, fetchHttp, type SessionBox } from './support/host';
import { startServer, type Server } from './support/server';

const DAY = 24 * 60 * 60 * 1000;
const INSTALLATION = 'c2f1a9e0b7d34c58';

let server: Server | undefined;
afterEach(async () => {
  await server?.stop();
  server = undefined;
});

/** The household as the app connects it on one device: with a user's session, or with none. */
async function household(options: { readonly session?: SessionBox; readonly now?: () => number } = {}) {
  const http = fetchHttp();
  const session = options.session ?? { value: undefined };
  const connected: ConnectedHousehold = await plugin.household!.connect(
    { connectionId: connectionId('foyer'), fields: { serverUrl: server!.url }, settings: {} },
    context({ name: 'Living room TV', installation: INSTALLATION, platform: 'tvos', http: http.client, session, ...(options.now ? { now: options.now } : {}) }),
  );
  return { connected, http, session };
}

async function refusal(promise: Promise<unknown>) {
  try {
    await promise;
  } catch (error) {
    if (isAppError(error)) return { code: error.code, reason: error.reason };
    throw error;
  }
  throw new Error('It was not refused.');
}

it('pairs a device for two users, another signs in with a password, and Foyer lists the device once', async () => {
  server = await startServer();
  const alex = await server.makeUser({ username: 'alex', name: 'Alex', password: 'alex has a long password', pin: '1234' });
  const kids = await server.makeUser({ username: 'kids', name: 'Kids', password: 'kids have a long password' });
  const sam = await server.makeUser({ username: 'sam', name: 'Sam', password: 'sam has a long password' });

  const device = await household();
  const info = await device.connected.info();
  expect(info).toEqual({ serverVersion: inject('appVersion'), householdId: expect.stringMatching(/^[a-z0-9]{15}$/), name: 'Foyer' });

  const code = await server.pairingCode([alex, kids]);
  const paired = await device.connected.pair!({ code });
  expect(paired.map((user) => user.self)).toEqual([
    { userId: alex, username: 'alex', name: 'Alex', pin: '1234' },
    { userId: kids, username: 'kids', name: 'Kids' },
  ]);
  // A code is spent the moment it is used.
  expect(await refusal(device.connected.pair!({ code }))).toMatchObject({ code: 'UNAUTHORIZED' });

  // Each user is then an instance of their own, with the session pairing gave them.
  const asAlex = await household({ session: { value: paired[0]!.session } });
  expect(await asAlex.connected.me()).toEqual({ userId: alex, username: 'alex', name: 'Alex', pin: '1234' });
  const view = await asAlex.connected.household();
  expect(view.householdId).toBe(info.householdId);
  expect(view.members).toEqual(
    expect.arrayContaining([
      { userId: alex, username: 'alex', name: 'Alex', hasPin: true },
      { userId: kids, username: 'kids', name: 'Kids', hasPin: false },
      { userId: sam, username: 'sam', name: 'Sam', hasPin: false },
    ]),
  );
  expect(JSON.stringify(view)).not.toContain('1234');

  const signedIn = await device.connected.signIn({ username: 'sam' }, { password: 'sam has a long password' });
  expect(signedIn.self).toEqual({ userId: sam, username: 'sam', name: 'Sam' });
  expect(signedIn.session).not.toContain('sam has a long password');
  expect(await refusal(device.connected.signIn({ username: 'sam' }, { password: 'not the password' }))).toMatchObject({ code: 'UNAUTHORIZED' });

  // One device, whoever signed in on it, and however.
  expect(await server.devices()).toEqual([
    expect.objectContaining({ name: 'Living room TV', platform: 'tvos', appVersion: inject('appVersion'), users: ['Alex', 'Kids', 'Sam'], paired: true }),
  ]);
});

it('renews a day-old session, and once the password changes is refused — asking Foyer once', async () => {
  server = await startServer();
  const alex = await server.makeUser({ username: 'alex', name: 'Alex', password: 'alex has a long password' });
  const signedIn = await (await household()).connected.signIn({ username: 'alex' }, { password: 'alex has a long password' });

  let shift = 0;
  const session: SessionBox = { value: signedIn.session };
  const asAlex = await household({ session, now: () => Date.now() + shift });
  expect((await asAlex.connected.me()).userId).toBe(alex);
  expect(asAlex.http.count('POST /api/collections/users/auth-refresh')).toBe(0);

  // A day on, the session is renewed — once, for every call that wants it.
  shift = DAY + 60_000;
  const before = session.value;
  await Promise.all([asAlex.connected.me(), asAlex.connected.household(), asAlex.connected.me()]);
  expect(asAlex.http.count('POST /api/collections/users/auth-refresh')).toBe(1);
  expect(session.value).not.toBe(before);

  // A new password ends every session of the user; the next call is refused and latched.
  await server.setPassword(alex, 'a new long password');
  expect(await refusal(asAlex.connected.me())).toEqual({ code: 'UNAUTHORIZED', reason: 'signed-out' });
  const asked = asAlex.http.exchanges.length;
  expect(await refusal(asAlex.connected.household())).toEqual({ code: 'UNAUTHORIZED', reason: 'signed-out' });
  expect(asAlex.http.exchanges.length).toBe(asked);

  // So is a new instance on the same session store: the latch is the user's, not the instance's.
  const again = await household({ session });
  expect(await refusal(again.connected.me())).toEqual({ code: 'UNAUTHORIZED', reason: 'signed-out' });
  expect(again.http.exchanges).toEqual([]);
});

it('checks the owner by their password typed again, and leaves the session as it was', async () => {
  server = await startServer();
  await server.makeUser({ username: 'alex', name: 'Alex', password: 'alex has a long password' });
  const signedIn = await (await household()).connected.signIn({ username: 'alex' }, { password: 'alex has a long password' });
  const session: SessionBox = { value: signedIn.session };
  const asAlex = await household({ session });

  await asAlex.connected.verifyOwner!({ password: 'alex has a long password' });
  expect(await refusal(asAlex.connected.verifyOwner!({ password: 'not it' }))).toMatchObject({ code: 'UNAUTHORIZED' });
  expect(session.value).toBe(signedIn.session);
  expect((await asAlex.connected.me()).username).toBe('alex');
});
