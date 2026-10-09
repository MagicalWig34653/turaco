import { api } from '../../../platform/api/client';
import { registerErrorMessages } from '../../../platform/api/errorMessages';
import type { Page } from '../../../platform/api/types';
import type {
  Anchor,
  BoardCreateBody,
  BoardUpdateBody,
  ColumnCards,
  ColumnOperation,
  MoveBody,
  MoveResult,
  TaskBoard,
} from './types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

registerErrorMessages({
  'tasks.board_not_found': 'tasks.board.error.notFound',
  'tasks.board_conflict': 'tasks.board.error.conflict',
  'tasks.board_archived': 'tasks.board.error.archived',
  'tasks.board_limit_reached': 'tasks.board.error.limitReached',
  'tasks.board_anchor_invalid': 'tasks.board.error.anchorInvalid',
});

export const boardsApi = {
  list: (cursor?: string, signal?: Signal) =>
    api.get<Page<TaskBoard>>('/tasks/boards', { signal, query: { limit: 100, cursor } }),
  get: (id: string, signal?: Signal) => api.get<TaskBoard>(`/tasks/boards/${enc(id)}`, { signal }),
  create: (body: BoardCreateBody) => api.post<TaskBoard>('/tasks/boards', body),
  update: (id: string, body: BoardUpdateBody) =>
    api.patch<TaskBoard>(`/tasks/boards/${enc(id)}`, body),
  columns: (id: string, expectedVersion: number, operation: ColumnOperation) =>
    api.post<TaskBoard>(`/tasks/boards/${enc(id)}/columns`, { expectedVersion, ...operation }),
  /** First page of every column; with `column` (and `cursor`) one page of that column. */
  cards: (id: string, query: { column?: string; cursor?: string } = {}, signal?: Signal) =>
    api.get<{ columns: ColumnCards[] }>(`/tasks/boards/${enc(id)}/cards`, {
      signal,
      query: { ...query, limit: 50 },
    }),
  /** expectedVersion is the task's version and mandatory; reason only where the move needs one. */
  move: (
    id: string,
    input: {
      taskId: string;
      columnId: string;
      expectedVersion: number;
      reason?: string | undefined;
      anchor?: Anchor | undefined;
    },
  ) => {
    const body: MoveBody = {
      taskId: input.taskId,
      columnId: input.columnId,
      expectedVersion: input.expectedVersion,
      ...(input.reason ? { reason: input.reason } : {}),
      ...(input.anchor && 'afterTaskId' in input.anchor
        ? { afterTaskId: input.anchor.afterTaskId }
        : {}),
      ...(input.anchor && 'top' in input.anchor ? { top: true } : {}),
    };
    return api.post<MoveResult>(`/tasks/boards/${enc(id)}/moves`, body);
  },
  place: (id: string, input: { taskId: string; columnId: string; anchor: Anchor }) =>
    api.put<{ taskId: string; columnId: string; rank: string }>(`/tasks/boards/${enc(id)}/ranks`, {
      taskId: input.taskId,
      columnId: input.columnId,
      ...('afterTaskId' in input.anchor
        ? { afterTaskId: input.anchor.afterTaskId }
        : { top: true }),
    }),
  archive: (id: string, expectedVersion: number) =>
    api.post<TaskBoard>(`/tasks/boards/${enc(id)}/archive`, { expectedVersion }),
  restore: (id: string, expectedVersion: number) =>
    api.post<TaskBoard>(`/tasks/boards/${enc(id)}/restore`, { expectedVersion }),
};
