export const advisoryStatuses = [
  'new',
  'analyzing',
  'applicable',
  'not_applicable',
  'remediation_planned',
  'remediating',
  'resolved',
  'archived',
] as const;
export const findingStatuses = [
  'open',
  'investigating',
  'accepted',
  'remediation_planned',
  'remediating',
  'remediated',
  'false_positive',
  'risk_accepted',
] as const;
export const severities = ['none', 'low', 'medium', 'high', 'critical'] as const;
export const confidences = ['probable', 'potential'] as const;
export const ruleKinds = ['introduced', 'fixed', 'lt', 'le', 'eq'] as const;
export const riskReasons = [
  'compensating_control',
  'low_exposure',
  'no_fix_available',
  'business_need',
  'other',
] as const;
export const falsePositiveReasons = [
  'version_misreported',
  'product_mismatch',
  'not_installed',
  'configuration_not_affected',
  'other',
] as const;
export const reopenReasons = [
  'review_due',
  'new_information',
  'error_correction',
  'other',
] as const;
export const notApplicableReasons = [
  'product_not_used',
  'version_not_used',
  'configuration_not_affected',
  'duplicate',
  'other',
] as const;
export type Rule = { kind: string; version: string };
export type Criterion = {
  id?: string;
  position?: number;
  softwareProductId: string | null;
  productName: string | null;
  publisher: string | null;
  osPlatform: string | null;
  normalization?: string;
  matchMethod?: string | null;
  rules: Rule[];
};
export type Advisory = {
  id: string;
  reference: string;
  source: string;
  externalId: string | null;
  title: string;
  summary: string | null;
  severity: string;
  sourceUrl: string | null;
  status: string;
  statusReason: string | null;
  version: number;
  publishedAt: string | null;
  modifiedAt: string | null;
  matchedAt: string | null;
  matchedIngestionAt: string | null;
  matchTruncated: boolean;
  unmatchedCriteria: number;
  criteriaIncomplete: boolean;
  criteriaSkipped: number;
  criteriaChangedUpstream: boolean;
  knownExploited: boolean;
  knownExploitedAddedAt: string | null;
  kevDueDate: string | null;
  criteriaRevision: number;
  matchedRevision: number | null;
  createdAt: string;
  updatedAt: string;
};
export type AdvisoryActionResponse = Advisory & { warnings: string[] };
export type AdvisoryDetail = {
  advisory: Advisory;
  criteria: Criterion[];
  productNames: Record<string, string>;
};
export type Finding = {
  id: string;
  reference: string;
  advisoryId: string;
  advisoryReference: string;
  advisoryTitle: string;
  deviceId: string;
  deviceHidden: boolean;
  deviceName: string | null;
  softwareProductId: string;
  productName: string | null;
  installedVersion: string | null;
  confidence: 'probable' | 'potential';
  status: string;
  statusReason: string | null;
  riskAcceptedBy: string | null;
  riskAcceptedAt: string | null;
  riskReviewBy: string | null;
  firstSeenAt: string;
  lastSeenAt: string;
  remediatedAt: string | null;
  version: number;
};
export type Page<T> = { items: T[]; nextCursor?: string };
export type Summary = {
  byStatus: Record<string, number>;
  byConfidence: Record<string, number>;
  affectedDevices: number;
  unmatchedCriteria: number;
  oldestOpenSince: string | null;
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
export type RemediationTask = {
  id: string;
  status: string;
  dueAt: string | null;
  assignedUserId?: string;
  assignedTeamId?: string;
  assigneeName?: string;
  assigneeHidden: boolean;
};
export type LinkedChange = {
  id: string;
  changeId?: string;
  reference?: string;
  status?: string;
  hidden: boolean;
};
export type Progress = {
  generatedAt: string;
  tasksTruncated: boolean;
  source: 'turaco_derived';
  findingByStatus: Record<string, number>;
  findingByConfidence: Record<string, number>;
  shareRemediated: number;
  acceptedRiskCount: number;
  earliestRiskReviewBy: string | null;
  oldestOpenFindingAgeDays: number | null;
  tasks: { open: number; done: number; cancelled: number; overdue: number };
  linkedChangesByStatus: Record<string, number>;
  hiddenLinkedChanges: number;
  residualRisk: 'none' | 'low' | 'medium' | 'high' | 'unknown';
};
export type Overview = {
  source: 'turaco_derived';
  applicableBySeverity: Record<string, number>;
  openFindingsByConfidence: Record<string, number>;
  overdueTasks: number;
  riskAcceptancesDueWithin30Days: number;
};
