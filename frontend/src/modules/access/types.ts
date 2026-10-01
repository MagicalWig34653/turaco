// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

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

export type RoleAssignmentFilter = {
  roleId?: string;
  subjectType?: SubjectType | '';
  includeRevoked?: boolean;
};
