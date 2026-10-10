// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export type PermissionRisk = 'normal' | 'elevated' | 'high';
export type PermissionGroupKind = 'view' | 'manage' | 'execute' | 'approve' | 'admin' | 'other';

export type Permission = {
  name: string;
  description: string;
  risk: PermissionRisk;
  /** Module registry key (for example `servicedesk`); absent on older servers. */
  module?: string;
  group?: PermissionGroupKind;
  /** Companion permissions this one cannot work without. */
  needs?: string[];
};

export type Role = {
  id: string;
  key: string;
  name: string;
  description: string;
  builtIn: boolean;
  permissions: string[];
  activeAssignments: number;
  version?: number;
  templateKey?: string;
  templateVersion?: number;
  createdAt: string;
  updatedAt: string;
};

export type RoleCreate = {
  key: string;
  name: string;
  description?: string;
  permissions?: string[];
  templateKey?: string;
  cloneFromRoleId?: string;
  acknowledgedRules?: string[];
  reason?: string;
};
export type RoleUpdate = { expectedVersion?: number; name?: string; description?: string };
export type RolePermissionsUpdate = {
  expectedVersion?: number;
  permissions: string[];
  acknowledgedRules?: string[];
  reason?: string;
};

export type TemplateUse = {
  roleId: string;
  roleKey: string;
  name: string;
  templateVersion: number;
  /** Permissions the template now has that the role lacks (drift). */
  missing: string[];
  /** Permissions the role has beyond the template. */
  extra: string[];
};

export type RoleTemplate = {
  key: string;
  version: number;
  name: string;
  description: string;
  nameKey: string;
  descriptionKey: string;
  audience: string;
  permissions: string[];
  requiresModules: string[];
  externalOnly: boolean;
  administratorAssignOnly: boolean;
  roles: TemplateUse[];
};

export type SodRule = { key: string; messageKey: string; left: string[]; right: string[] };

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
  expiresAt?: string;
};
export type RoleAssignmentCreate = {
  roleId: string;
  subjectType: SubjectType;
  subjectId: string;
  expiresAt?: string;
  acknowledgedRules?: string[];
  reason?: string;
};

export type RoleAssignmentFilter = {
  roleId?: string;
  subjectType?: SubjectType | '';
  subjectId?: string;
  includeRevoked?: boolean;
};

export type EffectiveRole = {
  roleId: string;
  roleKey: string;
  roleName: string;
  builtInAdmin: boolean;
  assignmentId: string;
  source: 'direct' | 'directory_group';
  groupId?: string;
  expiresAt?: string;
};
export type EffectivePermission = {
  name: string;
  risk: string;
  module: string;
  /** Assignment ids (see `EffectiveRole.assignmentId`) that grant the permission. */
  grantedBy: string[];
};
export type EffectivePermissions = {
  userId: string;
  roles: EffectiveRole[];
  permissions: EffectivePermission[];
  /** Separation-of-duties rule keys that apply to the combination. */
  warnings: string[];
  ceilings: string[];
  localAccount: boolean;
  excluded: string[];
};

export type Holder = {
  assignmentId: string;
  roleId: string;
  roleKey: string;
  roleName: string;
  builtInAdmin: boolean;
  subjectType: SubjectType;
  subjectId: string;
  subjectDisplayName: string;
  expiresAt?: string;
};

export type RoleMember = {
  userId: string;
  displayName: string;
  source: 'direct' | 'directory_group';
};
export type RoleMembers = { items: RoleMember[]; capped: boolean; limit: number };
