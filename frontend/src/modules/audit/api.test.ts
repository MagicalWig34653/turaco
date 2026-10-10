import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../../platform/api/client';
import { errorMessageKey } from '../../platform/api/errorMessages';
import { auditApi } from './api';

afterEach(() => vi.unstubAllGlobals());

describe('audit error codes', () => {
  it('registers audit filter and export codes', () => {
    expect(errorMessageKey({ code: 'audit.invalid_filter', status: 400 })).toBe(
      'error.invalidFilter',
    );
    expect(errorMessageKey({ code: 'audit.export_rate_limited', status: 429 })).toBe(
      'audit.export.error.rateLimited',
    );
  });
});

describe('exportCsv', () => {
  it('sends the range, filters and the details switch', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response('a,b', { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    const blob = await auditApi.exportCsv(
      { from: '2026-01-01T00:00:00Z', to: '2026-01-02T00:00:00Z', module: 'platform' },
      true,
    );
    expect(await blob.text()).toBe('a,b');
    const url = String(fetchMock.mock.calls[0]?.[0]);
    expect(url).toContain('/api/v1/audit-events/export.csv?');
    expect(url).toContain('includeDetails=true');
    expect(url).toContain('module=platform');
  });

  it('turns a 429 into an ApiError with Retry-After', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({ error: { code: 'audit.export_rate_limited', message: 'x' } }),
          {
            status: 429,
            headers: { 'Retry-After': '120' },
          },
        ),
      ),
    );
    const failure = await auditApi.exportCsv({}, false).catch((e: unknown) => e);
    expect(failure).toBeInstanceOf(ApiError);
    expect((failure as ApiError).retryAfterSeconds).toBe(120);
    expect((failure as ApiError).status).toBe(429);
  });
});
