import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type { Approval } from './types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

registerErrorMessages({
  'approvals.not_pending': 'error.approvalsNotPending',
  'approvals.not_approver': 'error.approvalsNotApprover',
  'approvals.version_conflict': 'error.versionConflict',
  'approvals.not_found': 'error.notFound',
});
registerErrorResolver((error) =>
  error.code.startsWith('approvals.invalid_') ? 'error.invalidRequest' : undefined,
);

export const approvalsApi = {
  list: (status: 'pending' | 'decided', cursor?: string, signal?: Signal) =>
    api.get<Page<Approval>>('/approvals', { signal, query: { status, limit: 50, cursor } }),
  get: (id: string, signal?: Signal) => api.get<Approval>(`/approvals/${enc(id)}`, { signal }),
  decide: (id: string, decision: 'approve' | 'reject', comment: string, expectedVersion: number) =>
    api.post<Approval>(`/approvals/${enc(id)}/${decision}`, { comment, expectedVersion }),
};
