import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type { RequestStatus, ServiceRequest, ServiceRequestDetail, SubmitBody } from './types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

registerErrorMessages({
  'requests.invalid_answers': 'error.requestsInvalidAnswers',
  'requests.invalid_transition': 'error.invalidTransition',
  'requests.version_conflict': 'error.versionConflict',
  'requests.item_inactive': 'error.requestsItemInactive',
  'requests.no_eligible_approver': 'error.requestsNoApprover',
  'requests.requested_for_invalid': 'error.requestsRequestedForInvalid',
  'requests.not_found': 'error.notFound',
});
registerErrorResolver((error) =>
  error.code.startsWith('requests.invalid_') ? 'error.invalidRequest' : undefined,
);

export const requestsApi = {
  list: (scope: 'mine' | 'all', status: RequestStatus | '', cursor?: string, signal?: Signal) =>
    api.get<Page<ServiceRequest>>('/service-requests', {
      signal,
      query: { scope, status, limit: 50, cursor },
    }),
  get: (id: string, signal?: Signal) =>
    api.get<ServiceRequestDetail>(`/service-requests/${enc(id)}`, { signal }),
  submit: (body: SubmitBody) => api.post<ServiceRequest>('/service-requests', body),
  cancel: (id: string, reason: string, expectedVersion: number) =>
    api.post<ServiceRequest>(`/service-requests/${enc(id)}/cancel`, { reason, expectedVersion }),
  hold: (id: string, reason: string, expectedVersion: number) =>
    api.post<ServiceRequest>(`/service-requests/${enc(id)}/hold`, { reason, expectedVersion }),
  resume: (id: string, expectedVersion: number) =>
    api.post<ServiceRequest>(`/service-requests/${enc(id)}/resume`, { expectedVersion }),
  complete: (id: string, reason: string, expectedVersion: number) =>
    api.post<ServiceRequest>(`/service-requests/${enc(id)}/complete`, { reason, expectedVersion }),
};
