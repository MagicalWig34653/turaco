// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export type RequestStatus =
  'pending_approval' | 'in_fulfillment' | 'waiting' | 'completed' | 'rejected' | 'cancelled';

export const requestStatuses: readonly RequestStatus[] = [
  'pending_approval',
  'in_fulfillment',
  'waiting',
  'completed',
  'rejected',
  'cancelled',
];

export const waitingReasons = ['stock', 'supplier', 'requester', 'external_system'] as const;
export type WaitingReason = (typeof waitingReasons)[number];

export type ServiceRequest = {
  id: string;
  reference: string;
  catalogItemId: string;
  catalogItemTitle: string;
  requesterId: string;
  requestedForId: string;
  status: RequestStatus;
  waitingReason: WaitingReason | null;
  statusReason: string | null;
  currentApprovalStep: number | null;
  submittedAt: string;
  completedAt: string | null;
  version: number;
  updatedAt: string;
};

export type RequestAction = 'cancel' | 'put_on_hold' | 'resume' | 'complete';

export type RequestApproval = {
  id: string;
  stepIndex: number;
  status: 'pending' | 'approved' | 'rejected' | 'cancelled';
  approverUserId: string | null;
  approverTeamId: string | null;
  decidedByUserId: string | null;
  decidedAt: string | null;
  decisionComment: string | null;
};

export type RequestTaskView = {
  id: string;
  title: string;
  status: string;
  priority: string;
  mandatory: boolean;
  dueAt: string | null;
  assignedUserId: string | null;
  assignedTeamId: string | null;
  /** The result note, only when the assignee released it for the requester. */
  resultNote?: string | null;
};

export type RequestField = {
  key: string;
  type: string;
  label: string;
  options?: Array<{ value: string; label: string }>;
};

export type ServiceRequestDetail = ServiceRequest & {
  allowRequestedFor: boolean;
  fields: RequestField[];
  answers: Record<string, unknown>;
  references: Array<{ fieldKey: string; type: 'user' | 'product'; id: string }>;
  approvals: RequestApproval[];
  tasks: RequestTaskView[];
  names: Record<string, string>;
  actions: RequestAction[];
};

export type SubmitBody = {
  catalogItemId: string;
  requestedForId?: string;
  answers: Record<string, unknown>;
};
