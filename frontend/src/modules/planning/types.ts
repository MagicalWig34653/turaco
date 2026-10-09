export const statuses = [
  'idea',
  'planning',
  'proposed',
  'approved',
  'active',
  'on_hold',
  'completed',
  'cancelled',
] as const;
export const itemTypes = ['change', 'task', 'procurement_request', 'service'] as const;
export const holdReasons = [
  'blocked_dependency',
  'resource_shortage',
  'budget',
  'reprioritized',
  'other',
] as const;
export const cancelReasons = [
  'no_longer_needed',
  'superseded',
  'budget',
  'reprioritized',
  'error_correction',
  'other',
] as const;
export const replanReasons = [
  'scope_change',
  'priority_change',
  'resource_change',
  'error_correction',
  'other',
] as const;
export const removeReasons = ['no_longer_needed', 'merged', 'error_correction', 'other'] as const;
export type InitiativeStatus = (typeof statuses)[number];
export type ItemType = (typeof itemTypes)[number];
export type Initiative = {
  id: string;
  reference: string;
  title: string;
  goal: string | null;
  ownerId: string;
  status: InitiativeStatus;
  statusReason: string | null;
  targetDate: string | null;
  approvalId: string | null;
  proposedBy: string | null;
  createdBy: string;
  approvedAt: string | null;
  activatedAt: string | null;
  closedAt: string | null;
  allowedOperations?: string[];
  version: number;
  createdAt: string;
  updatedAt: string;
};
export type Names = { users: Record<string, string> };
export type InitiativeList = { items: Initiative[]; nextCursor?: string; names: Names };
export type Item = {
  relationshipId: string;
  type: ItemType;
  id: string;
  reference: string | null;
  title: string | null;
  status: string | null;
  since: string;
  missing?: boolean;
  hidden?: boolean;
};
export type ItemPage = { items: Item[]; nextCursor?: string; truncated: boolean };
export type Milestone = {
  id: string;
  title: string;
  dueDate: string;
  position: number;
  doneAt: string | null;
  doneBy: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
};
export type Approval = {
  id: string;
  stepIndex: number;
  status: string;
  approverUserId: string | null;
  approverTeamId: string | null;
  decidedByUserId: string | null;
  decidedAt: string | null;
};
export type Progress = {
  items: number;
  itemsLimitExceeded: boolean;
  changesByStatus: Record<string, number> | null;
  tasksByStatus: Record<string, number> | null;
  procurementRequestsByStatus: Record<string, number> | null;
  milestones: { done: number; total: number; overdue: number };
};
export type InitiativeDetail = Initiative & {
  items: ItemPage;
  milestones: Milestone[];
  approvals: Approval[];
  progress: Progress;
  names: Names;
};
export type Transition = {
  id: string;
  fromStatus: string | null;
  toStatus: string;
  operation: string;
  reason: string | null;
  actorUserId: string | null;
  actorSystem: string | null;
  correlationId: string;
  createdAt: string;
};
export type TransitionList = { items: Transition[]; nextCursor?: string; names: Names };
export type CalendarAffected = {
  type: string;
  id: string;
  name: string | null;
  reference: string | null;
  missing?: boolean;
  hidden?: boolean;
  criticality?: string | null;
};
export type CalendarEntry = {
  changeId: string;
  reference: string;
  title: string | null;
  kind: string | null;
  risk: string | null;
  status: string;
  /** True while the Change is submitted but not approved: the window may still change. */
  proposed?: boolean;
  windowStart: string;
  windowEnd: string;
  affected: CalendarAffected[];
  initiatives: { id: string; reference: string; title: string }[];
};
export type Calendar = { from: string; to: string; items: CalendarEntry[]; truncated: boolean };
