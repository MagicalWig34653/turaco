import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type {
  MoveReasonCode,
  QueueLevel,
  QueueRoutingMode,
  QueueSubjectType,
  QueueVisibility,
  Priority,
  Ticket,
  TicketComment,
  TicketDetail,
  TicketExternalSync,
  TicketOperation,
  TicketQueue,
  TicketStatus,
} from './types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

registerErrorMessages({
  'tickets.version_conflict': 'error.versionConflict',
  'tickets.invalid_transition': 'error.ticketsInvalidTransition',
  'tickets.user_invalid': 'error.ticketsUserInvalid',
  'tickets.team_invalid': 'error.ticketsTeamInvalid',
  'tickets.device_invalid': 'error.ticketsDeviceInvalid',
  'tickets.not_found': 'error.notFound',
  'tickets.sync_disabled': 'error.ticketsSyncDisabled',
  'servicedesk.queue_not_found': 'error.queueNotFound',
  'servicedesk.queue_not_permitted': 'error.queueNotPermitted',
  'servicedesk.queue_prefix_taken': 'error.queuePrefixTaken',
  'servicedesk.queue_key_taken': 'error.queueKeyTaken',
  'servicedesk.queue_archived': 'error.queueArchived',
  'servicedesk.queue_same': 'error.queueSame',
  'servicedesk.queue_has_open_tickets': 'error.queueHasOpenTickets',
  'servicedesk.queue_is_default': 'error.queueIsDefault',
  'servicedesk.assignee_no_queue_access': 'error.assigneeNoQueueAccess',
});
registerErrorResolver((error) =>
  error.code.startsWith('tickets.invalid_') ? 'error.invalidRequest' : undefined,
);

export const ticketsApi = {
  list: (
    scope: 'mine' | 'all',
    filter: { status?: TicketStatus | ''; open?: boolean; queue?: string },
    cursor?: string,
    signal?: Signal,
  ) =>
    api.get<Page<Ticket>>('/tickets', { signal, query: { scope, ...filter, limit: 50, cursor } }),
  get: (id: string, signal?: Signal) => api.get<TicketDetail>(`/tickets/${enc(id)}`, { signal }),
  create: (body: {
    title: string;
    description?: string;
    assetId?: string;
    affectedUserId?: string;
    priority?: string;
    queueTeamId?: string;
    /** The Ticket Queue; omit for the intake Queue. */
    queueId?: string;
    queueKey?: string;
  }) => api.post<Ticket>('/tickets', body),
  moveToQueue: (
    id: string,
    body: { expectedVersion: number; targetQueueId: string; reasonCode: MoveReasonCode },
  ) => api.post<Ticket>(`/tickets/${enc(id)}/move-queue`, body),
  /** Resolves a current reference or an alias; 404 for anything the caller may not see. */
  byReference: (reference: string, signal?: Signal) =>
    api.get<{ ticketId: string; reference: string; alias: boolean }>('/tickets/by-reference', {
      signal,
      query: { reference },
    }),
  comment: (id: string, body: string, internal: boolean) =>
    api.post<TicketComment>(`/tickets/${enc(id)}/comments`, { body, internal }),
  assign: (
    id: string,
    expectedVersion: number,
    body: { assigneeId?: string; queueTeamId?: string },
  ) => api.post<Ticket>(`/tickets/${enc(id)}/assign`, { expectedVersion, ...body }),
  setPriority: (id: string, expectedVersion: number, priority: string) =>
    api.post<Ticket>(`/tickets/${enc(id)}/priority`, { expectedVersion, priority }),
  operate: (id: string, op: TicketOperation, expectedVersion: number, reason = '') =>
    api.post<Ticket>(`/tickets/${enc(id)}/${op}`, { expectedVersion, reason }),
  externalSync: (id: string, signal?: Signal) =>
    api.get<TicketExternalSync>(`/tickets/${enc(id)}/external-sync`, { signal }),
  retryExternalSync: (id: string) => api.post<void>(`/tickets/${enc(id)}/external-sync`, {}),
};

export type QueueCreateBody = {
  key: string;
  prefix: string;
  name: string;
  description?: string;
  publicLabel?: string;
  visibility?: QueueVisibility;
  routingMode?: QueueRoutingMode;
  defaultPriority?: Priority;
  defaultTeamId?: string;
  numberPadding?: number;
};
export type QueueUpdateBody = {
  expectedVersion: number;
  name?: string;
  description?: string;
  publicLabel?: string;
  visibility?: QueueVisibility;
  routingMode?: QueueRoutingMode;
  defaultPriority?: Priority;
  /** A Team id; an empty string clears it. */
  defaultTeamId?: string;
};
export type QueueGrantBody = {
  subjectType: QueueSubjectType;
  subjectId: string;
  level: QueueLevel;
};

export const ticketQueuesApi = {
  /** Without `forCreate`: the Queues the caller may view; with it: the Queues a Ticket can be raised into. */
  list: (forCreate = false, signal?: Signal) =>
    api.get<{ items: TicketQueue[] }>('/service-desk/queues', {
      signal,
      query: forCreate ? { for: 'create' } : {},
    }),
  get: (id: string, signal?: Signal) =>
    api.get<TicketQueue>(`/service-desk/queues/${enc(id)}`, { signal }),
  create: (body: QueueCreateBody) => api.post<TicketQueue>('/service-desk/queues', body),
  update: (id: string, body: QueueUpdateBody) =>
    api.patch<TicketQueue>(`/service-desk/queues/${enc(id)}`, body),
  archive: (id: string, expectedVersion: number) =>
    api.post<TicketQueue>(`/service-desk/queues/${enc(id)}/archive`, { expectedVersion }),
  restore: (id: string, expectedVersion: number) =>
    api.post<TicketQueue>(`/service-desk/queues/${enc(id)}/restore`, { expectedVersion }),
  makeDefault: (id: string, expectedVersion: number) =>
    api.post<TicketQueue>(`/service-desk/queues/${enc(id)}/make-default`, { expectedVersion }),
  replaceGrants: (id: string, expectedVersion: number, grants: QueueGrantBody[]) =>
    api.put<TicketQueue>(`/service-desk/queues/${enc(id)}/grants`, { expectedVersion, grants }),
};
