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

export const historyKinds = [
  'created',
  'assigned',
  'unassigned',
  'reassigned',
  'team_routed',
  'status_changed',
  'priority_changed',
  'queue_moved',
] as const;
export type TicketHistoryKind = (typeof historyKinds)[number];

/** One staff-visible change of a Ticket (derived from its audit events by the server). */
export type TicketHistoryEntry = {
  id: string;
  /** When the change happened. */
  at: string;
  kind: TicketHistoryKind | string;
  actorId?: string | null;
  /** The operation that caused the change (assign, start, queue_move, ...). */
  via?: string | null;
  reason?: string | null;
  fromUserId?: string | null;
  toUserId?: string | null;
  fromTeamId?: string | null;
  toTeamId?: string | null;
  fromStatus?: string | null;
  toStatus?: string | null;
  fromPriority?: string | null;
  toPriority?: string | null;
};

/** What the caller may do with this Ticket; decided by the server per Ticket. */
export type TicketAbilities = {
  comment: boolean;
  internalComment: boolean;
  assign: boolean;
  setPriority: boolean;
  transition: boolean;
  move: boolean;
  markDuplicate: boolean;
};

export type TicketDetail = Ticket & {
  /** Absent on servers that do not return abilities yet. */
  abilities?: TicketAbilities;
  comments: TicketComment[];
  /** Absent for requesters and on servers that do not return history yet. */
  history?: TicketHistoryEntry[];
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
