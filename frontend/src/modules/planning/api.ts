import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type {
  Initiative,
  InitiativeDetail,
  InitiativeList,
  ItemPage,
  Milestone,
  TransitionList,
  Calendar,
  ItemType,
} from './types';
const enc = encodeURIComponent;
registerErrorMessages({
  'planning.not_found': 'error.notFound',
  'planning.invalid_reference': 'planning.error.reference',
  'planning.invalid_cursor': 'error.invalidRequest',
  'planning.invalid_limit': 'error.invalidRequest',
  'planning.invalid_transition': 'planning.error.transition',
  'planning.version_conflict': 'error.versionConflict',
  'planning.no_eligible_approver': 'planning.error.approver',
  'planning.limit_reached': 'planning.error.limit',
});
registerErrorResolver((error) =>
  error.code === 'planning.invalid_request' ? 'error.invalidRequest' : undefined,
);
export const planningApi = {
  list: (filter: Record<string, string>, cursor?: string, signal?: AbortSignal) =>
    api.get<InitiativeList>('/initiatives', { query: { ...filter, cursor, limit: 50 }, signal }),
  get: (id: string, signal?: AbortSignal) =>
    api.get<InitiativeDetail>(`/initiatives/${enc(id)}`, { signal }),
  create: (body: { title: string; goal: string; ownerUserId: string; targetDate: string }) =>
    api.post<Initiative>('/initiatives', body),
  update: (
    id: string,
    body: {
      expectedVersion: number;
      title?: string;
      goal?: string;
      ownerUserId?: string;
      targetDate?: string;
    },
  ) => api.patch<Initiative>(`/initiatives/${enc(id)}`, body),
  action: (id: string, action: string, body: Record<string, unknown>) =>
    api.post<Initiative>(`/initiatives/${enc(id)}/${action}`, body),
  items: (id: string, cursor?: string, signal?: AbortSignal) =>
    api.get<ItemPage>(`/initiatives/${enc(id)}/items`, { query: { cursor, limit: 100 }, signal }),
  addItem: (id: string, type: ItemType, itemId: string, expectedVersion: number) =>
    api.post(`/initiatives/${enc(id)}/items`, { type, id: itemId, expectedVersion }),
  removeItem: (id: string, type: ItemType, itemId: string, expectedVersion: number) =>
    api.delete<void>(`/initiatives/${enc(id)}/items/${enc(type)}/${enc(itemId)}`, {
      query: { expectedVersion },
    }),
  addMilestone: (
    id: string,
    body: { expectedVersion: number; title: string; dueDate: string; position?: number },
  ) => api.post<Milestone>(`/initiatives/${enc(id)}/milestones`, body),
  updateMilestone: (
    id: string,
    mid: string,
    body: { expectedVersion: number; title?: string; dueDate?: string; position?: number },
  ) => api.patch<Milestone>(`/initiatives/${enc(id)}/milestones/${enc(mid)}`, body),
  milestoneAction: (
    id: string,
    mid: string,
    action: string,
    expectedVersion: number,
    reason?: string,
  ) =>
    api.post<Milestone>(`/initiatives/${enc(id)}/milestones/${enc(mid)}/${action}`, {
      expectedVersion,
      reason,
    }),
  transitions: (id: string, cursor?: string, signal?: AbortSignal) =>
    api.get<TransitionList>(`/initiatives/${enc(id)}/transitions`, {
      query: { cursor, limit: 50 },
      signal,
    }),
  calendar: (from: string, to: string, signal?: AbortSignal) =>
    api.get<Calendar>('/maintenance-calendar', { query: { from, to }, signal }),
};
