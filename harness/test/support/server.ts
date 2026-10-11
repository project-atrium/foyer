// The real binary on a temporary data directory and a free port: one server
// per test, so each starts with no users and a fresh rate limiter.
import { execFile, spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { promisify } from 'node:util';

import { inject } from 'vitest';

const run = promisify(execFile);

export interface Server {
  readonly url: string;
  /** What the household's superuser does in the dashboard: makes a user — a profile — and answers its id. */
  makeUser(user: { readonly username: string; readonly name: string; readonly password: string; readonly pin?: string }): Promise<string>;
  /** And sets a user's password, which ends every session of it. */
  setPassword(userId: string, password: string): Promise<void>;
  /** Pair a device: a code that signs these users in. */
  pairingCode(userIds: readonly string[]): Promise<string>;
  /** The devices the Household page lists, with their users' names. */
  devices(): Promise<readonly { readonly name: string; readonly platform: string; readonly appVersion: string; readonly users: readonly string[]; readonly paired: boolean }[]>;
  /** A WebDAV share set up on the Adapters page, and its connection's id. */
  share(address: string): Promise<string>;
  /** One of its folders made a library, and the library's id. */
  addLibrary(connection: string, folder: string, name: string, kind: 'movies' | 'shows'): Promise<string>;
  /** Scan now, and wait until the library holds this many titles. */
  scan(library: string, titles: number): Promise<void>;
  /** What Live Activity shows playing now. */
  playings(): Promise<readonly Playing[]>;
  /** Live Activity's Stop. */
  stopPlaying(id: string): Promise<void>;
  /** What the server printed, for a failure's message. */
  output(): string;
  stop(): Promise<void>;
}

/** A playing as Live Activity shows it. */
export interface Playing {
  readonly id: string;
  readonly user: { readonly id: string; readonly name: string };
  readonly device: { readonly name: string; readonly platform: string };
  readonly kind: string;
  readonly item: { readonly title: string };
  readonly positionMs: number;
  readonly bytes: number;
}

const SUPERUSER = { email: 'owner@example.com', password: 'the dashboard password' };

export async function startServer(): Promise<Server> {
  const binary = inject('binary');
  const directory = mkdtempSync(join(tmpdir(), 'foyer-harness-'));
  const data = join(directory, 'pb_data');
  // Nothing from the shell that runs the tests: its FOYER_* would change the server under test.
  const env = { PATH: process.env.PATH ?? '', HOME: directory };
  const port = await freePort();
  const url = `http://127.0.0.1:${port}`;
  const child = spawn(binary, ['serve', '--dir', data, '--http', `127.0.0.1:${port}`], { cwd: directory, env, stdio: ['ignore', 'pipe', 'pipe'] });
  let output = '';
  child.stdout.on('data', (chunk: Buffer) => (output += chunk.toString()));
  child.stderr.on('data', (chunk: Buffer) => (output += chunk.toString()));

  const stop = async () => {
    if (child.exitCode === null && child.signalCode === null) {
      child.kill('SIGTERM');
      await once(child, 'exit');
    }
    rmSync(directory, { recursive: true, force: true });
  };

  try {
    await until(async () => (await fetch(`${url}/api/health`)).ok, 20_000);
  } catch (error) {
    await stop();
    throw new Error(`The server did not start:\n${output}`, { cause: error });
  }

  const command = async (...args: readonly string[]) => (await run(binary, [...args, '--dir', data], { cwd: directory, env })).stdout.trim();
  let superuser: Promise<string> | undefined;
  const superuserToken = () =>
    (superuser ??= (async () => {
      await command('superuser', 'upsert', SUPERUSER.email, SUPERUSER.password);
      const response = await fetch(`${url}/api/collections/_superusers/auth-with-password`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ identity: SUPERUSER.email, password: SUPERUSER.password }),
      });
      if (!response.ok) throw new Error(`The superuser could not sign in (${response.status}): ${await response.text()}`);
      return ((await response.json()) as { token: string }).token;
    })());

  return {
    url,
    makeUser: async (user) => {
      const response = await fetch(`${url}/api/collections/users/records`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: await superuserToken() },
        body: JSON.stringify({ ...user, passwordConfirm: user.password }),
      });
      if (!response.ok) throw new Error(`The user could not be made (${response.status}): ${await response.text()}`);
      return ((await response.json()) as { id: string }).id;
    },
    setPassword: async (userId, password) => {
      const response = await fetch(`${url}/api/collections/users/records/${userId}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json', Authorization: await superuserToken() },
        body: JSON.stringify({ password, passwordConfirm: password }),
      });
      if (!response.ok) throw new Error(`The password could not be set (${response.status}): ${await response.text()}`);
    },
    pairingCode: async (userIds) => {
      const response = await fetch(`${url}/api/foyer/admin/pairings`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: await superuserToken() },
        body: JSON.stringify({ users: userIds }),
      });
      if (!response.ok) throw new Error(`No code was made (${response.status}): ${await response.text()}`);
      return ((await response.json()) as { code: string }).code;
    },
    devices: async () => {
      const response = await fetch(`${url}/api/foyer/admin/household`, { headers: { Authorization: await superuserToken() } });
      if (!response.ok) throw new Error(`The household could not be read (${response.status}): ${await response.text()}`);
      return ((await response.json()) as { devices: Awaited<ReturnType<Server['devices']>> }).devices;
    },
    share: async (address) => {
      const response = await fetch(`${url}/api/foyer/admin/connections`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: await superuserToken() },
        body: JSON.stringify({ adapter: 'webdav', name: 'Zurg', enabled: true, values: { url: address } }),
      });
      if (!response.ok) throw new Error(`The share could not be set up (${response.status}): ${await response.text()}`);
      return ((await response.json()) as { id: string }).id;
    },
    addLibrary: async (connection, folder, name, kind) => {
      const response = await fetch(`${url}/api/foyer/admin/libraries`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: await superuserToken() },
        body: JSON.stringify({ connection, folder, name, kind, enabled: true }),
      });
      if (!response.ok) throw new Error(`The library could not be made (${response.status}): ${await response.text()}`);
      return ((await response.json()) as { id: string }).id;
    },
    scan: async (library, titles) => {
      const token = await superuserToken();
      const asked = await fetch(`${url}/api/foyer/admin/scan`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: token },
        body: JSON.stringify({ library, full: true }),
      });
      if (!asked.ok) throw new Error(`No scan (${asked.status}): ${await asked.text()}`);
      await until(async () => {
        const response = await fetch(`${url}/api/foyer/admin/library?library=${library}`, { headers: { Authorization: token } });
        return response.ok && ((await response.json()) as { totalItems: number }).totalItems >= titles;
      }, 20_000).catch((error: unknown) => {
        throw new Error(`The scan found too little:\n${output}`, { cause: error });
      });
    },
    playings: async () => {
      const response = await fetch(`${url}/api/foyer/admin/sessions`, { headers: { Authorization: await superuserToken() } });
      if (!response.ok) throw new Error(`Live Activity could not be read (${response.status}): ${await response.text()}`);
      return ((await response.json()) as { sessions: readonly Playing[] }).sessions;
    },
    stopPlaying: async (id) => {
      const response = await fetch(`${url}/api/foyer/admin/sessions/${id}/stop`, { method: 'POST', headers: { Authorization: await superuserToken() } });
      if (!response.ok) throw new Error(`The playing could not be stopped (${response.status}): ${await response.text()}`);
    },
    output: () => output,
    stop,
  };
}

export function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const probe = createServer();
    probe.on('error', reject);
    probe.listen(0, '127.0.0.1', () => {
      const address = probe.address();
      probe.close(() => (typeof address === 'object' && address ? resolve(address.port) : reject(new Error('No port.'))));
    });
  });
}

export async function until(ready: () => Promise<boolean>, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    try {
      if (await ready()) return;
    } catch {
      // Not listening yet.
    }
    if (Date.now() > deadline) throw new Error('Timed out.');
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
}
