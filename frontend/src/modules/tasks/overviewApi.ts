import { api } from '../../platform/api/client';

export type TaskOverviewOwner = { id: string; name: string; open: number; overdue: number };

export type TaskOverviewItem = {
  id: string;
  title: string;
  status: string;
  priority: string;
  assignedUserId: string | null;
  assignedUserName: string | null;
  assignedTeamId: string | null;
  assignedTeamName: string | null;
  dueAt: string | null;
  createdAt: string;
};

/** GET /tasks/overview: open Tasks by owner for callers who see every Task (tasks.view or tasks.manage). */
export type TaskOverview = {
  generatedAt: string;
  totals: { open: number; overdue: number; blocked: number; unowned: number };
  users: TaskOverviewOwner[];
  usersTruncated: boolean;
  teams: TaskOverviewOwner[];
  teamsTruncated: boolean;
  oldestOverdue: TaskOverviewItem[];
  oldestUnowned: TaskOverviewItem[];
};

export const taskOverviewApi = {
  get: (signal?: AbortSignal) => api.get<TaskOverview>('/tasks/overview', { signal }),
};
