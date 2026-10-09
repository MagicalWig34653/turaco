import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type { MessageKey } from '../../platform/i18n/i18n';
import type {
  Change,
  ChangeDetail,
  ChangeImpact,
  ChangeList,
  TransitionList,
  ResourceType,
  Affected,
  AffectedCandidate,
} from './types';
const enc = encodeURIComponent;
registerErrorMessages({
  'changes.not_found': 'error.notFound',
  'changes.invalid_reference': 'changes.error.reference',
  'changes.invalid_cursor': 'error.invalidRequest',
  'changes.invalid_limit': 'error.invalidRequest',
  'changes.invalid_transition': 'changes.error.transition',
  'changes.version_conflict': 'error.versionConflict',
  'changes.no_eligible_approver': 'changes.error.approver',
  'changes.review_required': 'changes.error.review',
  'changes.tasks_open': 'changes.error.tasks',
  'changes.limit_reached': 'changes.error.limit',
  'changes.impact_busy': 'changes.error.impactBusy',
  'changes.separation_of_duties': 'changes.error.separation',
  'changes.window_not_approved': 'changes.error.window',
});
const invalidRequestMessages: Record<string, MessageKey> = {
  'expectedVersion is required': 'changes.error.versionRequired',
  'the kind can only be changed in draft': 'changes.error.kindDraft',
  'an outcome note is required for the review': 'changes.error.outcomeRequired',
  'an emergency maintenance window may start at most one hour in the past':
    'changes.error.emergencyWindow',
  'a maintenance window is required': 'changes.error.readyWindow',
  'a rollback plan is required for medium and high risk': 'changes.error.readyRollback',
  'at least one affected resource is required': 'changes.error.readyAffected',
};
registerErrorResolver((error) =>
  error.code === 'changes.invalid_request'
    ? (invalidRequestMessages[error.message ?? ''] ?? 'error.invalidRequest')
    : error.code.startsWith('changes.invalid_')
      ? 'error.invalidRequest'
      : undefined,
);
export type ChangeFields = {
  title: string;
  description: string;
  kind: string;
  risk: string;
  ownerUserId: string;
  rollbackPlan: string;
  window: { start: string | null; end: string | null };
};
export const changesApi = {
  list: (filter: Record<string, string>, cursor?: string, signal?: AbortSignal) =>
    api.get<ChangeList>('/changes', { query: { ...filter, cursor, limit: 50 }, signal }),
  get: (id: string, signal?: AbortSignal) =>
    api.get<ChangeDetail>(`/changes/${enc(id)}`, { signal }),
  /** Picker for the wizard; needs changes.manage. A type the caller cannot see answers with no items. */
  affectedLookup: (type: AffectedCandidate['type'], q: string, signal?: AbortSignal, limit = 20) =>
    api.get<{ items: AffectedCandidate[] }>('/changes/affected-lookup', {
      query: { type, q, limit },
      signal,
    }),
  create: (body: ChangeFields) => api.post<Change>('/changes', body),
  update: (id: string, body: Partial<ChangeFields> & { expectedVersion: number }) =>
    api.patch<Change>(`/changes/${enc(id)}`, body),
  action: (id: string, action: string, body: Record<string, unknown>) =>
    api.post<Change>(`/changes/${enc(id)}/${action}`, body),
  addAffected: (id: string, type: ResourceType, targetId: string, expectedVersion: number) =>
    api.post<Affected>(`/changes/${enc(id)}/affected`, { type, id: targetId, expectedVersion }),
  removeAffected: (id: string, type: ResourceType, targetId: string, expectedVersion: number) =>
    api.delete<void>(`/changes/${enc(id)}/affected/${enc(type)}/${enc(targetId)}`, {
      query: { expectedVersion },
    }),
  addTask: (
    id: string,
    body: {
      expectedVersion: number;
      title: string;
      description: string;
      dueAt?: string;
      assignedUserId?: string;
      assignedTeamId?: string;
    },
  ) => api.post<{ taskId: string }>(`/changes/${enc(id)}/tasks`, body),
  transitions: (id: string, cursor?: string, signal?: AbortSignal) =>
    api.get<TransitionList>(`/changes/${enc(id)}/transitions`, {
      query: { cursor, limit: 50 },
      signal,
    }),
  impact: (id: string, depth: number, signal?: AbortSignal) =>
    api.get<ChangeImpact>(`/changes/${enc(id)}/impact`, { query: { depth }, signal }),
};
