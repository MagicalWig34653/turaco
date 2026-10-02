import { api } from '../../platform/api/client';
import type { Page } from '../../platform/api/types';
import type { DefinitionCreateBody, DefinitionUpdateBody, RecurringTaskDefinition } from './types';
import '../tasks/api'; // registers the shared tasks.* error messages

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;
const base = '/recurring-task-definitions';

export const recurrenceApi = {
  /** Oldest first; paginate with the opaque nextCursor. */
  list: (cursor?: string, signal?: Signal) =>
    api.get<Page<RecurringTaskDefinition>>(base, { signal, query: { limit: 50, cursor } }),
  get: (id: string, signal?: Signal) =>
    api.get<RecurringTaskDefinition>(`${base}/${enc(id)}`, { signal }),
  create: (body: DefinitionCreateBody) => api.post<RecurringTaskDefinition>(base, body),
  update: (id: string, body: DefinitionUpdateBody) =>
    api.patch<RecurringTaskDefinition>(`${base}/${enc(id)}`, body),
  pause: (id: string, expectedVersion: number) =>
    api.post<RecurringTaskDefinition>(`${base}/${enc(id)}/pause`, { expectedVersion }),
  resume: (id: string, expectedVersion: number) =>
    api.post<RecurringTaskDefinition>(`${base}/${enc(id)}/resume`, { expectedVersion }),
  remove: (id: string, expectedVersion: number) =>
    api.delete<void>(`${base}/${enc(id)}`, { query: { expectedVersion } }),
};
