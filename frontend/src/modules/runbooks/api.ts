import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

registerErrorMessages({ 'knowledge.invalid_transition': 'error.knowledgeInvalidTransition' });
registerErrorResolver(() => undefined);

export type RunbookStep = { title: string; description?: string; teamId?: string };

export type Runbook = {
  id: string;
  reference: string;
  title: string;
  description: string;
  steps: RunbookStep[];
  active: boolean;
  version: number;
  updatedAt: string;
};

export type Execution = {
  id: string;
  runbookId: string;
  runbookTitle: string;
  steps: RunbookStep[];
  contextType: string | null;
  contextId: string | null;
  status: 'running' | 'completed' | 'cancelled';
  taskIds: string[];
  finishedAt: string | null;
  version: number;
  createdAt: string;
};

export const runbooksApi = {
  list: (activeOnly: boolean, cursor?: string, signal?: Signal) =>
    api.get<Page<Runbook>>('/runbooks', {
      signal,
      query: { active: activeOnly, limit: 50, cursor },
    }),
  get: (id: string, signal?: Signal) => api.get<Runbook>(`/runbooks/${enc(id)}`, { signal }),
  create: (body: { title: string; description: string; steps: RunbookStep[] }) =>
    api.post<Runbook>('/runbooks', body),
  update: (
    id: string,
    expectedVersion: number,
    body: { title: string; description: string; steps: RunbookStep[] },
  ) => api.patch<Runbook>(`/runbooks/${enc(id)}`, { expectedVersion, ...body }),
  setActive: (id: string, active: boolean, expectedVersion: number) =>
    api.post<Runbook>(`/runbooks/${enc(id)}/${active ? 'activate' : 'deactivate'}`, {
      expectedVersion,
    }),
  start: (id: string, ticketId?: string) =>
    api.post<Execution>(`/runbooks/${enc(id)}/executions`, ticketId ? { ticketId } : {}),
  executions: (
    filter: { runbookId?: string; ticketId?: string },
    cursor?: string,
    signal?: Signal,
  ) =>
    api.get<Page<Execution>>('/runbook-executions', {
      signal,
      query: { ...filter, limit: 50, cursor },
    }),
  cancel: (id: string, reason: string) =>
    api.post<Execution>(`/runbook-executions/${enc(id)}/cancel`, { reason }),
};
