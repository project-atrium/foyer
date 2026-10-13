// A pretend Stalker portal — dev/fakeportal, the real binary: a few channels,
// a guide around now, and each channel's stream made by ffmpeg as it is
// asked for. Like a real subscription, it plays as many streams as it allows,
// and a new one past that ends the oldest: what Foyer's lines must never
// make it do.
import { spawn } from 'node:child_process';
import { once } from 'node:events';

import { inject } from 'vitest';

import { freePort, until } from './server';

export interface Portal {
  /** The address a provider hands out: `…/c/`. */
  readonly address: string;
  readonly mac: string;
  /** What streams are open now, and how often each channel's was asked for. */
  streams(): Promise<{ readonly open: Readonly<Record<string, number>>; readonly asked: Readonly<Record<string, number>> }>;
  output(): string;
  stop(): Promise<void>;
}

export async function startPortal(options: { readonly streams?: number } = {}): Promise<Portal> {
  const port = await freePort();
  const mac = '00:1A:79:00:00:01';
  const child = spawn(inject('fakeportal'), ['-addr', `127.0.0.1:${port}`, '-mac', mac, '-streams', String(options.streams ?? 1)], { stdio: ['ignore', 'pipe', 'pipe'] });
  let output = '';
  child.stdout.on('data', (chunk: Buffer) => (output += chunk.toString()));
  child.stderr.on('data', (chunk: Buffer) => (output += chunk.toString()));
  const base = `http://127.0.0.1:${port}`;
  const stop = async () => {
    if (child.exitCode === null && child.signalCode === null) {
      child.kill('SIGTERM');
      await once(child, 'exit');
    }
  };
  try {
    await until(async () => (await fetch(`${base}/debug/streams`)).ok, 10_000);
  } catch (error) {
    await stop();
    throw new Error(`fakeportal did not start:\n${output}`, { cause: error });
  }
  return {
    address: `${base}/c/`,
    mac,
    streams: async () => (await (await fetch(`${base}/debug/streams`)).json()) as Awaited<ReturnType<Portal['streams']>>,
    output: () => output,
    stop,
  };
}
