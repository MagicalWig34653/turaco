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
  /** A drill: never in the banner, the briefing or Overview counts. */
  isExercise: boolean;
  /** When the next public update is promised; null when none is. */
  nextUpdateDue: string | null;
  /** Only for majorincidents.manage holders. */
  ownerUserId?: string;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type MajorIncidentDetail = MajorIncident & {
  updates: Array<{ id: string; status: string; body: string; createdAt: string }>;
  /** Linked tickets the caller may see; absent on servers that do not return them yet. */
  tickets?: Array<{ id: string; reference: string; title: string; status: string }>;
  allowedOperations: string[];
  locations: Array<{ id: string; name: string }>;
  /** Linked tickets the caller cannot see; only for majorincidents.manage holders, omitted at zero. */
  hiddenLinkedTickets?: number;
  ownerName?: string;
};

export const incidentsApi = {
  /** `exercises: false` hides drills (the banner does that). */
  list: (activeOnly: boolean, cursor?: string, signal?: Signal, exercises = true) =>
    api.get<Page<MajorIncident>>('/major-incidents', {
      signal,
      query: { active: activeOnly, limit: 50, cursor, exercises: exercises ? undefined : 'false' },
    }),
  get: (id: string, signal?: Signal) =>
    api.get<MajorIncidentDetail>(`/major-incidents/${enc(id)}`, { signal }),
  declare: (title: string, message: string, isExercise = false) =>
    api.post<MajorIncident>('/major-incidents', { title, message, isExercise }),
  setOwner: (id: string, expectedVersion: number, ownerUserId: string | null) =>
    api.post<MajorIncident>(`/major-incidents/${enc(id)}/owner`, { expectedVersion, ownerUserId }),
  setNextUpdate: (id: string, expectedVersion: number, dueAt: string | null) =>
    api.post<MajorIncident>(`/major-incidents/${enc(id)}/next-update`, { expectedVersion, dueAt }),
  setLocations: (id: string, expectedVersion: number, locationIds: string[]) =>
    api.post<MajorIncident>(`/major-incidents/${enc(id)}/locations`, {
      expectedVersion,
      locationIds,
    }),
  postUpdate: (id: string, message: string) =>
    api.post<MajorIncident>(`/major-incidents/${enc(id)}/updates`, { message }),
  operate: (id: string, op: string, expectedVersion: number, message = '') =>
    api.post<MajorIncident>(`/major-incidents/${enc(id)}/${op}`, { expectedVersion, message }),
  subscribe: (id: string, on: boolean) =>
    api.post<void>(`/major-incidents/${enc(id)}/${on ? 'subscribe' : 'unsubscribe'}`, {}),
  linkTicket: (id: string, ticketId: string) =>
    api.post<void>(`/major-incidents/${enc(id)}/tickets`, { ticketId }),
};
