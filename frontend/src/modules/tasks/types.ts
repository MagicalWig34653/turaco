// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export type TaskStatus = 'open' | 'in_progress' | 'blocked' | 'completed' | 'cancelled';
export type TaskPriority = 'low' | 'normal' | 'high' | 'urgent';

export const taskStatuses: readonly TaskStatus[] = [
  'open',
  'in_progress',
  'blocked',
  'completed',
  'cancelled',
];
export const taskPriorities: readonly TaskPriority[] = ['low', 'normal', 'high', 'urgent'];

export type Task = {
  id: string;
  title: string;
  description: string | null;
  status: TaskStatus;
  statusReason: string | null;
  priority: TaskPriority;
  assignedUserId: string | null;
  assignedUserName: string | null;
  assignedTeamId: string | null;
  assignedTeamName: string | null;
  contextType: string | null;
  contextId: string | null;
  dueAt: string | null;
  completedAt: string | null;
  createdByUserId: string | null;
  completedByUserId: string | null;
  /** The closing comment given when the task was completed; null otherwise. Absent on older servers. */
  resultNote?: string | null;
  /** True when the requester of the originating Service Request may read the result note. */
  resultNoteForRequester?: boolean;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type TaskFilter = {
  status?: TaskStatus;
  priority?: TaskPriority;
  overdue?: boolean;
  mine?: boolean;
  q?: string;
};

export type TaskCreateBody = {
  title: string;
  description?: string;
  priority?: TaskPriority;
  dueAt?: string | null;
};

export type TaskUpdateBody = {
  expectedVersion: number;
  title?: string;
  description?: string;
  priority?: TaskPriority;
  /** null clears the due date; undefined keeps it. */
  dueAt?: string | null;
};

export type TaskAssignBody = { expectedVersion: number; userId?: string; teamId?: string };

export type TaskAction = 'start' | 'block' | 'unblock' | 'complete' | 'cancel' | 'reopen';
