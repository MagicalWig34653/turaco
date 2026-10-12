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
  /** Only in `nextToBreach`: the nearest due time still ahead. */
  slaDueAt?: string;
};

/** GET /tickets/overview: open Tickets of the Queues the caller may view (docs: manager overview). */
export type TicketOverview = {
  generatedAt: string;
  /** True while the sla switch is on; only then the SLA figures below are present. */
  slaEnabled: boolean;
  totals: { open: number; unassigned: number; waiting: number; atRisk?: number; breached?: number };
  queues: Array<{
    queue: OverviewQueueRef;
    level: 'view' | 'work' | 'manage';
    open: number;
    unassigned: number;
    waiting: number;
    oldestOpenAt: string | null;
    atRisk?: number;
    breached?: number;
  }>;
  assignees: Array<{ userId: string; name: string; open: number; waiting: number }>;
  assigneesTruncated: boolean;
  oldestUnassigned: TicketOverviewItem[];
  oldestWaiting: TicketOverviewItem[];
  nextToBreach?: TicketOverviewItem[];
};

export const ticketOverviewApi = {
  get: (signal?: AbortSignal) => api.get<TicketOverview>('/tickets/overview', { signal }),
};
