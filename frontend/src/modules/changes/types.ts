export const statuses = [
  'draft',
  'assessment',
  'pending_approval',
  'approved',
  'rejected',
  'scheduled',
  'in_progress',
  'completed',
  'failed',
  'review',
  'closed',
  'cancelled',
] as const;
export const kinds = ['standard', 'normal', 'emergency'] as const;
export const risks = ['low', 'medium', 'high'] as const;
export const resourceTypes = ['service', 'vm', 'asset', 'location'] as const;
export const failReasons = [
  'execution_error',
  'verification_failed',
  'window_exceeded',
  'dependency_unavailable',
  'other',
] as const;
export const cancelReasons = [
  'no_longer_needed',
  'superseded',
  'rescheduled',
  'risk_too_high',
  'error_correction',
  'other',
] as const;
export type ChangeStatus = (typeof statuses)[number];
export type ChangeKind = (typeof kinds)[number];
export type ChangeRisk = (typeof risks)[number];
export type ResourceType = (typeof resourceTypes)[number];
export type Change = {
  id: string;
  reference: string;
  title: string;
  description: string | null;
  kind: ChangeKind;
  risk: ChangeRisk;
  status: ChangeStatus;
  statusReason: string | null;
  requesterId: string;
  ownerId: string | null;
  rollbackPlan: string | null;
  emergencyJustification: string | null;
  windowStart: string | null;
  windowEnd: string | null;
  outcomeNote: string | null;
  rollbackDone: boolean | null;
  approvalId: string | null;
  approvedWindowStart: string | null;
  approvedWindowEnd: string | null;
  emergencyApprovedBy: string | null;
  reviewRequired: boolean;
  startedAt: string | null;
  completedAt: string | null;
  closedAt: string | null;
  allowedOperations?: string[];
  version: number;
  createdAt: string;
  updatedAt: string;
};
export type Names = { users: Record<string, string> };
export type ChangeList = { items: Change[]; nextCursor?: string; names?: Names };
export type Affected = {
  relationshipId: string;
  type: ResourceType;
  id: string;
  name: string | null;
  reference: string | null;
  status: string | null;
  confidence: string;
  since: string;
  missing?: boolean;
  hidden?: boolean;
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
export type Task = { id: string; title: string; status: string; dueAt: string | null };
export type ChangeDetail = Change & {
  affected: Affected[];
  approvals: Approval[];
  tasks: { total: number; open: number; items: Task[] };
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
  createdAt: string;
};
export type TransitionList = { items: Transition[]; nextCursor?: string; names: Names };
export type ImpactNode = {
  type: ResourceType;
  id: string;
  name: string | null;
  reference: string | null;
  status: string | null;
  criticality: string | null;
  missing?: boolean;
  hidden?: boolean;
  depth: number;
  confidence: string;
};
export type ImpactStart = {
  type: ResourceType;
  id: string;
  name: string | null;
  reference: string | null;
  nodes: ImpactNode[];
  truncated: boolean;
  depthLimited: boolean;
  nodeLimited: boolean;
};
export type ChangeImpact = { starts: ImpactStart[]; skipped: number; truncated: boolean };
