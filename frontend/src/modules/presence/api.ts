import { api } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';
import type {
  Availability,
  Coverage,
  Entry,
  Minimum,
  Operation,
  PresenceStatus,
  Settings,
} from './types';
registerErrorMessages({
  'presence.disabled': 'presence.error.disabled',
  'presence.version_conflict': 'presence.error.conflict',
  'presence.read_only': 'presence.readOnly',
  'presence.invalid_recurrence': 'presence.error.recurrence',
  'presence.window_too_large': 'presence.windowHint',
  'presence.invalid_request': 'error.invalidRequest',
  'presence.not_permitted': 'error.forbidden',
  'presence.not_found': 'error.notFound',
  'presence.invalid_transition': 'presence.error.conflict',
});
const base = '/presence';
export const presenceApi = {
  status: (signal?: AbortSignal) => api.get<PresenceStatus>(`${base}/status`, { signal }),
  entries: (from: string, to: string, signal?: AbortSignal) =>
    api.get<{ items: Entry[] }>(`${base}/me/entries`, { query: { from, to }, signal }),
  operate: (operation: Operation, id: string | undefined, body: unknown) =>
    api.post<Entry>(
      operation === 'create'
        ? `${base}/entries`
        : `${base}/entries/${encodeURIComponent(id!)}/${operation}`,
      body,
    ),
  availability: (userIds: string, signal?: AbortSignal) =>
    api.get<{ items: Availability[] }>(`${base}/availability`, {
      query: { userIds, at: new Date().toISOString() },
      signal,
    }),
  coverage: (id: string, from: string, to: string, signal?: AbortSignal) =>
    api.get<Coverage>(`${base}/teams/${encodeURIComponent(id)}/coverage`, {
      query: { from, to },
      signal,
    }),
  minimum: (id: string, signal?: AbortSignal) =>
    api.get<{ minimum: Minimum | null }>(`${base}/teams/${encodeURIComponent(id)}/minimum`, {
      signal,
    }),
  setMinimum: (id: string, body: unknown) =>
    api.put<Minimum>(`${base}/teams/${encodeURIComponent(id)}/minimum`, body),
  settings: (signal?: AbortSignal) => api.get<Settings>(`${base}/settings`, { signal }),
  saveSettings: (body: unknown) => api.put<Settings>(`${base}/settings`, body),
  purge: () => api.post<{ deletedEntries: number }>(`${base}/settings/purge`),
};
