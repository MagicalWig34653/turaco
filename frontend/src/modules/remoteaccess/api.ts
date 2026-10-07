import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type { MessageKey } from '../../platform/i18n/i18n';
import type { Page } from '../../platform/api/types';
import { refusalCodes } from './types';
import type {
  Capabilities,
  NewSession,
  ObservationsSummary,
  PeerMapping,
  RemoteSession,
  SessionDetail,
  SessionFilters,
} from './types';

registerErrorMessages({
  'remoteaccess.invalid_request': 'error.invalidRequest',
  'remoteaccess.invalid_limit': 'error.invalidRequest',
  'remoteaccess.invalid_cursor': 'error.invalidRequest',
  'remoteaccess.not_found': 'error.notFound',
  'remoteaccess.version_conflict': 'error.versionConflict',
  'remoteaccess.invalid_transition': 'remoteaccess.error.invalid_transition',
  'remoteaccess.rate_limited': 'remoteaccess.error.rate_limited',
  'remoteaccess.handle_used': 'remoteaccess.error.handle_used',
  'remoteaccess.handle_expired': 'remoteaccess.error.handle_expired',
  'remoteaccess.handle_active': 'remoteaccess.error.handle_active',
  'remoteaccess.consent_recorded': 'remoteaccess.error.consent_recorded',
  'remoteaccess.peer_taken': 'remoteaccess.error.peer_taken',
  'remoteaccess.no_eligible_approver': 'remoteaccess.error.no_eligible_approver',
  'remoteaccess.launch_failed': 'remoteaccess.error.launch_failed',
  'remoteaccess.approver_not_authorized': 'remoteaccess.error.no_eligible_approver',
});
// Every policy refusal (HTTP 409 remoteaccess.<code>) has its own plain-language message.
registerErrorResolver((error) => {
  const code = error.code.replace(/^remoteaccess\./, '');
  return error.code.startsWith('remoteaccess.') &&
    (refusalCodes as readonly string[]).includes(code)
    ? (`remoteaccess.reason.${code}` as MessageKey)
    : undefined;
});

const enc = encodeURIComponent;

export const remoteAccessApi = {
  capabilities: (deviceId: string, signal?: AbortSignal) =>
    api.get<Capabilities>('/remote-access/capabilities', { signal, query: { deviceId } }),
  sessions: (
    filters: Partial<SessionFilters> & { initiatedBy?: string },
    cursor?: string,
    signal?: AbortSignal,
    limit = 50,
  ) =>
    api.get<Page<RemoteSession>>('/remote-access/sessions', {
      signal,
      query: {
        status: filters.status,
        deviceId: filters.deviceId,
        ticketId: filters.ticketId,
        initiatedBy: filters.initiatedBy,
        limit,
        cursor,
      },
    }),
  session: (id: string, signal?: AbortSignal) =>
    api.get<SessionDetail>(`/remote-access/sessions/${enc(id)}`, { signal }),
  request: (body: NewSession) => api.post<RemoteSession>('/remote-access/sessions', body),
  consent: (id: string, expectedVersion: number, decision: 'granted' | 'declined') =>
    api.post<RemoteSession>(`/remote-access/sessions/${enc(id)}/consent`, {
      expectedVersion,
      decision,
    }),
  close: (id: string, expectedVersion: number, reason: string) =>
    api.post<RemoteSession>(`/remote-access/sessions/${enc(id)}/close`, {
      expectedVersion,
      reason,
    }),
  cancel: (id: string, expectedVersion: number, reason: string) =>
    api.post<RemoteSession>(`/remote-access/sessions/${enc(id)}/cancel`, {
      expectedVersion,
      reason,
    }),
  /**
   * Issues the one-time handle and exchanges it at once. The returned launch link must go straight
   * to the browser and nowhere else (no state, storage, logging or URL).
   */
  launch: async (id: string, expectedVersion: number): Promise<string> => {
    const handle = await api.post<{ token: string }>(`/remote-access/sessions/${enc(id)}/launch`, {
      expectedVersion,
    });
    const out = await api.post<{ launchUri: string }>('/remote-access/launch-handles/exchange', {
      token: handle.token,
    });
    return out.launchUri;
  },
  observationsSummary: (signal?: AbortSignal) =>
    api.get<ObservationsSummary>('/remote-access/observations/summary', { signal }),
  mappings: (deviceId: string, signal?: AbortSignal) =>
    api.get<{ items: PeerMapping[] }>('/remote-access/peer-mappings', {
      signal,
      query: { deviceId },
    }),
  mapPeer: (deviceId: string, provider: string, peerId: string, reason: string) =>
    api.put<PeerMapping>('/remote-access/peer-mappings', { deviceId, provider, peerId, reason }),
  unmapPeer: (deviceId: string, provider: string, reason: string) =>
    api.delete<void>('/remote-access/peer-mappings', { query: { deviceId, provider, reason } }),
};
