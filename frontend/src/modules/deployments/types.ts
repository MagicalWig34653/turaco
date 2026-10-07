// Types mirror the /target-sets and /deployments endpoints in api/openapi/openapi.yaml (F9 G2).
// Responses may gain fields; screens read only what they need and ignore the rest.

export const osPlatforms = ['windows', 'macos', 'ios', 'android', 'linux', 'other'] as const;
export type OsPlatform = (typeof osPlatforms)[number];
export const ownerships = ['corporate', 'personal', 'unknown'] as const;
export type Ownership = (typeof ownerships)[number];
export const complianceStates = [
  'compliant',
  'noncompliant',
  'in_grace_period',
  'unknown',
] as const;
export type ComplianceState = (typeof complianceStates)[number];

export type TargetGroup = { externalId: string; includeNested: boolean };

/** The strict, closed definition structure; unknown fields are refused by the API. */
export type TargetFilters = {
  platform?: string[];
  osVersionPrefix?: string;
  ownership?: string[];
  compliance?: string[];
  manufacturer?: string[];
  model?: string[];
  groups?: TargetGroup[];
  assetLocationIds?: string[];
};

export type TargetDefinition = {
  filters: TargetFilters;
  includeDeviceIds: string[];
  excludeDeviceIds: string[];
};

