import { api } from '../../platform/api/client';
import type { Page } from '../../platform/api/types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

export const incidentStatuses = [
  'identified',
  'investigating',
  'mitigating',
  'monitoring',
  'resolved',
  'closed',
] as const;
export type IncidentStatus = (typeof incidentStatuses)[number];

export type MajorIncident = {
  id: string;
  reference: string;
  title: string;
  summary: string;
  status: IncidentStatus;
  resolvedAt: string | null;
  closedAt: string | null;
  subscribed: boolean;
  linkedTickets: number;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type MajorIncidentDetail = MajorIncident & {
  updates: Array<{ id: string; status: string; body: string; createdAt: string }>;
  /** Linked tickets the caller may see; absent on servers that do not return them yet. */
  tickets?: Array<{ id: string; reference: string; title: string; status: string }>;
  allowedOperations: string[];
};

export const incidentsApi = {
  list: (activeOnly: boolean, cursor?: string, signal?: Signal) =>
    api.get<Page<MajorIncident>>('/major-incidents', {
      signal,
      query: { active: activeOnly, limit: 50, cursor },
    }),
  get: (id: string, signal?: Signal) =>
    api.get<MajorIncidentDetail>(`/major-incidents/${enc(id)}`, { signal }),
  declare: (title: string, message: string) =>
    api.post<MajorIncident>('/major-incidents', { title, message }),
  postUpdate: (id: string, message: string) =>
    api.post<MajorIncident>(`/major-incidents/${enc(id)}/updates`, { message }),
  operate: (id: string, op: string, expectedVersion: number, message = '') =>
    api.post<MajorIncident>(`/major-incidents/${enc(id)}/${op}`, { expectedVersion, message }),
  subscribe: (id: string, on: boolean) =>
    api.post<void>(`/major-incidents/${enc(id)}/${on ? 'subscribe' : 'unsubscribe'}`, {}),
  linkTicket: (id: string, ticketId: string) =>
    api.post<void>(`/major-incidents/${enc(id)}/tickets`, { ticketId }),
};
