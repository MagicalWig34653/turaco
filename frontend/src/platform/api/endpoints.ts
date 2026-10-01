import { api, type ApiClient } from './client';
import type {
  AuditEvent,
  AuditFilter,
  AuthMethods,
  AuthSession,
  DirectoryGroup,
  DirectorySyncRequest,
  DirectorySyncRun,
  EmergencyLoginRequest,
  LoginRequest,
  Page,
  Permission,
  Role,
  RoleAssignment,
  RoleAssignmentCreate,
  RoleAssignmentFilter,
  RoleCreate,
  RoleUpdate,
  User,
} from './types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

/** Typed wrappers for the endpoints the web UI uses (api/openapi/openapi.yaml). */
export function createEndpoints(client: ApiClient) {
  return {
    authMethods: (signal?: Signal) => client.get<AuthMethods>('/auth/methods', { signal }),
    login: (body: LoginRequest) =>
      client.post<void>('/auth/login', body, { skipUnauthorizedHandler: true }),
    emergencyLogin: (body: EmergencyLoginRequest) =>
      client.post<void>('/auth/emergency-login', body, { skipUnauthorizedHandler: true }),
    kerberosLogin: (signal?: Signal) =>
      client.get<void>('/auth/kerberos', { signal, skipUnauthorizedHandler: true }),
    session: (signal?: Signal) =>
      client.get<AuthSession>('/auth/session', { signal, skipUnauthorizedHandler: true }),
    logout: () => client.post<void>('/auth/logout', undefined, { skipUnauthorizedHandler: true }),

    permissions: (signal?: Signal) =>
      client.get<{ items: Permission[] }>('/permissions', { signal }),

    roles: (signal?: Signal) => client.get<{ items: Role[] }>('/roles', { signal }),
    role: (id: string, signal?: Signal) => client.get<Role>(`/roles/${enc(id)}`, { signal }),
    createRole: (body: RoleCreate) => client.post<Role>('/roles', body),
    updateRole: (id: string, body: RoleUpdate) => client.patch<Role>(`/roles/${enc(id)}`, body),
    setRolePermissions: (id: string, permissions: string[]) =>
      client.put<Role>(`/roles/${enc(id)}/permissions`, { permissions }),
    deleteRole: (id: string) => client.delete<void>(`/roles/${enc(id)}`),

    roleAssignments: (filter: RoleAssignmentFilter, cursor?: string, signal?: Signal) =>
      client.get<Page<RoleAssignment>>('/role-assignments', {
        signal,
        query: {
          roleId: filter.roleId,
          subjectType: filter.subjectType,
          includeRevoked: filter.includeRevoked,
          limit: 50,
          cursor,
        },
      }),
    assignRole: (body: RoleAssignmentCreate) =>
      client.post<RoleAssignment>('/role-assignments', body),
    revokeRoleAssignment: (id: string) =>
      client.post<RoleAssignment>(`/role-assignments/${enc(id)}/revoke`),

    searchUsers: (q: string, signal?: Signal) =>
      client.get<Page<User>>('/users', { signal, query: { q, limit: 10 } }),
    user: (id: string, signal?: Signal) => client.get<User>(`/users/${enc(id)}`, { signal }),
    searchDirectoryGroups: (q: string, signal?: Signal) =>
      client.get<Page<DirectoryGroup>>('/directory-groups', { signal, query: { q, limit: 10 } }),

    directorySyncRuns: (cursor?: string, signal?: Signal) =>
      client.get<Page<DirectorySyncRun>>('/directory-sync-runs', {
        signal,
        query: { limit: 25, cursor },
      }),
    directorySyncRun: (id: string, signal?: Signal) =>
      client.get<DirectorySyncRun>(`/directory-sync-runs/${enc(id)}`, { signal }),
    requestDirectorySync: () => client.post<DirectorySyncRequest>('/directory-sync-runs'),

    auditEvents: (filter: AuditFilter, cursor?: string, signal?: Signal) =>
      client.get<Page<AuditEvent>>('/audit-events', {
        signal,
        query: { ...filter, limit: 50, cursor },
      }),
  };
}

export const endpoints = createEndpoints(api);
