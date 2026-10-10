import { afterEach, describe, expect, it, vi } from 'vitest';
import { problemsApi } from './api';

afterEach(() => vi.unstubAllGlobals());

describe('problemsApi.setOwner', () => {
  it('posts the owner with the expected version', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response('{"id":"p1","ownerId":"u1"}', { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    await problemsApi.setOwner('p1', 'u1', 3);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toContain('/api/v1/problems/p1/owner');
    expect(init.method).toBe('POST');
    expect(JSON.parse(String(init.body))).toEqual({ expectedVersion: 3, ownerId: 'u1' });
  });
});
