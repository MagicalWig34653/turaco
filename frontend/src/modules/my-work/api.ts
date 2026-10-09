import { api } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';

// Types mirror api/openapi/openapi.yaml (GET /my-work/items, GET /my-work/counts).

export const workSources = ['tickets', 'team_tickets', 'tasks'] as const;
export type WorkSource = (typeof workSources)[number];

export type WorkItem = {
  id: string;
  source: WorkSource;
  kind: 'task' | 'ticket';
  title: string;
  reference?: string;
  status: string;
  waitingReason?: string;
  priority: string;
  dueAt: string | null;
  updatedAt: string;
  /** In-app path that opens the item. */
  href: string;
};

export type WorkItemsPage = {
  items: WorkItem[];
  nextCursor?: string;
  /** Sources that failed for this page; their position stays in the cursor, so a retry delivers them. */
  unavailable: string[];
};

export type WorkCount = {
  source: string;
  count?: number;
  capped?: boolean;
  status: 'ok' | 'unavailable';
};

/** `total`, `totalCapped` and `complete` are absent on servers that predate them. */
export type WorkCounts = {
  items: WorkCount[];
  total?: number;
  totalCapped?: boolean;
  complete?: boolean;
};

registerErrorMessages({
  'mywork.invalid_request': 'error.invalidRequest',
  'mywork.invalid_cursor': 'error.invalidRequest',
});

type Signal = AbortSignal | undefined;

export const myWorkApi = {
  items: (
    query: {
      sources?: readonly WorkSource[] | undefined;
      cursor?: string | undefined;
      limit?: number;
    },
    signal?: Signal,
  ) =>
    api.get<WorkItemsPage>('/my-work/items', {
      signal,
      query: {
        sources: query.sources?.length ? query.sources.join(',') : undefined,
        cursor: query.cursor,
        limit: query.limit ?? 50,
      },
    }),
  counts: (sources?: readonly WorkSource[] | undefined, signal?: Signal) =>
    api.get<WorkCounts>('/my-work/counts', {
      signal,
      query: { sources: sources?.length ? sources.join(',') : undefined },
    }),
};
