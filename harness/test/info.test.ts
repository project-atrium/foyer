import { FOYER_PROTOCOL, readFoyerInfo } from '@loge/api';
import { afterEach, expect, inject, it } from 'vitest';

import { fetchHttp } from './support/host';
import { startServer, type Server } from './support/server';

let server: Server | undefined;
afterEach(async () => {
  await server?.stop();
  server = undefined;
});

it("answers info as the app reads it: the app's version, the protocol its adapters speak, and the household", async () => {
  server = await startServer();
  const http = fetchHttp();
  const answer = await http.client.request({ method: 'GET', url: `${server.url}/api/foyer/info` });
  expect(answer.status).toBe(200);
  expect(readFoyerInfo(JSON.parse(answer.text))).toEqual({
    serverVersion: inject('appVersion'),
    protocol: FOYER_PROTOCOL,
    household: { id: expect.stringMatching(/^[a-z0-9]{15}$/), name: 'Foyer' },
  });
});

it('makes a user as the superuser does', async () => {
  server = await startServer();
  const id = await server.makeUser({ username: 'alex', name: 'Alex', password: 'a long password', pin: '1234' });
  expect(id).toMatch(/^[a-z0-9]{15}$/);
});
