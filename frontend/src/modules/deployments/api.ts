import { api } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import { normalizePage } from '../software/helpers';
import type {
  Deployment,
  DeploymentAttempt,
  DeploymentDetail,
  DeploymentFilter,
  DeploymentInput,
  DeploymentProgress,
  DeploymentReport,
  DeploymentSecurityContext,
  DeploymentRing,
  DeploymentTarget,
  PlanValidation,
  Rollout,
  RingInput,
  TargetEvaluation,
  TargetExplanation,
  TargetSet,
  TargetSetInput,
} from './types';

// Generic endpoints.* codes are registered by the endpoints and software modules; these are the planning ones.
registerErrorMessages({
  'endpoints.deployment_invalid': 'deployments.error.invalid',
  'endpoints.high_impact_required': 'deployments.error.highImpactRequired',
  'endpoints.no_eligible_approver': 'deployments.error.noEligibleApprover',
  'endpoints.target_set_in_use': 'deployments.error.targetSetInUse',
  'endpoints.target_set_name_taken': 'deployments.error.targetSetNameTaken',
  'endpoints.target_set_archived': 'deployments.error.targetSetArchived',
  'endpoints.change_window_invalid': 'deployments.error.changeWindowInvalid',
  'endpoints.approval_required': 'deployments.error.approvalRequired',
  'endpoints.approval_not_required': 'deployments.error.approvalNotRequired',
  'endpoints.plan_changed': 'deployments.error.planChanged',
  'endpoints.no_window_high_impact': 'deployments.error.noWindowHighImpact',
  'endpoints.evaluation_busy': 'deployments.error.evaluationBusy',
  'endpoints.editors_full': 'deployments.error.editorsFull',
  'endpoints.approver_not_authorized': 'deployments.error.approverNotAuthorized',
  'endpoints.deploy_write_disabled': 'deployments.error.deployWriteDisabled',
  'endpoints.separation_of_duties': 'deployments.error.separationOfDuties',
  'endpoints.change_window_closed': 'deployments.error.windowClosed',
  'endpoints.soak_not_elapsed': 'deployments.error.soakNotElapsed',
  'endpoints.threshold_not_met': 'deployments.error.thresholdNotMet',
  'endpoints.evidence_not_fresh': 'deployments.error.evidenceNotFresh',
  'endpoints.promotion_approval_required': 'deployments.error.promotionApprovalRequired',
  'endpoints.ring_not_awaiting_promotion': 'deployments.error.ringNotAwaiting',
  'endpoints.ring_not_halted': 'deployments.error.ringNotHalted',
  'endpoints.no_evidence': 'deployments.error.noEvidence',
  'endpoints.retry_required': 'deployments.error.retryRequired',
  'endpoints.retry_limit_reached': 'deployments.error.retryLimit',
  'endpoints.clear_pending': 'deployments.error.clearPending',
  'endpoints.assignment_cleared': 'deployments.error.assignmentCleared',
  'endpoints.no_next_ring': 'deployments.error.noNextRing',
});

const enc = encodeURIComponent;
const pageSize = 50;

type RingResult = { deployment: Deployment; ring: DeploymentRing };

export const targetSetsApi = {
  list: async (includeArchived: boolean, cursor?: string, signal?: AbortSignal) =>
    normalizePage(
      await api.get<Page<TargetSet>>('/target-sets', {
        query: { includeArchived, cursor, limit: pageSize },
        signal,
      }),
    ),
  get: (id: string, signal?: AbortSignal) =>
    api.get<TargetSet>(`/target-sets/${enc(id)}`, { signal }),
  create: (input: TargetSetInput) => api.post<TargetSet>('/target-sets', input),
  update: (id: string, input: TargetSetInput, expectedVersion: number) =>
    api.patch<TargetSet>(`/target-sets/${enc(id)}`, { ...input, expectedVersion }),
  archive: (id: string, expectedVersion: number) =>
    api.post<TargetSet>(`/target-sets/${enc(id)}/archive`, { expectedVersion }),
  evaluate: (id: string, signal?: AbortSignal) =>
    api.get<TargetEvaluation>(`/target-sets/${enc(id)}/evaluate`, { signal }),
  explain: (id: string, deviceId: string, signal?: AbortSignal) =>
    api.get<TargetExplanation>(`/target-sets/${enc(id)}/explain`, {
      query: { deviceId },
      signal,
    }),
};

