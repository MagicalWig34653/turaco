import { api } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type { AuditEvent, AuditFilter } from './types';

type Signal = AbortSignal | undefined;

registerErrorMessages({ 'audit.invalid_filter': 'error.invalidFilter' });

export const auditApi = {
  /** Ordered by occurredAt desc, then id; paginate with the opaque nextCursor. */
  events: (filter: AuditFilter, cursor?: string, signal?: Signal) =>
    api.get<Page<AuditEvent>>('/audit-events', {
      signal,
      query: { ...filter, limit: 50, cursor },
    }),
};
