// A pretend Zurg — dev/fakezurg, the real binary — over records written for
// one test: never the household's own, whose records hold Real-Debrid's
// links. Every file is the same sample bytes, so a ranged read can be held
// to them.
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { inject } from 'vitest';

import { freePort, until } from './server';

export interface Zurg {
  /** The share's WebDAV root, as a household sets it up: `…/dav`. */
  readonly url: string;
  /** What every file holds. */
  readonly sample: Uint8Array;
  stop(): Promise<void>;
}

/** A torrent as Zurg's record of it says: its name, when it came, and its files. */
export interface Torrent {
  readonly name: string;
  readonly added: string;
  readonly files: readonly string[];
}

/** What every file holds: four kilobytes that are not video, unless a test brings a video of its own. */
export async function startZurg(torrents: readonly Torrent[], options: { readonly video?: Uint8Array } = {}): Promise<Zurg> {
  const directory = mkdtempSync(join(tmpdir(), 'foyer-harness-zurg-'));
  const folder = mkdtempSync(join(directory, 'torrents-'));
  // Four kilobytes that are not video: nothing here decodes them, and every byte is known.
  const sample = options.video ?? new Uint8Array(4096).map((_, index) => (index * 7 + 3) % 256);
  torrents.forEach((torrent, index) => {
    const files = Object.fromEntries(
      torrent.files.map((file) => [file, { path: `/${file}`, bytes: sample.length, selected: 1, State: 'ok_file', Ended: torrent.added }]),
    );
    writeFileSync(join(folder, `${index}.zurgtorrent`), JSON.stringify({ Name: torrent.name, Added: torrent.added, SelectedFiles: files }));
  });
  const samplePath = join(directory, 'sample.bin');
  writeFileSync(samplePath, sample);

  const port = await freePort();
  const child = spawn(inject('fakezurg'), ['-addr', `127.0.0.1:${port}`, '-sample', samplePath, folder], { stdio: ['ignore', 'pipe', 'pipe'] });
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
  const url = `http://127.0.0.1:${port}/dav`;
  try {
    await until(async () => (await fetch(`${url}/`, { method: 'PROPFIND', headers: { Depth: '1' } })).status < 500, 10_000);
  } catch (error) {
    await stop();
    throw new Error(`fakezurg did not start:\n${output}`, { cause: error });
  }
  return { url, sample, stop };
}
