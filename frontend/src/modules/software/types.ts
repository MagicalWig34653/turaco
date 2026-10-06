// Types mirror the /software endpoints in api/openapi/openapi.yaml (F9 G1).

export const productStatuses = [
  'candidate',
  'approved',
  'deprecated',
  'retired',
  'blocked',
] as const;
export type ProductStatus = (typeof productStatuses)[number];

export const versionStatuses = [
  'registered',
  'pending',
  'approved',
  'rejected',
  'revoked',
] as const;
export type VersionStatus = (typeof versionStatuses)[number];

export const packageStatuses = [
  'requested',
  'building',
  'packaged',
  'published',
  'failed',
] as const;
export type PackageStatus = (typeof packageStatuses)[number];

export type ProductOperation = 'approve' | 'deprecate' | 'retire' | 'block' | 'unblock';
export type VersionOperation = 'request-approval' | 'approve' | 'reject' | 'revoke';

/** Reason codes the API accepts per operation; codes, never free text. */
export const productReasons = {
  deprecate: ['superseded', 'vendor_end_of_support', 'policy', 'other'],
  retire: ['no_longer_used', 'superseded', 'vendor_end_of_life', 'other'],
  block: ['security_risk', 'license', 'vendor_unsupported', 'policy', 'duplicate', 'other'],
  unblock: ['re_evaluation', 'error_correction', 'other'],
} as const satisfies Partial<Record<ProductOperation, readonly string[]>>;

export const versionReasons = {
  reject: ['hash_unverified', 'untrusted_source', 'license', 'security_risk', 'policy', 'other'],
  revoke: ['security_risk', 'defect', 'superseded', 'policy', 'other'],
} as const satisfies Partial<Record<VersionOperation, readonly string[]>>;

export type SoftwareProduct = {
  id: string;
  name: string;
  publisher: string | null;
  approvalStatus: ProductStatus;
  approvalReason: string | null;
  version: number;
  updatedAt: string;
};

export type SoftwareVersion = {
  id: string;
  productId: string;
  productName: string;
  productVersion: string;
  installerSha256: string;
  /** Null when redacted for view-only users. */
  installerUrl: string | null;
  publisher: string | null;
  /** Null when redacted for view-only users. */
  installCommand: string | null;
  installCommandSha256: string;
  /** Null when redacted for view-only users. */
  detectionRule: string | null;
  detectionRuleSha256: string;
  bindingSha256: string;
  registeredBy: string;
  approvalStatus: VersionStatus;
  approvalReason: string | null;
  requestedBy: string | null;
  requestedAt: string | null;
  decidedBy: string | null;
  decidedAt: string | null;
  version: number;
  createdAt: string;
};

export type SoftwareApproval = {
  id: string;
  fromStatus: string;
  toStatus: string;
  operation: string;
  reason: string | null;
  installerSha256: string;
  bindingSha256: string;
  actorUserId: string | null;
  actorSystem: string | null;
  createdAt: string;
};

export type SoftwarePackage = {
  id: string;
  provider: string;
  providerPackageId: string | null;
  versionId: string;
  status: PackageStatus;
  installerSha256: string | null;
  hashMismatch: boolean;
  /** The version's approval was revoked; publishing is refused. */
  versionRevoked: boolean;
  /** The product is blocked or retired; publishing is refused. */
  productBlocked: boolean;
  /** Open package_published_after_revoke finding: published although Turaco would refuse it. */
  publishedAfterRevoke: boolean;
  packageAttempt: number;
  publishAttempt: number;
  publishedAt: string | null;
  managementProvider: string | null;
  managementArtifactExternalId: string | null;
  managementArtifactId: string | null;
  source: string;
  observedAt: string | null;
  lastSyncedAt: string | null;
  publishRequestedAt: string | null;
  version: number;
};

export type SoftwareVersionDetail = {
  version: SoftwareVersion;
  approvals: SoftwareApproval[];
  packages: SoftwarePackage[];
};

export type CatalogEntry = {
  providerId: string;
  name: string;
  publisher: string | null;
  latestVersion: string | null;
  sourceUrl: string | null;
};

export type PackageSyncResult = {
  checked: number;
  changed: number;
  linked: number;
  findingsRaised: number;
  findingsResolved: number;
  /** Reports older than the stored state, ignored. */
  stale: number;
  /** Packages the provider could not report on. */
  errors: number;
};

export type RegisterVersionInput = {
  productId: string;
  version: string;
  installerSha256: string;
  installerUrl: string;
  publisher: string;
  installCommand: string;
  detectionRule: string;
};
