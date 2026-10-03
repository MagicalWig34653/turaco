export const platforms = ['windows', 'macos', 'ios', 'android', 'linux', 'other'] as const;
export const complianceStates = [
  'compliant',
  'noncompliant',
  'in_grace_period',
  'unknown',
] as const;
export const findingKinds = [
  'no_asset_match',
  'serial_conflict',
  'duplicate_device',
  'unmatched_software',
  'provider_reported_error',
] as const;
export const artifactKinds = [
  'application',
  'configuration_profile',
  'compliance_policy',
  'endpoint_security_policy',
  'script',
  'remediation',
] as const;
export const observationStates = [
  'applied',
  'pending',
  'failed',
  'conflict',
  'not_applicable',
  'unknown',
] as const;
export type ArtifactFilters = {
  kind: string;
  q: string;
  platform: string;
  includeDeleted: boolean;
};
export type ManagementFilterFilters = { q: string; platform: string; includeDeleted: boolean };
export type ManagementArtifact = {
  id: string;
  provider: string;
  externalId: string;
  kind: string;
  name: string;
  platform: string;
  softwareProductId: string | null;
  revision: string | null;
  source: string;
  observedAt: string;
  lastSyncedAt: string;
  deletedObservedAt: string | null;
  version: number;
};
export type ManagementFilter = {
  id: string;
  provider: string;
  externalId: string;
  name: string;
  platform: string;
  rule: string;
  revision: string | null;
  source: string;
  observedAt: string;
  lastSyncedAt: string;
  deletedObservedAt: string | null;
};
export type FilterSummary = {
  id: string;
  name: string;
  platform: string;
  rule: string;
  deleted: boolean;
};
export type ManagementAssignment = {
  id: string;
  providerAssignmentId: string;
  targetKind: string;
  targetGroupExternalId: string | null;
  mode: string;
  intent: string;
  filterMode: string;
  filter: FilterSummary | null;
  current: boolean;
  source: string;
  observedAt: string;
  lastSyncedAt: string;
  validFrom: string;
  validUntil: string | null;
};
export type ManagementArtifactDetail = ManagementArtifact & {
  assignments: ManagementAssignment[];
  observationCounts: Record<string, number>;
};
export type ManagementObservation = {
  id: string;
  artifactId: string;
  artifactName: string;
  artifactKind: string;
  artifactDeleted: boolean;
  normalizedState: string;
  rawStatus: string;
  source: string;
  observedAt: string;
  lastSyncedAt: string;
};
export const reasonCodes = [
  'serial_confirmed',
  'correction',
  'duplicate',
  'wrong_asset',
  'other',
] as const;
export type ReasonCode = (typeof reasonCodes)[number];

export type Device = {
  id: string;
  provider: string;
  externalId: string;
  name: string;
  serialNumber: string | null;
  assetId: string | null;
  assetLinkSource: string | null;
  autoLinkBlocked: boolean;
  osPlatform: string;
  osVersion: string | null;
  manufacturer: string | null;
  model: string | null;
  ownership: string;
  complianceState: string;
  lastCheckinAt: string | null;
  source: string;
  observedAt: string;
  lastSyncedAt: string;
  deletedObservedAt: string | null;
  version: number;
};
export type Installation = {
  id: string;
  softwareProductId: string | null;
  productName: string | null;
  rawName: string;
  rawVersion: string;
  rawPublisher: string | null;
  observedAt: string;
};
export type Finding = {
  id: string;
  kind: string;
  deviceId: string;
  deviceName: string;
  status: string;
  detail: Record<string, unknown>;
  raisedAt: string;
  resolvedAt: string | null;
};
export type DeviceDetail = Device & { software: Installation[]; findings: Finding[] };
export type DeviceFilters = {
  platform: string;
  compliance: string;
  q: string;
  linked: string;
  includeDeleted: boolean;
};
export type FindingFilters = { kind: string; status: string; deviceId: string };
export type SyncCounts = {
  devicesCreated: number;
  devicesUpdated: number;
  devicesUnchanged: number;
  devicesTombstoned: number;
  devicesRejected: number;
  devicesLinked: number;
  softwareObserved: number;
  softwareSkipped: number;
  softwareErrors: number;
  findingsRaised: number;
  findingsResolved: number;
  filtersCreated: number;
  filtersUpdated: number;
  filtersUnchanged: number;
  filtersTombstoned: number;
  filtersRejected: number;
  artifactsCreated: number;
  artifactsUpdated: number;
  artifactsUnchanged: number;
  artifactsTombstoned: number;
  artifactsRejected: number;
  artifactsLinked: number;
  assignmentsOpened: number;
  assignmentsClosed: number;
  assignmentsUnchanged: number;
  assignmentsRejected: number;
  observationsCreated: number;
  observationsChanged: number;
  observationsUnchanged: number;
  observationsSkipped: number;
  membershipsOpened: number;
  membershipsClosed: number;
  membershipsUnchanged: number;
  membershipsSkipped: number;
  managementTombstonesSkipped: number;
  providerFindingsRaised: number;
  providerFindingsResolved: number;
  managementErrors: number;
};

