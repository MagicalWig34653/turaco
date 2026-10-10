import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type {
  Task,
  TaskAction,
  TaskAssignBody,
  TaskCreateBody,
  TaskFilter,
  TaskUpdateBody,
} from './types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

registerErrorMessages({
  'tasks.version_conflict': 'error.versionConflict',
  'tasks.invalid_transition': 'error.invalidTransition',
  'tasks.assignee_invalid': 'error.assigneeInvalid',
  'tasks.not_found': 'error.notFound',
});
registerErrorResolver((error) =>
  error.code.startsWith('tasks.invalid_') ? 'error.invalidRequest' : undefined,
);

export const tasksApi = {
  /** Shared task order (due date, priority, id); paginate with the opaque nextCursor. */
  list: (filter: TaskFilter, cursor?: string, signal?: Signal) =>
    api.get<Page<Task>>('/tasks', { signal, query: { ...filter, limit: 50, cursor } }),
  myWork: (cursor?: string, signal?: Signal) =>
    api.get<Page<Task>>('/my-work', { signal, query: { limit: 50, cursor } }),
  get: (id: string, signal?: Signal) => api.get<Task>(`/tasks/${enc(id)}`, { signal }),
  create: (body: TaskCreateBody) => api.post<Task>('/tasks', body),
  update: (id: string, body: TaskUpdateBody) => api.patch<Task>(`/tasks/${enc(id)}`, body),
  assign: (id: string, body: TaskAssignBody) => api.post<Task>(`/tasks/${enc(id)}/assign`, body),
  unassign: (id: string, expectedVersion: number) =>
    api.post<Task>(`/tasks/${enc(id)}/unassign`, { expectedVersion }),
  /** Completes a task; the optional result note (at most 1000 characters) is stored as the closing comment. */
  complete: (
    id: string,
    expectedVersion: number,
    resultNote?: string,
    resultNoteForRequester?: boolean,
  ) =>
    api.post<Task>(`/tasks/${enc(id)}/complete`, {
      expectedVersion,
      ...(resultNote === undefined ? {} : { resultNote }),
      ...(resultNote !== undefined && resultNoteForRequester ? { resultNoteForRequester } : {}),
    }),
  /** reason is sent only for the actions that require one (block, cancel, reopen). */
  transition: (id: string, action: TaskAction, expectedVersion: number, reason?: string) =>
    api.post<Task>(
      `/tasks/${enc(id)}/${action}`,
      reason === undefined ? { expectedVersion } : { expectedVersion, reason },
    ),
};
