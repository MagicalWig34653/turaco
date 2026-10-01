import { describe, expect, it, vi } from 'vitest';
import { ApiClient, ApiError, buildQuery, parseRetryAfter } from './client';
import { errorMessageKey } from './errorMessages';

function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

describe('buildQuery', () => {
  it('omits empty, null, undefined and false values', () => {
    expect(buildQuery({ a: 'x', b: '', c: undefined, d: null, e: false, f: true, g: 0 })).toBe(
      '?a=x&f=true&g=0',
    );
    expect(buildQuery({})).toBe('');
    expect(buildQuery()).toBe('');
  });
});

describe('parseRetryAfter', () => {
  it('parses delta seconds only', () => {
    expect(parseRetryAfter('120')).toBe(120);
    expect(parseRetryAfter(null)).toBeUndefined();
    expect(parseRetryAfter('Wed, 21 Oct 2026 07:28:00 GMT')).toBeUndefined();
  });
});

describe('ApiClient', () => {
  it('sends same-origin JSON requests below /api/v1', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(json(200, { ok: true }));
    const client = new ApiClient(fetchImpl);
    await expect(client.post('/roles', { key: 'a' }, { query: { x: '1' } })).resolves.toEqual({
      ok: true,
    });
    const [url, init] = fetchImpl.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/roles?x=1');
    expect(init.method).toBe('POST');
    expect(init.credentials).toBe('same-origin');
    expect(init.body).toBe('{"key":"a"}');
    expect((init.headers as Record<string, string>)['Content-Type']).toBe('application/json');
  });

  it('returns undefined for 204', async () => {
    const client = new ApiClient(() => Promise.resolve(new Response(null, { status: 204 })));
    await expect(client.post('/auth/logout')).resolves.toBeUndefined();
  });

  it('maps the error envelope to ApiError including Retry-After', async () => {
    const client = new ApiClient(() =>
      Promise.resolve(
        json(
          429,
          { error: { code: 'auth.too_many_attempts', message: 'slow down', requestId: 'r1' } },
          { 'Retry-After': '90' },
        ),
      ),
    );
    const error = await client.post('/auth/login', {}).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({
      status: 429,
      code: 'auth.too_many_attempts',
      message: 'slow down',
      requestId: 'r1',
      retryAfterSeconds: 90,
    });
  });

  it('falls back to a generic error for non-envelope bodies', async () => {
    const client = new ApiClient(() => Promise.resolve(new Response('oops', { status: 502 })));
    const error = (await client.get('/roles').catch((e: unknown) => e)) as ApiError;
    expect(error.status).toBe(502);
    expect(error.code).toBe('platform.unknown_error');
  });

  it('maps transport failures to status 0', async () => {
    const client = new ApiClient(() => Promise.reject(new TypeError('failed')));
    const error = (await client.get('/roles').catch((e: unknown) => e)) as ApiError;
    expect(error).toMatchObject({ status: 0, code: 'platform.network_error' });
  });

  it('notifies on 401 unless the call opts out', async () => {
    const client = new ApiClient(() =>
      Promise.resolve(json(401, { error: { code: 'platform.unauthenticated', message: 'x' } })),
    );
    const listener = vi.fn();
    const off = client.onUnauthorized(listener);
    await client.get('/roles').catch(() => undefined);
    expect(listener).toHaveBeenCalledTimes(1);
    await client.get('/auth/session', { skipUnauthorizedHandler: true }).catch(() => undefined);
    expect(listener).toHaveBeenCalledTimes(1);
    off();
    await client.get('/roles').catch(() => undefined);
    expect(listener).toHaveBeenCalledTimes(1);
  });
});

describe('errorMessageKey', () => {
  it('maps platform and auth codes and falls back by status', () => {
    expect(errorMessageKey({ code: 'platform.csrf_rejected', status: 403 })).toBe('error.csrf');
    expect(errorMessageKey({ code: 'auth.temporarily_unavailable', status: 503 })).toBe(
      'login.error.temporarilyUnavailable',
    );
    expect(errorMessageKey({ code: 'audit.invalid_cursor', status: 400 })).toBe(
      'error.invalidRequest',
    );
    expect(errorMessageKey({ code: 'whatever', status: 403 })).toBe('error.forbidden');
    expect(errorMessageKey({ code: 'whatever', status: 418 })).toBe('error.generic');
  });

  it('does not know feature codes until the module registers them', () => {
    expect(errorMessageKey({ code: 'unregistered.code', status: 409 })).toBe('error.generic');
  });
});