export type GroupRef = { externalId: string | null; name: string | null; redacted: boolean };
export type UserRef = { id: string | null; name: string | null; redacted: boolean };
export type Expected = {
  result: string;
  confidence: string;
  reasons: string[];
  evaluatedAt: string;
};
export type Observed = {
  state: string;
  rawStatus: string;
  source: string;
  observedAt: string;
  lastSyncedAt: string;
  stale: boolean;
};
export type AssignedTarget = {
  assignmentId: string;
  targetKind: string;
  group: GroupRef | null;
  mode: string;
  intent: string;
  filterMode: string;
  filter: FilterSummary | null;
  match: string;
  origins: string[];
  nested: boolean;
  source: string;
  lastSyncedAt: string;
};
export type DeviceManagementItem = {
  artifact: ManagementArtifact;
  assigned: boolean;
  assignments: AssignedTarget[];
  expected: Expected;
  observed: Observed | null;
  mismatch?: string;
};
export type DeviceManagementPage = {
  items: DeviceManagementItem[];
  nextCursor?: string;
  truncated: boolean;
};
export type PathStep = {
  kind: string;
  origin: string;
  group: GroupRef | null;
  assignmentId?: string;
  mode?: string;
  intent?: string;
  targetKind?: string;
  filterResult?: string;
  result: string;
};
export type AssignmentPath = {
  deviceId: string;
  deviceName: string;
  artifact: ManagementArtifact;
  user: UserRef | null;
  expected: Expected;
  path: PathStep[];
  assignments: AssignedTarget[];
  observed: Observed | null;
};
export type Evaluation = {
  shown: boolean;
  evaluated: number;
  truncated: boolean;
  expected: Record<string, number>;
  observed: Record<string, number>;
  examples: {
    deviceId: string;
    name: string;
    result: string;
    confidence: string;
    observed: string;
  }[];
};
export type GroupManagement = {
  groupId: string;
  externalId: string;
  name: string;
  items: { artifact: ManagementArtifact; assignments: AssignedTarget[]; evaluation: Evaluation }[];
  nextCursor?: string;
  candidateDevices: number;
  candidatesTruncated: boolean;
};
export type UserManagement = {
  userId: string;
  name: string;
  items: {
    artifact: ManagementArtifact;
    targeting: AssignedTarget[];
    userResult: string;
    devices: { deviceId: string; name: string; expected: Expected; observed: Observed | null }[];
  }[];
  nextCursor?: string;
  devicesShown: boolean;
  devicesTruncated: boolean;
};
export type ArtifactTargets = {
  artifact: ManagementArtifact;
  assignments: AssignedTarget[];
  evaluation: Evaluation;
  observedTotal: Record<string, number>;
};
export type DeviceManagementFilters = { kind: string; state: string; mismatch: string };
