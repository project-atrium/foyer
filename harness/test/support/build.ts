// Builds the server once for the whole run, as a real binary: the harness
// tests what ships, never PocketBase in-process.
import { execFileSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

import type { TestProject } from 'vitest/node';

declare module 'vitest' {
  export interface ProvidedContext {
    readonly binary: string;
    readonly fakezurg: string;
    readonly appVersion: string;
  }
}

const repository = fileURLToPath(new URL('../../..', import.meta.url));

export default function setup(project: TestProject) {
  const directory = mkdtempSync(join(tmpdir(), 'foyer-harness-build-'));
  const binary = join(directory, 'foyer');
  execFileSync('go', ['build', '-o', binary, '.'], { cwd: repository, stdio: 'inherit' });
  project.provide('binary', binary);
  // A pretend Zurg, for a library: never the household's own, whose records hold Real-Debrid's links.
  const fakezurg = join(directory, 'fakezurg');
  execFileSync('go', ['build', '-o', fakezurg, './dev/fakezurg'], { cwd: repository, stdio: 'inherit' });
  project.provide('fakezurg', fakezurg);
  return () => rmSync(directory, { recursive: true, force: true });
}
