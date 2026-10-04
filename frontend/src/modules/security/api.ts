import { api } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';
import type {
  Advisory,
  AdvisoryActionResponse,
  AdvisoryDetail,
  Criterion,
  Finding,
  Page,
  Summary,
  Transition,
  RemediationTask,
  LinkedChange,
  Progress,
  Overview,
} from './types';
const enc = encodeURIComponent;
registerErrorMessages({
  'security.not_found': 'error.notFound',
  'security.invalid_request': 'error.invalidRequest',
  'security.invalid_reference': 'security.error.reference',
  'security.invalid_cursor': 'error.invalidRequest',
  'security.invalid_limit': 'error.invalidRequest',
  'security.invalid_transition': 'security.error.transition',
  'security.version_conflict': 'error.versionConflict',
  'security.duplicate': 'security.error.duplicate',
});
export const securityApi = {
  overview: (signal?: AbortSignal) => api.get<Overview>('/security/overview', { signal }),
  progress: (id: string, signal?: AbortSignal) =>
    api.get<Progress>(`/security/advisories/${enc(id)}/progress`, { signal }),
  tasks: (kind: 'advisories' | 'findings', id: string, signal?: AbortSignal) =>
    api.get<{ items: RemediationTask[] }>(`/security/${kind}/${enc(id)}/tasks`, { signal }),
  createTask: (
    kind: 'advisories' | 'findings',
    id: string,
    body: {
      expectedVersion: number;
      assignedUserId?: string;
      assignedTeamId?: string;
      dueAt?: string;
    },
  ) => api.post<{ taskId: string }>(`/security/${kind}/${enc(id)}/tasks`, body),
  changes: (id: string, signal?: AbortSignal) =>
    api.get<{ items: LinkedChange[] }>(`/security/advisories/${enc(id)}/changes`, { signal }),
  linkChange: (id: string, changeId: string, expectedVersion: number) =>
    api.post<LinkedChange>(`/security/advisories/${enc(id)}/changes`, {
      changeId,
      expectedVersion,
    }),
  unlinkChange: (id: string, changeId: string, expectedVersion: number) =>
    api.delete<void>(`/security/advisories/${enc(id)}/changes/${enc(changeId)}`, {
      body: { expectedVersion },
    }),
  advisories: (filter: Record<string, string>, cursor?: string, signal?: AbortSignal) =>
    api.get<Page<Advisory>>('/security/advisories', {
      query: { ...filter, cursor, limit: 50 },
      signal,
    }),
  advisory: (id: string, signal?: AbortSignal) =>
    api.get<AdvisoryDetail>(`/security/advisories/${enc(id)}`, { signal }),
  create: (body: Record<string, unknown>) => api.post<AdvisoryDetail>('/security/advisories', body),
  update: (id: string, body: Record<string, unknown>) =>
    api.patch<Advisory>(`/security/advisories/${enc(id)}`, body),
  criteria: (id: string, expectedVersion: number, criteria: Criterion[]) =>
    api.put<AdvisoryDetail>(`/security/advisories/${enc(id)}/criteria`, {
      expectedVersion,
      criteria,
    }),
  normalize: (id: string, expectedVersion: number) =>
    api.post<AdvisoryDetail>(`/security/advisories/${enc(id)}/normalize`, { expectedVersion }),
  import: (records: unknown[]) =>
    api.post<Record<string, number>>('/security/advisories/import', { records }),
  advisoryAction: (id: string, action: string, expectedVersion: number, reason?: string) =>
    api.post<AdvisoryActionResponse>(`/security/advisories/${enc(id)}/${action}`, {
      expectedVersion,
      reason,
    }),
  advisoryFindings: (id: string, cursor?: string, signal?: AbortSignal) =>
    api.get<Page<Finding>>(`/security/advisories/${enc(id)}/findings`, {
      query: { cursor, limit: 50 },
      signal,
    }),
  summary: (id: string, signal?: AbortSignal) =>
    api.get<Summary>(`/security/advisories/${enc(id)}/summary`, { signal }),
  advisoryTransitions: (id: string, signal?: AbortSignal) =>
    api.get<Page<Transition>>(`/security/advisories/${enc(id)}/transitions`, {
      query: { limit: 50 },
      signal,
    }),
  findings: (filter: Record<string, string>, cursor?: string, signal?: AbortSignal) =>
    api.get<Page<Finding>>('/security/findings', {
      query: { ...filter, cursor, limit: 50 },
      signal,
    }),
  finding: (id: string, signal?: AbortSignal) =>
    api.get<Finding>(`/security/findings/${enc(id)}`, { signal }),
  findingAction: (
    id: string,
    action: string,
    body: { expectedVersion: number; reason?: string; reviewBy?: string },
  ) => api.post<Finding>(`/security/findings/${enc(id)}/${action}`, body),
  findingTransitions: (id: string, signal?: AbortSignal) =>
    api.get<Page<Transition>>(`/security/findings/${enc(id)}/transitions`, {
      query: { limit: 50 },
      signal,
    }),
};
