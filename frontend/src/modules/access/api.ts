import { api } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type {
  Permission,
  Role,
  RoleAssignment,
  RoleAssignmentCreate,
  RoleAssignmentFilter,
  RoleCreate,
  RoleUpdate,
} from './types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

registerErrorMessages({
  'authorization.not_found': 'error.notFound',
  'authorization.subject_not_found': 'error.subjectNotFound',
  'authorization.invalid_request': 'error.invalidRequest',
  'authorization.unknown_permission': 'error.unknownPermission',
  'authorization.duplicate_key': 'error.duplicateKey',
  'authorization.built_in_role': 'error.builtInRole',
  'authorization.role_in_use': 'error.roleInUse',
  'authorization.duplicate_assignment': 'error.duplicateAssignment',
  'authorization.last_administrator': 'error.lastAdministrator',
});

/** Roles, Role Assignments and Permissions. */
export const accessApi = {
  permissions: (signal?: Signal) => api.get<{ items: Permission[] }>('/permissions', { signal }),

  roles: (signal?: Signal) => api.get<{ items: Role[] }>('/roles', { signal }),
  role: (id: string, signal?: Signal) => api.get<Role>(`/roles/${enc(id)}`, { signal }),
  createRole: (body: RoleCreate) => api.post<Role>('/roles', body),
  updateRole: (id: string, body: RoleUpdate) => api.patch<Role>(`/roles/${enc(id)}`, body),
  setRolePermissions: (id: string, permissions: string[]) =>
    api.put<Role>(`/roles/${enc(id)}/permissions`, { permissions }),
  deleteRole: (id: string) => api.delete<void>(`/roles/${enc(id)}`),

  roleAssignments: (filter: RoleAssignmentFilter, cursor?: string, signal?: Signal) =>
    api.get<Page<RoleAssignment>>('/role-assignments', {
      signal,
      query: {
        roleId: filter.roleId,
        subjectType: filter.subjectType,
        includeRevoked: filter.includeRevoked,
        limit: 50,
        cursor,
      },
    }),
  assignRole: (body: RoleAssignmentCreate) => api.post<RoleAssignment>('/role-assignments', body),
  revokeRoleAssignment: (id: string) =>
    api.post<RoleAssignment>(`/role-assignments/${enc(id)}/revoke`),
};