export type TargetSet = {
  id: string;
  reference: string;
  name: string;
  description: string | null;
  ownerUserId: string;
  definition: TargetDefinition;
  allDevices: boolean;
  /** all_devices or nested_root_group when plans using the set are high impact. */
  highImpactReason?: string | null;
  includeDeviceCount?: number;
  excludeDeviceCount?: number;
  /** Without endpoints.view the explicit lists come back empty; the counts remain. */
  deviceListsRedacted?: boolean;
  updatedBy?: string | null;
  archivedAt: string | null;
  createdBy: string;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type TargetSetInput = {
  name: string;
  description: string;
  ownerUserId?: string;
  definition: TargetDefinition;
};

export type TargetExample = {
  deviceId: string;
  name: string | null;
  redacted: boolean;
  osPlatform: string;
  complianceState: string;
};

export type TargetEvaluation = {
  matched: number;
  scanned: number;
  truncated: boolean;
  incomplete: boolean;
  cap: number;
  /** Breakdowns and examples may be left out for callers who only see counts. */
  byPlatform?: Record<string, number>;
  byCompliance?: Record<string, number>;
  evaluatedAt: string;
  examples?: TargetExample[];
  /** Examples are withheld (no endpoints.view, or an approver-only read). */
  examplesRedacted?: boolean;
};

export const clauseNames = [
  'platform',
  'os_version_prefix',
  'ownership',
  'compliance',
  'manufacturer',
  'model',
  'group',
  'asset_location_id',
  'include',
  'exclude',
  'all_devices',
] as const;
export const clauseReasons = [
  'value_differs',
  'value_missing',
  'not_listed',
  'not_member',
  'no_asset_link',
  'no_location',
] as const;

export type ClauseResult = { clause: string; result: 'matched' | 'failed'; reason: string | null };

export type TargetExplanation = {
  targetSetId: string;
  deviceId: string;
  deviceName: string | null;
  redacted: boolean;
  matched: boolean;
  incomplete: boolean;
  evaluatedAt: string;
  clauses: ClauseResult[];
};

export const deploymentStatuses = [
  'draft',
  'pending_approval',
  'approved',
  'scheduled',
  'cancelled',
  'resolving_targets',
  'ready',
  'running',
  'paused',
  'completed',
  'completed_with_errors',
  'failed',
] as const;
export type DeploymentStatus = (typeof deploymentStatuses)[number];

export const deploymentIntents = ['install', 'update', 'uninstall'] as const;
export type DeploymentIntent = (typeof deploymentIntents)[number];

export const cancelReasons = [
  'superseded',
  'no_longer_needed',
  'plan_error',
  'security_risk',
  'other',
] as const;

export type Deployment = {
  id: string;
  reference: string;
  name: string;
  softwareVersionId: string;
  productId: string;
  productName: string;
  productVersion: string;
  intent: DeploymentIntent;
  supersede: boolean;
  /** Follow-up Tasks for failure clusters and halts (F9 G4). */
  createTasks?: boolean;
  status: DeploymentStatus;
  statusReason: string | null;
  ownerUserId: string;
  createdBy: string;
  editors: string[];
  highImpact: boolean;
  approvalId: string | null;
  submittedBy: string | null;
  submittedAt: string | null;
  planSha256: string | null;
  approvedAt: string | null;
  scheduledBy: string | null;
  scheduledAt: string | null;
  cancelledBy: string | null;
  cancelledAt: string | null;
  /** Number of rings when a list response carries it. */
  ringCount?: number;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type DeploymentRing = {
  id: string;
  /** Set when the ring's Target Set or targets could not be fully resolved. */
  incomplete?: boolean;
  position: number;
  name: string;
  targetSetId: string;
  targetSetReference: string;
  targetSetName: string;
  approvalRequired: boolean;
  successThresholdPercent: number;
  minFreshEvidencePercent: number | null;
  soakMinutes: number;
  changeId: string | null;
  noWindowRequired: boolean;
  maxTargets: number;
};

export type RingInput = {
  name: string;
  targetSetId: string;
  approvalRequired: boolean;
  successThresholdPercent: number;
  minFreshEvidencePercent: number | null;
  soakMinutes: number;
  changeId: string | null;
  noWindowRequired: boolean;
  maxTargets: number | null;
};

export const issueCodes = [
  'version_not_approved',
  'product_not_approved',
  'package_gate_closed',
  'package_not_published',
  'no_rings',
  'target_set_archived',
  'window_required',
  'change_window_invalid',
  'no_window_high_impact',
  'approval_required',
  'too_many_targets',
  'no_targets',
  'evaluation_incomplete',
  'overlapping_deployment',
  'overlap_check_truncated',
  'high_impact',
] as const;

export type PlanIssue = {
  code: string;
  blocking: boolean;
  ringId: string | null;
  count: number | null;
  deploymentId: string | null;
};

export type RingTargets = {
  ringId: string;
  matched: number;
  truncated: boolean;
  incomplete: boolean;
};

export type PlanValidation = {
  valid: boolean;
  highImpact: boolean;
  /** uninstall, supersede, all_devices, nested_root_group or target_count. */
  highImpactReason?: string | null;
  incomplete?: boolean;
  totalTargets?: number | null;
  evaluated: boolean;
  validatedAt: string;
  issues: PlanIssue[];
  rings: RingTargets[];
};

export type DeploymentTransition = {
  fromStatus: DeploymentStatus | null;
  toStatus: DeploymentStatus;
  operation: string;
  reason: string | null;
  planSha256: string | null;
  actorUserId: string | null;
  actorSystem: string | null;
  createdAt: string;
};

export type DeploymentApprovalState = {
  id: string;
  status: string;
  approverUserId: string | null;
  approverTeamId: string | null;
  decidedByUserId: string | null;
  decidedAt: string | null;
};

export type ChangeWindow = {
  id: string;
  /** May be absent or a placeholder when the caller may not see the Change. */
  reference?: string | null;
  hidden?: boolean;
  status: string | null;
  windowStart: string | null;
  windowEnd: string | null;
};

export type DeploymentDetail = Deployment & {
  rings: DeploymentRing[];
  transitions: DeploymentTransition[];
  approvals: DeploymentApprovalState[];
  changes: Record<string, ChangeWindow>;
  validation: PlanValidation;
};

export type DeploymentInput = {
  name: string;
  softwareVersionId: string;
  intent: DeploymentIntent;
  supersede: boolean;
  createTasks?: boolean;
  ownerUserId?: string;
};

export type DeploymentFilter = { status: string; productId: string; versionId: string };

export const haltReasons = ['manual_halt', 'quality_issue', 'security_risk', 'other'] as const;

export const targetStates = [
  'pending',
  'assignment_requested',
  'awaiting_observation',
  'successful',
  'failed',
  'expired',
  'already_satisfied',
  'not_applicable',
  'cancelled',
] as const;

export const ringStatuses = [
  'pending',
  'active',
  'awaiting_promotion',
  'promoted',
  'halted',
] as const;

export type RingProgress = {
  ringId: string;
  ringRunId: string;
  position: number;
  name: string;
  status: string;
  statusReason: string | null;
  activatedAt: string | null;
  settledAt: string | null;
  awaitingSince: string | null;
  promotedAt: string | null;
  haltedAt: string | null;
  promotionApprovalStatus: string | null;
  approvalRequired: boolean;
  successThresholdPercent: number;
  soakMinutes: number;
  counts: Record<string, number>;
  freshSuccessful: number;
  freshObserved: number;
  successRatePercent: number | null;
  soakRemainingSeconds: number;
  /** previous_ring, resume, none, observations, soak, threshold, fresh_evidence, completion, approval, promotion. */
  nextGate: string;
  clearPending?: boolean;
  clearFailed?: boolean;
  retryCount?: number;
};

export type DeploymentProgress = {
  deployment: Deployment;
  rings: RingProgress[];
  clearPending?: boolean;
  /** resolving_targets for more than 15 minutes. */
  resolvingStuck?: boolean;
};

export type DeploymentTarget = {
  id: string;
  deviceId: string;
  /** Null without endpoints.view. */
  deviceName: string | null;
  state: string;
  stateReason: string | null;
  resolvedAt: string;
  assignmentRequestedAt: string | null;
  readBackAt: string | null;
  expiresAt: string | null;
  decidedAt: string | null;
  evidenceObservedAt: string | null;
};

export type DeploymentAttempt = {
  id: string;
  ringRunId: string;
  kind: string;
  attempt: number;
  operationId: string;
  requestedAt: string;
  outcomeCode: string;
  finishedAt?: string | null;
};

// Reporting (F9 G4): mirrors GET /deployments/{id}/report, /security-context and /software/rollouts.

export type ReportGate = { gate: string; passed: boolean; at: string; reason?: string | null };

export type RingReport = {
  ringId: string;
  ringRunId: string;
  position: number;
  name: string;
  status: string;
  statusReason?: string | null;
  startedAt?: string | null;
  endedAt?: string | null;
  successThresholdPercent: number;
  counts: Record<string, number>;
  successRatePercent?: number | null;
  medianSecondsToSuccess?: number | null;
  gates: ReportGate[];
};

export type FailureCluster = {
  findingId: string;
  dimension: string;
  value: string;
  failed: number;
  total: number;
  raisedAt: string;
};

export type DeploymentReport = {
  deployment: Deployment;
  generatedAt: string;
  totals: Record<string, number>;
  successRatePercent?: number | null;
  medianSecondsToSuccess?: number | null;
  rings: RingReport[];
  topFailureReasons: { code: string; count: number }[];
  clusters: FailureCluster[];
  people: {
    ownerUserId?: string | null;
    createdBy?: string | null;
    submittedBy?: string | null;
    scheduledBy?: string | null;
    startedBy?: string | null;
    cancelledBy?: string | null;
    approvedAt?: string | null;
    promotions?: { ringId: string; by?: string | null; at: string }[];
  };
  followUps: { reason: string; ringId?: string | null; taskId: string; createdAt: string }[];
};

export type SecurityContextAdvisory = {
  id: string;
  reference: string;
  title: string;
  severity: string;
  status: string;
  knownExploited: boolean;
  openFindings: number;
  affectedDevices: number;
};

export type DeploymentSecurityContext = {
  deploymentId: string;
  productName: string;
  productVersion: string;
  detailed: boolean;
  advisories?: SecurityContextAdvisory[] | null;
  advisoryCount: number;
  openFindingCount: number;
  targetDevices: number;
  truncated: boolean;
};

export type Rollout = {
  deployment: Deployment;
  ringCount: number;
  currentPosition?: number | null;
  currentRingName?: string | null;
  currentRingStatus?: string | null;
  awaitingPromotion: boolean;
  haltedRings: number;
  counts: Record<string, number>;
  targetTotal: number;
  decided: number;
  successRatePercent?: number | null;
};
