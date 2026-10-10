// Types mirror api/openapi/openapi.yaml (F14 People, Teams, Locations, Departments).

export type AccountSource = 'directory' | 'local' | 'emergency';
export type AccountKind = 'employee' | 'external';
export type StatusSource = 'platform' | 'directory';

/** A row of `POST /users/query` and the response of every user write. */
export type PersonRow = {
  id: string;
  displayName: string;
  givenName?: string | null;
  familyName?: string | null;
  primaryEmail?: string | null;
  status: string;
  statusSource: StatusSource;
  accountKind: AccountKind;
  source: AccountSource;
  version: number;
  updatedAt: string;
  departmentId?: string | null;
  primaryLocationId?: string | null;
  managerUserId?: string | null;
  employeeNumber?: string | null;
  accessExpiresAt?: string | null;
};

/** Owner of one attribute: who may change it, where the value came from and how fresh it is. */
export type FieldOwner = {
  key: string;
  owner: 'directory' | 'platform';
  source: string;
  observedAt?: string | null;
};

export type ExternalIdentity = {
  id: string;
  providerKey: string;
  /** Last four characters of the subject; the whole value is never sent. */
  subjectSuffix: string;
  linkedAt: string;
  /** How an Entra identity was linked; empty for directory identities. */
  via: '' | 'administrator' | 'source_anchor' | 'provisioning' | 'cli';
  username: string | null;
  enabled: boolean;
  lastSeenAt: string | null;
  deletedObservedAt: string | null;
};

/** What the link dialog needs to know about the installation (platform administrators only). */
export type EntraLinkingInfo = { configured: boolean; tenantId: string; tenantIds: string[] };

export type EntraLinkResult = {
  user: PersonRow;
  credentialDeleted: boolean;
  noticeSent: boolean;
};

export type PersonDetail = PersonRow & {
  fields: FieldOwner[];
  externalIdentities: ExternalIdentity[];
};

export type PersonCreate = {
  displayName: string;
  givenName?: string | null;
  familyName?: string | null;
  primaryEmail?: string | null;
  employeeNumber?: string | null;
  departmentId?: string | null;
  primaryLocationId?: string | null;
  accountKind?: 'employee';
};

export type ProfileUpdate = {
  expectedVersion: number;
  displayName?: string;
  givenName?: string | null;
  familyName?: string | null;
  primaryEmail?: string | null;
  employeeNumber?: string | null;
};

/** Result of an invitation or reset: `link` appears once and only for an invitation. */
export type CredentialLink = { expiresAt: string; mailed: boolean; link?: string };

export type TeamRole = 'lead' | 'member';

export type TeamMemberRow = {
  userId: string;
  displayName: string;
  role?: string | null;
  source: string;
  validFrom: string;
};

export type TeamRow = {
  id: string;
  name: string;
  description: string;
  active: boolean;
  version: number;
  updatedAt: string;
  leads?: TeamMemberRow[];
};

export type LocationKind = 'site' | 'area';

export type LocationNode = {
  id: string;
  name: string;
  kind: LocationKind;
  parentId?: string | null;
  code?: string | null;
  description: string;
  externalKey?: string | null;
  active: boolean;
  version: number;
  updatedAt: string;
};

export type DepartmentNode = {
  id: string;
  name: string;
  code?: string | null;
  parentId?: string | null;
  externalKey?: string | null;
  active: boolean;
  version: number;
  updatedAt: string;
};

export type OrgNode = {
  id: string;
  name: string;
  code?: string | null;
  parentId?: string | null;
  active: boolean;
  version: number;
  kind?: LocationKind;
  description?: string;
};

export type AuthMethodsInfo = {
  local: boolean;
  password: boolean;
  kerberos: boolean;
  emergency: boolean;
};

// ---- CSV import, bulk operations, directory linking, access extension (F14 section 1.6) ----

export type ImportKind = 'users' | 'locations' | 'departments';
export type BatchKind = ImportKind | 'bulk_users';
export type ImportMode = 'create_only' | 'update_only' | 'upsert';
export type ImportMatchKey = 'primary_email' | 'employee_number' | 'code';
export type RowAction = 'create' | 'update' | 'unchanged' | 'reject';

export type ImportRowIssue = { field?: string; code: string };
export type ImportDiffValue = { from: string | null; to: string | null };

export type ImportRow = {
  row: number;
  action: RowAction;
  /** Match key of an import row, or the User id of a bulk row. */
  key: string;
  label?: string;
  userId?: string;
  diff: Record<string, ImportDiffValue>;
  errors: ImportRowIssue[];
  warnings: ImportRowIssue[];
};

export type ImportBatch = {
  id: string;
  kind: BatchKind;
  matchKey?: string;
  mode?: string;
  operation?: string;
  status: 'previewed' | 'applied';
  fileHash?: string;
  previewHash: string;
  rowCount: number;
  counts: Partial<Record<RowAction, number>>;
  unknownColumns: string[];
  expiresAt: string;
  appliedAt?: string;
  appliedCounts?: Partial<Record<RowAction, number>>;
};

export type ImportBatchPreview = ImportBatch & {
  rows: ImportRow[];
  nextCursor?: string;
  replayed?: boolean;
};

export type BulkOperation =
  'set_department' | 'set_primary_location' | 'set_manager' | 'deactivate';

export type BulkPreviewRequest = {
  operation: BulkOperation;
  userIds: string[];
  departmentId?: string | null;
  locationId?: string | null;
  managerUserId?: string | null;
  reason?: string;
};

export type SyncConflictChoice = {
  runId: string;
  providerKey: string;
  externalId: string;
  username: string;
};

export type ExtendReason =
  'contract_renewed' | 'project_extended' | 'sponsor_request' | 'correction';
