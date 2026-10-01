// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export type AuthMethods = { password: boolean; kerberos: boolean; emergency: boolean };

export type AuthSession = {
  userId: string;
  authMethod: string;
  expiresAt: string;
  permissions: string[];
};

export type LoginRequest = { identifier: string; password: string };
export type EmergencyLoginRequest = { login: string; password: string };

export type UserStatus = 'active' | 'inactive' | 'departed' | 'external' | 'unknown';

export type User = {
  id: string;
  displayName: string;
  givenName?: string | null;
  familyName?: string | null;
  primaryEmail?: string | null;
  status: UserStatus;
  updatedAt: string;
};

export type DirectoryGroup = {
  id: string;
  providerKey: string;
  externalId: string;
  displayName: string;
  description?: string | null;
  firstObservedAt: string;
  lastObservedAt: string;
  deletedObservedAt?: string | null;
};

export type Page<T> = { items: T[]; nextCursor?: string };

export type PermissionRisk = 'normal' | 'elevated' | 'high';
export type Permission = { name: string; description: string; risk: PermissionRisk };

export type Role = {
  id: string;
  key: string;
  name: string;
  description: string;
  builtIn: boolean;
  permissions: string[];
  activeAssignments: number;
  createdAt: string;
  updatedAt: string;
};
export type RoleCreate = {
  key: string;
  name: string;
  description?: string;
  permissions?: string[];
};
export type RoleUpdate = { name?: string; description?: string };

export type SubjectType = 'user' | 'directory_group';

export type RoleAssignment = {
  id: string;
  roleId: string;
  roleKey: string;
  subjectType: SubjectType;
  subjectId: string;
  subjectDisplayName: string;
  scope: 'global';
  createdAt: string;
  createdBy?: string;
  revokedAt?: string;
  revokedBy?: string;
};
export type RoleAssignmentCreate = { roleId: string; subjectType: SubjectType; subjectId: string };

export type SyncOutcome = 'running' | 'succeeded' | 'failed' | 'sweep_withheld';
export type SyncConflictKind = 'email_in_use' | 'manager_unresolved' | 'invalid_attributes';
export type SyncConflict = { kind: SyncConflictKind; externalId: string; username: string };

export type DirectorySyncRun = {
  id: string;
  providerKey: string;
  jobId: string | null;
  trigger: 'scheduled' | 'manual';
  startedAt: string;
  observedAt: string | null;
  finishedAt: string | null;
  outcome: SyncOutcome;
  counts: Record<string, number>;
  conflicts: SyncConflict[];
  conflictCount: number;
  error: string | null;
};
export type DirectorySyncRequest = { jobId: string; created: boolean };

export type AuditEvent = {
  id: string;
  occurredAt: string;
  actorId?: string;
  action: string;
  targetType: string;
  targetId: string;
  correlationId: string;
  before?: unknown;
  after?: unknown;
  metadata: Record<string, unknown>;
};

export type AuditFilter = {
  actionPrefix?: string;
  targetType?: string;
  targetId?: string;
  actorId?: string;
  correlationId?: string;
  from?: string;
  to?: string;
};

export type RoleAssignmentFilter = {
  roleId?: string;
  subjectType?: SubjectType | '';
  includeRevoked?: boolean;
};
