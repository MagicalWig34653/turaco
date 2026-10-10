import { api } from '../../platform/api/client';
import type { Page } from '../../platform/api/types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

export const problemStatuses = [
  'new',
  'under_investigation',
  'cause_identified',
  'known_error',
  'resolution_planned',
  'resolved',
  'closed',
] as const;
export type ProblemStatus = (typeof problemStatuses)[number];

export type Problem = {
  id: string;
  reference: string;
  title: string;
  description: string | null;
  status: ProblemStatus;
  cause: string | null;
  workaround: string | null;
  resolution: string | null;
  ownerId: string | null;
  linkedTickets: number;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type ProblemDetail = Problem & {
  tickets: Array<{ id: string; reference: string; title: string; status: string }>;
  allowedOperations: string[];
};

export const problemsApi = {
  list: (status: ProblemStatus | '', cursor?: string, signal?: Signal) =>
    api.get<Page<Problem>>('/problems', { signal, query: { status, limit: 50, cursor } }),
  get: (id: string, signal?: Signal) => api.get<ProblemDetail>(`/problems/${enc(id)}`, { signal }),
  create: (title: string, description: string) =>
    api.post<Problem>('/problems', { title, description }),
  operate: (id: string, op: string, expectedVersion: number, text = '') =>
    api.post<Problem>(`/problems/${enc(id)}/${op.replaceAll('_', '-')}`, { expectedVersion, text }),
  setOwner: (id: string, ownerId: string, expectedVersion: number) =>
    api.post<Problem>(`/problems/${enc(id)}/owner`, { expectedVersion, ownerId }),
  linkTicket: (id: string, ticketId: string) =>
    api.post<void>(`/problems/${enc(id)}/tickets`, { ticketId }),
  unlinkTicket: (id: string, ticketId: string) =>
    api.delete<void>(`/problems/${enc(id)}/tickets/${enc(ticketId)}`),
  knownErrors: (ticketId: string, signal?: Signal) =>
    api.get<{ items: Problem[] }>(`/tickets/${enc(ticketId)}/known-errors`, { signal }),
};
