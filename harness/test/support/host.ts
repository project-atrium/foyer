// A device, as far as a plugin can tell: the app's host context rebuilt on
// Node — its crypto on node:crypto, the same algorithms the app runs — and
// the network through fetch, every exchange recorded.
import { createCipheriv, createDecipheriv, createHash, hkdfSync, randomBytes } from 'node:crypto';

import { TransportError, type HttpClient, type PluginContext, type PluginCrypto } from '@loge/api';
import { inject } from 'vitest';

const NONCE = 12;
const TAG = 16;

export function nodeHostCrypto(): PluginCrypto {
  return {
    randomBytes: (length) => new Uint8Array(randomBytes(length)),
    sha256: async (data) => new Uint8Array(createHash('sha256').update(data).digest()),
    expandKey: async (key, info, length) => new Uint8Array(hkdfSync('sha256', key, new Uint8Array(0), Buffer.from(info, 'utf8'), length)),
    seal: async (key, plaintext, context) => {
      const nonce = randomBytes(NONCE);
      const cipher = createCipheriv('aes-256-gcm', key, nonce);
      cipher.setAAD(Buffer.from(context, 'utf8'));
      const body = Buffer.concat([cipher.update(plaintext), cipher.final()]);
      return new Uint8Array(Buffer.concat([nonce, body, cipher.getAuthTag()]));
    },
    open: async (key, sealed, context) => {
      if (sealed.length < NONCE + TAG) return undefined;
      try {
        const decipher = createDecipheriv('aes-256-gcm', key, sealed.subarray(0, NONCE));
        decipher.setAAD(Buffer.from(context, 'utf8'));
        decipher.setAuthTag(sealed.subarray(sealed.length - TAG));
        return new Uint8Array(Buffer.concat([decipher.update(sealed.subarray(NONCE, sealed.length - TAG)), decipher.final()]));
      } catch {
        return undefined;
      }
    },
  };
}

export interface Exchange {
  readonly route: string;
  readonly status: number;
}

/** The app's HTTP port over fetch, as a phone's would be, with every exchange kept. */
export function fetchHttp() {
  const exchanges: Exchange[] = [];
  const client: HttpClient = {
    request: async (request) => {
      if (request.signal?.aborted) throw new TransportError('aborted');
      let response: Response;
      try {
        response = await fetch(request.url, {
          method: request.method,
          ...(request.headers ? { headers: { ...request.headers } } : {}),
          ...(request.body === undefined ? {} : { body: request.body }),
          signal: AbortSignal.timeout(request.timeoutMs ?? 30_000),
        });
      } catch (error) {
        throw new TransportError('unreachable', String(error));
      }
      const headers: Record<string, string> = {};
      response.headers.forEach((value, key) => {
        headers[key.toLowerCase()] = value;
      });
      const text = await response.text();
      exchanges.push({ route: `${request.method} ${new URL(request.url).pathname}`, status: response.status });
      return { status: response.status, headers, text };
    },
  };
  return { client, exchanges, count: (route: string) => exchanges.filter((exchange) => exchange.route === route).length };
}

/** Where a device keeps its session: it outlives a provider, as the keychain outlives a launch. */
export interface SessionBox {
  value: string | undefined;
}

/**
 * A device's context for one adapter, as the app builds it: the network
 * through fetch, its crypto, a session that outlives a provider, and the
 * password fields it signs in with.
 */
export function context(options: {
  readonly name: string;
  readonly installation: string;
  readonly http: HttpClient;
  readonly session: SessionBox;
  readonly credentials?: Readonly<Record<string, string>>;
}): PluginContext {
  const { session } = options;
  return {
    http: options.http,
    credentials: { read: async () => ({ ...options.credentials }) },
    session: {
      read: async () => session.value,
      write: async (value) => {
        session.value = value;
      },
      clear: async () => {
        session.value = undefined;
      },
    },
    network: { current: () => 'wifi' },
    client: { appName: 'Loge', appVersion: inject('appVersion'), deviceName: options.name, installationId: options.installation },
    clock: { now: () => Date.now(), sleep: async () => undefined },
    crypto: nodeHostCrypto(),
  };
}
