import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type {
  Announcements,
  BriefingCreateBody,
  BriefingItem,
  BriefingStatus,
  BriefingUpdateBody,
  FeedResult,
} from './types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;
const base = '/briefing-items';

registerErrorMessages({
  'briefing.version_conflict': 'error.versionConflict',
  'briefing.invalid_transition': 'error.invalidTransition',
  'briefing.not_found': 'error.notFound',
});
registerErrorResolver((error) =>
  error.code.startsWith('briefing.invalid_') ? 'error.invalidRequest' : undefined,
);

export const briefingApi = {
  /** Any signed-in user: audience-all items and the public status of open, non-exercise incidents. */
  announcements: (signal?: Signal) => api.get<Announcements>('/briefing/announcements', { signal }),
  feed: (signal?: Signal) => api.get<FeedResult>('/briefing/feed', { signal }),
  /** Viewers get published, unexpired items; managers get all and may filter by status. */
  list: (status: BriefingStatus | '', cursor?: string, signal?: Signal) =>
    api.get<Page<BriefingItem>>(base, { signal, query: { status, limit: 50, cursor } }),
  get: (id: string, signal?: Signal) => api.get<BriefingItem>(`${base}/${enc(id)}`, { signal }),
  create: (body: BriefingCreateBody) => api.post<BriefingItem>(base, body),
  update: (id: string, body: BriefingUpdateBody) =>
    api.patch<BriefingItem>(`${base}/${enc(id)}`, body),
  publish: (id: string, expectedVersion: number) =>
    api.post<BriefingItem>(`${base}/${enc(id)}/publish`, { expectedVersion }),
  withdraw: (id: string, expectedVersion: number) =>
    api.post<BriefingItem>(`${base}/${enc(id)}/withdraw`, { expectedVersion }),
  remove: (id: string, expectedVersion: number) =>
    api.delete<void>(`${base}/${enc(id)}`, { query: { expectedVersion } }),
};
