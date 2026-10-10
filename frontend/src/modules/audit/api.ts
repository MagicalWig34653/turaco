import { ApiError, NETWORK_ERROR_CODE, buildQuery, toApiError } from '../../platform/api/client';
import { api } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';
import { exportQuery } from './auditModel';
import type { AuditEventList, AuditFilter, AuditRetention } from './types';

type Signal = AbortSignal | undefined;

registerErrorMessages({
  'audit.invalid_filter': 'error.invalidFilter',
  'audit.range_required': 'audit.export.error.rangeRequired',
  'audit.range_too_long': 'audit.export.error.rangeTooLong',
  'audit.export_too_large': 'audit.export.error.tooLarge',
  'audit.export_rate_limited': 'audit.export.error.rateLimited',
});

export const auditApi = {
  /** Ordered by occurredAt desc, then id; paginate with the opaque nextCursor. */
  events: (filter: AuditFilter, cursor?: string, signal?: Signal) =>
    api.get<AuditEventList>('/audit-events', {
      signal,
      query: { ...filter, limit: 50, cursor },
    }),
  retention: (signal?: Signal) => api.get<AuditRetention>('/audit-events/retention', { signal }),
  /**
   * Downloads the CSV (the API client only reads JSON). Failures are thrown as ApiError so the
   * range, size and rate-limit answers (including Retry-After) reach the caller.
   */
  async exportCsv(filter: AuditFilter, includeDetails: boolean): Promise<Blob> {
    let response: Response;
    try {
      response = await fetch(
        `/api/v1/audit-events/export.csv${buildQuery(exportQuery(filter, includeDetails))}`,
        { credentials: 'same-origin', headers: { Accept: 'text/csv' } },
      );
    } catch {
      throw new ApiError({ status: 0, code: NETWORK_ERROR_CODE, message: 'Network error' });
    }
    if (!response.ok) throw await toApiError(response);
    return response.blob();
  },
};
