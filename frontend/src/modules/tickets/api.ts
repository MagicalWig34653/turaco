import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type { Ticket, TicketComment, TicketDetail, TicketOperation, TicketStatus } from './types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

registerErrorMessages({
  'tickets.version_conflict': 'error.versionConflict',
  'tickets.invalid_transition': 'error.ticketsInvalidTransition',
  'tickets.user_invalid': 'error.ticketsUserInvalid',
  'tickets.team_invalid': 'error.ticketsTeamInvalid',
  'tickets.device_invalid': 'error.ticketsDeviceInvalid',
  'tickets.not_found': 'error.notFound',
});
registerErrorResolver((error) =>
  error.code.startsWith('tickets.invalid_') ? 'error.invalidRequest' : undefined,
);

export const ticketsApi = {
  list: (
    scope: 'mine' | 'all',
    filter: { status?: TicketStatus | ''; open?: boolean },
    cursor?: string,
    signal?: Signal,
  ) =>
    api.get<Page<Ticket>>('/tickets', { signal, query: { scope, ...filter, limit: 50, cursor } }),
  get: (id: string, signal?: Signal) => api.get<TicketDetail>(`/tickets/${enc(id)}`, { signal }),
  create: (body: { title: string; description?: string; assetId?: string }) =>
    api.post<Ticket>('/tickets', body),
  comment: (id: string, body: string, internal: boolean) =>
    api.post<TicketComment>(`/tickets/${enc(id)}/comments`, { body, internal }),
  assign: (id: string, expectedVersion: number, body: { assigneeId?: string }) =>
    api.post<Ticket>(`/tickets/${enc(id)}/assign`, { expectedVersion, ...body }),
  setPriority: (id: string, expectedVersion: number, priority: string) =>
    api.post<Ticket>(`/tickets/${enc(id)}/priority`, { expectedVersion, priority }),
  operate: (id: string, op: TicketOperation, expectedVersion: number, reason = '') =>
    api.post<Ticket>(`/tickets/${enc(id)}/${op}`, { expectedVersion, reason }),
};
