import { api } from '../../platform/api/client';

export type OverviewQueueRef = { id: string; key: string; prefix: string; name: string };

export type TicketOverviewItem = {
  id: string;
  reference: string;
  title: string;
  priority: string;
  status: string;
  queue: OverviewQueueRef;
  assigneeId: string | null;
  assigneeName?: string;
  createdAt: string;
  updatedAt: string;
};

/** GET /tickets/overview: open Tickets of the Queues the caller may view (docs: manager overview). */
export type TicketOverview = {
  generatedAt: string;
  totals: { open: number; unassigned: number; waiting: number };
  queues: Array<{
    queue: OverviewQueueRef;
    level: 'view' | 'work' | 'manage';
    open: number;
    unassigned: number;
    waiting: number;
    oldestOpenAt: string | null;
  }>;
  assignees: Array<{ userId: string; name: string; open: number; waiting: number }>;
  assigneesTruncated: boolean;
  oldestUnassigned: TicketOverviewItem[];
  oldestWaiting: TicketOverviewItem[];
};

export const ticketOverviewApi = {
  get: (signal?: AbortSignal) => api.get<TicketOverview>('/tickets/overview', { signal }),
};
