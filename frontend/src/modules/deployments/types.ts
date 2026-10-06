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
  ownerUserId?: string;
};

export type DeploymentFilter = { status: string; productId: string; versionId: string };
