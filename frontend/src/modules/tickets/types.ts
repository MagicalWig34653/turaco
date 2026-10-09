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

export type TicketQueueRef = { id: string; key: string; prefix: string; name: string };

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
  /** The routing Team hint inside the desk; only for people who can view the Ticket's Queue. */
  queueTeamId: string | null;
  /** The Ticket's Queue; present only when the caller may know the Queue. */
  queueId?: string;
  queue?: TicketQueueRef | null;
  /** Neutral desk label returned instead of `queue` when the Queue is not known to the caller. */
  queueLabel?: string;
  /** Earlier references of a moved Ticket, newest first (detail response only). */
  aliases?: string[];
  assigneeId: string | null;
  assetId: string | null;
  majorIncidentId: string | null;
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

export interface TicketExternalSync {
  enabled: boolean;
  syncState: 'pending' | 'synced' | 'failed' | null;
  externalId: string | null;
  lastError: string | null;
  lastSyncedAt: string | null;
  externalUpdatedAt: string | null;
  attempts: number;
}

export const queueLevels = ['create', 'view', 'work', 'manage'] as const;
export type QueueLevel = (typeof queueLevels)[number];
export type QueueSubjectType = 'user' | 'team' | 'role';
export type QueueVisibility = 'internal' | 'public';
export type QueueRoutingMode = 'employee_choice' | 'automatic' | 'both';
export const moveReasonCodes = ['misrouted', 'different_skill', 'reorganization', 'other'] as const;
export type MoveReasonCode = (typeof moveReasonCodes)[number];

export type TicketQueueGrant = {
  subjectType: QueueSubjectType;
  subjectId: string;
  level: QueueLevel;
  grantedBy?: string;
  grantedAt?: string;
};

export type TicketQueue = {
  id: string;
  key: string;
  prefix: string;
  name: string;
  description: string;
  publicLabel?: string;
  status: 'active' | 'archived';
  visibility: QueueVisibility;
  routingMode?: QueueRoutingMode;
  defaultPriority?: Priority;
  defaultTeamId?: string;
  isDefault: boolean;
  version: number;
  /** The caller's effective level ('' when none). */
  level: '' | QueueLevel;
  canCreate: boolean;
  grants?: TicketQueueGrant[];
  archivedAt?: string;
};
