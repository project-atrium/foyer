import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import { defineConfig } from 'vitest/config';

// The same aliases as tsconfig: one @loge/api, and the adapters the app ships, from source.
const source = (path: string) => fileURLToPath(new URL(`../../loge/adapters/${path}`, import.meta.url));

// Loge and Foyer share one version: the server must answer the app's.
const appVersion = (JSON.parse(readFileSync(new URL('../../loge/package.json', import.meta.url), 'utf8')) as { version: string }).version;

export default defineConfig({
  resolve: {
    alias: {
      '@loge/api': source('api/src/index.ts'),
    },
  },
  test: {
    include: ['test/**/*.test.ts'],
    globalSetup: ['test/support/build.ts'],
    provide: { appVersion },
    // Each test starts a server of its own; building the binary is done once, before any of them.
    testTimeout: 30_000,
    hookTimeout: 30_000,
  },
});
