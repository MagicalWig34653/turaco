import { api } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type {
  EffectivePermissions,
  Holder,
  Permission,
  Role,
  RoleAssignment,
  RoleAssignmentCreate,
  RoleAssignmentFilter,
  RoleCreate,
  RoleMembers,
  RolePermissionsUpdate,
  RoleTemplate,
  RoleUpdate,
  SodRule,
} from './types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

registerErrorMessages({
  'authorization.not_found': 'error.notFound',
  'authorization.subject_not_found': 'error.subjectNotFound',
  'authorization.invalid_request': 'error.invalidRequest',
  'authorization.unknown_permission': 'error.unknownPermission',
  'authorization.unknown_template': 'access.error.unknownTemplate',
  'authorization.duplicate_key': 'error.duplicateKey',
  'authorization.built_in_role': 'error.builtInRole',
  'authorization.role_in_use': 'error.roleInUse',
  'authorization.duplicate_assignment': 'error.duplicateAssignment',
  'authorization.last_administrator': 'error.lastAdministrator',
  'authorization.version_conflict': 'people.error.versionConflict',
});

/** Roles, Role Templates, Role Assignments, Permissions and effective access. */
export const accessApi = {
  permissions: (signal?: Signal) => api.get<{ items: Permission[] }>('/permissions', { signal }),
  templates: (signal?: Signal) => api.get<{ items: RoleTemplate[] }>('/role-templates', { signal }),
  sodRules: (signal?: Signal) => api.get<{ items: SodRule[] }>('/access/sod-rules', { signal }),

  roles: (signal?: Signal) => api.get<{ items: Role[] }>('/roles', { signal }),
  role: (id: string, signal?: Signal) => api.get<Role>(`/roles/${enc(id)}`, { signal }),
  roleMembers: (id: string, signal?: Signal) =>
    api.get<RoleMembers>(`/roles/${enc(id)}/members`, { signal }),
  createRole: (body: RoleCreate) => api.post<Role>('/roles', body),
  updateRole: (id: string, body: RoleUpdate) => api.patch<Role>(`/roles/${enc(id)}`, body),
  setRolePermissions: (id: string, body: RolePermissionsUpdate) =>
    api.put<Role>(`/roles/${enc(id)}/permissions`, body),
  deleteRole: (id: string, expectedVersion?: number) =>
    api.delete<void>(`/roles/${enc(id)}`, { query: { expectedVersion } }),

  roleAssignments: (filter: RoleAssignmentFilter, cursor?: string, signal?: Signal) =>
    api.get<Page<RoleAssignment>>('/role-assignments', {
      signal,
      query: {
        roleId: filter.roleId,
        subjectType: filter.subjectType,
        subjectId: filter.subjectId,
        includeRevoked: filter.includeRevoked,
        limit: 50,
        cursor,
      },
    }),
  assignRole: (body: RoleAssignmentCreate) => api.post<RoleAssignment>('/role-assignments', body),
  revokeRoleAssignment: (id: string) =>
    api.post<RoleAssignment>(`/role-assignments/${enc(id)}/revoke`),

  effectivePermissions: (userId: string, signal?: Signal) =>
    api.get<EffectivePermissions>(`/users/${enc(userId)}/effective-permissions`, { signal }),
  holders: (permission: string, cursor?: string, signal?: Signal) =>
    api.get<Page<Holder>>('/access/holders', { signal, query: { permission, limit: 50, cursor } }),
};
