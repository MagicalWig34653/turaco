// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export const ticketStatuses = [
  'new',
  'open',
  'in_progress',
  'waiting',
  'resolved',
  'closed',
  'cancelled',
] as const;
export type TicketStatus = (typeof ticketStatuses)[number];

export const priorities = ['low', 'normal', 'high', 'urgent'] as const;
export type Priority = (typeof priorities)[number];

export const waitingReasons = [
  'customer',
  'vendor',
  'external_service',
  'scheduled_change',
  'hardware',
] as const;

export type Ticket = {
  id: string;
  reference: string;
  title: string;
  description: string | null;
  status: TicketStatus;
  waitingReason: (typeof waitingReasons)[number] | null;
  statusReason: string | null;
  resolution: string | null;
  priority: Priority;
  reporterId: string;
  affectedUserId: string;
  queueTeamId: string | null;
  assigneeId: string | null;
  assetId: string | null;
  deviceSnapshot: Record<string, unknown> | null;
  resolvedAt: string | null;
  closedAt: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type TicketComment = {
  id: string;
  authorId: string;
  body: string;
  internal: boolean;
  createdAt: string;
};

export type TicketDetail = Ticket & {
  comments: TicketComment[];
  allowedOperations: string[];
  names: Record<string, string>;
};

export type TicketOperation =
  'start' | 'wait' | 'resume' | 'resolve' | 'close' | 'reopen' | 'cancel';