export const deploymentsApi = {
  list: async (filter: DeploymentFilter, cursor?: string, signal?: AbortSignal) =>
    normalizePage(
      await api.get<Page<Deployment>>('/deployments', {
        query: {
          status: filter.status || undefined,
          productId: filter.productId || undefined,
          versionId: filter.versionId || undefined,
          cursor,
          limit: pageSize,
        },
        signal,
      }),
    ),
  get: (id: string, signal?: AbortSignal) =>
    api.get<DeploymentDetail>(`/deployments/${enc(id)}`, { signal }),
  create: (input: DeploymentInput) => api.post<Deployment>('/deployments', input),
  update: (id: string, input: DeploymentInput, expectedVersion: number) =>
    api.patch<Deployment>(`/deployments/${enc(id)}`, { ...input, expectedVersion }),
  addRing: (id: string, input: RingInput, expectedVersion: number) =>
    api.post<RingResult>(`/deployments/${enc(id)}/rings`, { ...input, expectedVersion }),
  updateRing: (id: string, ringId: string, input: RingInput, expectedVersion: number) =>
    api.patch<RingResult>(`/deployments/${enc(id)}/rings/${enc(ringId)}`, {
      ...input,
      expectedVersion,
    }),
  removeRing: (id: string, ringId: string, expectedVersion: number) =>
    api.delete<Deployment>(`/deployments/${enc(id)}/rings/${enc(ringId)}`, {
      query: { expectedVersion },
    }),
  reorderRings: (id: string, ringIds: string[], expectedVersion: number) =>
    api.post<Deployment>(`/deployments/${enc(id)}/rings/reorder`, { ringIds, expectedVersion }),
  validate: (id: string) => api.post<PlanValidation>(`/deployments/${enc(id)}/validate`, {}),
  submit: (id: string, approver: { type: 'user' | 'team'; id: string }, expectedVersion: number) =>
    api.post<Deployment>(`/deployments/${enc(id)}/submit`, {
      [approver.type === 'user' ? 'approverUserId' : 'approverTeamId']: approver.id,
      expectedVersion,
    }),
  schedule: (id: string, expectedVersion: number) =>
    api.post<Deployment>(`/deployments/${enc(id)}/schedule`, { expectedVersion }),
  cancel: (id: string, reason: string, expectedVersion: number) =>
    api.post<Deployment>(`/deployments/${enc(id)}/cancel`, { reason, expectedVersion }),
};

const op = (id: string, action: string, body: object) =>
  api.post<Deployment>(`/deployments/${enc(id)}/${action}`, body);

/** Execution operations (F9 G3); every one carries the Deployment's version. */
export const executionApi = {
  start: (id: string, expectedVersion: number) => op(id, 'start', { expectedVersion }),
  pause: (id: string, expectedVersion: number) => op(id, 'pause', { expectedVersion }),
  resume: (id: string, expectedVersion: number) => op(id, 'resume', { expectedVersion }),
  halt: (id: string, reason: string, expectedVersion: number) =>
    op(id, 'halt', { reason, expectedVersion }),
  haltRing: (id: string, ringId: string, reason: string, expectedVersion: number) =>
    op(id, `rings/${enc(ringId)}/halt`, { reason, expectedVersion }),
  resumeRing: (id: string, ringId: string, expectedVersion: number, retry = false) =>
    op(id, `rings/${enc(ringId)}/resume`, {
      expectedVersion,
      ...(retry ? { reason: 'retry' } : {}),
    }),
  promoteRing: (id: string, ringId: string, expectedVersion: number) =>
    op(id, `rings/${enc(ringId)}/promote`, { expectedVersion }),
  requestApproval: (
    id: string,
    ringId: string,
    approver: { type: 'user' | 'team'; id: string },
    expectedVersion: number,
  ) =>
    op(id, `rings/${enc(ringId)}/request-approval`, {
      [approver.type === 'user' ? 'approverUserId' : 'approverTeamId']: approver.id,
      expectedVersion,
    }),
  progress: (id: string, signal?: AbortSignal) =>
    api.get<DeploymentProgress>(`/deployments/${enc(id)}/progress`, { signal }),
  targets: async (
    id: string,
    ringId: string,
    state: string,
    cursor?: string,
    signal?: AbortSignal,
  ) =>
    normalizePage(
      await api.get<Page<DeploymentTarget> & { namesRedacted?: boolean }>(
        `/deployments/${enc(id)}/rings/${enc(ringId)}/targets`,
        { query: { state: state || undefined, cursor, limit: pageSize }, signal },
      ),
    ),
  attempts: async (id: string, cursor?: string, signal?: AbortSignal) =>
    normalizePage(
      await api.get<Page<DeploymentAttempt>>(`/deployments/${enc(id)}/attempts`, {
        query: { cursor, limit: pageSize },
        signal,
      }),
    ),
};

export const reportApi = {
  report: (id: string, signal?: AbortSignal) =>
    api.get<DeploymentReport>(`/deployments/${enc(id)}/report`, { signal }),
  securityContext: (id: string, signal?: AbortSignal) =>
    api.get<DeploymentSecurityContext>(`/deployments/${enc(id)}/security-context`, { signal }),
  /** The CSV is an attachment; the browser downloads it with the session cookie. */
  csvUrl: (id: string) => `/api/v1/deployments/${enc(id)}/report.csv`,
  rollouts: async (status: string, cursor?: string, signal?: AbortSignal) =>
    normalizePage(
      await api.get<Page<Rollout>>('/software/rollouts', {
        query: { status: status || undefined, cursor, limit: pageSize },
        signal,
      }),
    ),
};
