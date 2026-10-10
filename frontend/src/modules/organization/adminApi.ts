import { api } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type { QueryPageResult } from './adminModel';
import type {
  AuthMethodsInfo,
  BulkPreviewRequest,
  CredentialLink,
  DepartmentNode,
  EntraLinkingInfo,
  EntraLinkResult,
  ExtendReason,
  ImportBatch,
  ImportBatchPreview,
  ImportKind,
  ImportMatchKey,
  ImportMode,
  ImportRow,
  LocationNode,
  PersonCreate,
  PersonDetail,
  PersonRow,
  ProfileUpdate,
  TeamMemberRow,
  SyncConflictChoice,
  TeamRole,
  TeamRow,
} from './adminTypes';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

registerErrorMessages({
  'organization.conflict': 'people.error.conflict',
  'organization.version_conflict': 'people.error.versionConflict',
  'organization.field_directory_owned': 'people.error.directoryOwned',
  'organization.impact_confirmation_required': 'people.error.impactRequired',
  'organization.invalid_hierarchy': 'people.error.invalidHierarchy',
  'organization.target_inactive': 'people.error.targetInactive',
  'organization.invalid_state': 'people.error.invalidState',
  'organization.directory_user': 'people.error.directoryUser',
  'organization.emergency_account': 'people.error.emergencyAccount',
  'organization.self_operation': 'people.error.selfOperation',
  'organization.last_administrator': 'people.error.lastAdministrator',
  'organization.directory_identity_disabled': 'people.error.directoryIdentityDisabled',
  'organization.no_email': 'people.error.noEmail',
  'organization.team_inactive': 'people.error.teamInactive',
  'organization.user_not_active': 'people.error.userNotActive',
  'organization.invalid_status': 'people.error.invalidState',
  'access.team_membership_self': 'people.error.teamMembershipSelf',
  'access.dominance_required': 'access.error.dominanceRequired',
  'access.grant_exceeds_holder': 'access.error.grantExceedsHolder',
  'access.high_risk_requires_administrator': 'access.error.highRiskAdministrator',
  'access.role_not_held': 'access.error.roleNotHeld',
  'access.self_assignment': 'access.error.selfAssignment',
  'access.admin_no_expiry': 'access.error.adminNoExpiry',
  'access.local_account_high_risk': 'access.error.localAccountHighRisk',
  'access.external_party_permission_required': 'access.error.externalPartyPermission',
  'access.sod_acknowledgement_required': 'access.error.sodRequired',
  'organization.import_too_large': 'people.import.error.tooLarge',
  'organization.import_too_many_rows': 'people.import.error.tooManyRows',
  'organization.import_stale': 'people.import.error.stale',
  'organization.import_preview_mismatch': 'people.import.error.mismatch',
  'organization.directory_link_refused_roles': 'people.link.error.roles',
  'organization.entra_not_configured': 'entra.error.notConfigured',
  'organization.entra_tenant_not_allowed': 'entra.error.tenantNotAllowed',
  'organization.entra_identity_in_use': 'entra.error.inUse',
  'organization.entra_tenant_already_linked': 'entra.error.tenantAlreadyLinked',
  'organization.directory_identity_in_use': 'people.link.error.inUse',
  'access.admin_required': 'people.link.error.adminRequired',
  'auth.mail_not_configured': 'auth.error.mailNotConfigured',
  'auth.base_url_not_configured': 'auth.error.baseUrlNotConfigured',
  'auth.mail_failed': 'auth.error.mailFailed',
  'auth.method_unavailable': 'auth.error.methodUnavailable',
  'auth.invalid_token': 'auth.error.invalidToken',
  'auth.password_policy': 'auth.error.passwordPolicy',
  'auth.too_many_attempts': 'auth.error.tooManyAttempts',
});

/** Fetches every page of a flat list endpoint (locations and departments are small trees). */
async function allPages<T>(
  path: string,
  signal: Signal,
  query: Record<string, string | number> = {},
): Promise<T[]> {
  const items: T[] = [];
  let cursor: string | undefined;
  for (let guard = 0; guard < 20; guard += 1) {
    const page: Page<T> = await api.get<Page<T>>(path, {
      signal,
      query: { ...query, limit: 200, ...(cursor ? { cursor } : {}) },
    });
    items.push(...page.items);
    if (!page.nextCursor) return items;
    cursor = page.nextCursor;
  }
  return items;
}

/** People, Teams, Locations and Departments administration (F14). */
export const peopleAdminApi = {
  authMethods: (signal?: Signal) => api.get<AuthMethodsInfo>('/auth/methods', { signal }),

  user: (id: string, signal?: Signal) => api.get<PersonDetail>(`/users/${enc(id)}`, { signal }),
  usersQuery: (body: unknown, signal?: Signal) =>
    api.post<QueryPageResult<PersonRow>>('/users/query', body, { signal }),
  searchUsers: (q: string, signal?: Signal) =>
    api.get<Page<PersonRow>>('/users', { signal, query: { q, limit: 15 } }),
  createUser: (body: PersonCreate) => api.post<PersonRow>('/users', body),
  updateProfile: (id: string, body: ProfileUpdate) =>
    api.patch<PersonRow>(`/users/${enc(id)}/profile`, body),
  setDepartment: (id: string, expectedVersion: number, departmentId: string | null) =>
    api.put<PersonRow>(`/users/${enc(id)}/department`, { expectedVersion, departmentId }),
  setPrimaryLocation: (id: string, expectedVersion: number, locationId: string | null) =>
    api.put<PersonRow>(`/users/${enc(id)}/primary-location`, { expectedVersion, locationId }),
  setManager: (id: string, expectedVersion: number, managerUserId: string | null) =>
    api.put<PersonRow>(`/users/${enc(id)}/manager`, { expectedVersion, managerUserId }),
  deactivate: (id: string, expectedVersion: number, reason: string) =>
    api.post<PersonRow>(`/users/${enc(id)}/deactivate`, { expectedVersion, reason }),
  reactivate: (id: string, expectedVersion: number, reason: string) =>
    api.post<PersonRow>(`/users/${enc(id)}/reactivate`, { expectedVersion, reason }),
  markDeparted: (id: string, expectedVersion: number, reason: string) =>
    api.post<PersonRow>(`/users/${enc(id)}/mark-departed`, { expectedVersion, reason }),
  sendInvitation: (id: string) => api.post<CredentialLink>(`/users/${enc(id)}/send-invitation`),
  resetPassword: (id: string) => api.post<CredentialLink>(`/users/${enc(id)}/reset-password`),

  teams: (q: string, signal?: Signal) => allPages<TeamRow>('/teams', signal, q ? { q } : {}),
  team: (id: string, signal?: Signal) => api.get<TeamRow>(`/teams/${enc(id)}`, { signal }),
  createTeam: (name: string) => api.post<TeamRow>('/teams', { name }),
  updateTeam: (
    id: string,
    body: { name?: string; description?: string; expectedVersion?: number },
  ) => api.patch<TeamRow>(`/teams/${enc(id)}`, body),
  activateTeam: (id: string) => api.post<TeamRow>(`/teams/${enc(id)}/activate`),
  deactivateTeam: (id: string, confirmImpact: boolean) =>
    api.post<TeamRow>(
      `/teams/${enc(id)}/deactivate`,
      confirmImpact ? { confirmImpact: true } : undefined,
    ),
  teamMembers: (id: string, signal?: Signal) =>
    allPages<TeamMemberRow>(`/teams/${enc(id)}/members`, signal),
  addTeamMember: (id: string, userId: string, role: TeamRole) =>
    api.post<TeamMemberRow>(`/teams/${enc(id)}/members`, { userId, role }),
  removeTeamMember: (id: string, userId: string) =>
    api.delete<void>(`/teams/${enc(id)}/members/${enc(userId)}`),
  setTeamMemberRole: (id: string, userId: string, role: TeamRole) =>
    api.put<TeamMemberRow>(`/teams/${enc(id)}/members/${enc(userId)}/role`, { role }),

  locations: (signal?: Signal) => allPages<LocationNode>('/locations', signal),
  createLocation: (body: {
    kind: 'site' | 'area';
    parentId: string | null;
    name: string;
    code?: string | null;
    description?: string;
  }) => api.post<LocationNode>('/locations', body),
  updateLocation: (
    id: string,
    body: { expectedVersion: number; name?: string; code?: string | null; description?: string },
  ) => api.patch<LocationNode>(`/locations/${enc(id)}`, body),
  moveLocation: (id: string, expectedVersion: number, parentId: string | null) =>
    api.post<LocationNode>(`/locations/${enc(id)}/move`, { expectedVersion, parentId }),
  setLocationActive: (
    id: string,
    active: boolean,
    expectedVersion: number,
    confirmImpact: boolean,
  ) =>
    api.post<LocationNode>(`/locations/${enc(id)}/${active ? 'activate' : 'deactivate'}`, {
      expectedVersion,
      ...(confirmImpact ? { confirmImpact: true } : {}),
    }),

  departments: (signal?: Signal) => allPages<DepartmentNode>('/departments', signal),
  createDepartment: (body: { name: string; code?: string | null; parentId: string | null }) =>
    api.post<DepartmentNode>('/departments', body),
  updateDepartment: (
    id: string,
    body: { expectedVersion: number; name?: string; code?: string | null },
  ) => api.patch<DepartmentNode>(`/departments/${enc(id)}`, body),
  moveDepartment: (id: string, expectedVersion: number, parentId: string | null) =>
    api.post<DepartmentNode>(`/departments/${enc(id)}/move`, { expectedVersion, parentId }),
  setDepartmentActive: (
    id: string,
    active: boolean,
    expectedVersion: number,
    confirmImpact: boolean,
  ) =>
    api.post<DepartmentNode>(`/departments/${enc(id)}/${active ? 'activate' : 'deactivate'}`, {
      expectedVersion,
      ...(confirmImpact ? { confirmImpact: true } : {}),
    }),

  // ---- CSV import, bulk operations, directory linking, access extension ----
  previewImport: (
    kind: ImportKind,
    matchKey: ImportMatchKey,
    mode: ImportMode,
    file: File,
  ): Promise<ImportBatchPreview> => {
    const form = new FormData();
    form.set('kind', kind);
    form.set('matchKey', matchKey);
    form.set('mode', mode);
    form.set('file', file, file.name);
    return api.post<ImportBatchPreview>('/import-batches', form);
  },
  importBatch: (id: string, signal?: Signal) =>
    api.get<ImportBatch>(`/import-batches/${enc(id)}`, { signal }),
  importRows: (id: string, action: string, cursor: string | undefined, signal?: Signal) =>
    api.get<{ items: ImportRow[]; nextCursor?: string }>(`/import-batches/${enc(id)}/rows`, {
      signal,
      query: { action, cursor, limit: 100 },
    }),
  /** Path of the rejected-rows CSV download (same origin, session cookie). */
  rejectedCsvPath: (id: string) => `/api/v1/import-batches/${enc(id)}/rejected.csv`,
  applyImport: (id: string, previewHash: string, expectedRejects: number) =>
    api.post<ImportBatchPreview>(`/import-batches/${enc(id)}/apply`, {
      previewHash,
      expectedRejects,
    }),
  bulkPreview: (request: BulkPreviewRequest) =>
    api.post<ImportBatchPreview>('/users/bulk-operations', { ...request, dryRun: true }),
  bulkApply: (batchId: string, previewHash: string, expectedRejects: number) =>
    api.post<ImportBatchPreview>('/users/bulk-operations', {
      dryRun: false,
      batchId,
      previewHash,
      expectedRejects,
    }),
  linkDirectoryIdentity: (
    id: string,
    expectedVersion: number,
    choice: Pick<SyncConflictChoice, 'runId' | 'externalId'>,
  ) =>
    api.post<PersonRow>(`/users/${enc(id)}/link-directory-identity`, {
      expectedVersion,
      runId: choice.runId,
      externalId: choice.externalId,
    }),
  entraLinking: (signal?: Signal) => api.get<EntraLinkingInfo>('/users/entra-linking', { signal }),
  linkEntraIdentity: (id: string, expectedVersion: number, tenantId: string, objectId: string) =>
    api.post<EntraLinkResult>(`/users/${enc(id)}/external-identities/entra`, {
      expectedVersion,
      tenantId,
      objectId,
    }),
  unlinkExternalIdentity: (id: string, identityId: string) =>
    api.delete<EntraLinkResult>(`/users/${enc(id)}/external-identities/${enc(identityId)}`),
  extendAccess: (
    id: string,
    expectedVersion: number,
    accessExpiresAt: string,
    reason: ExtendReason,
  ) =>
    api.post<PersonRow>(`/users/${enc(id)}/extend-access`, {
      expectedVersion,
      accessExpiresAt,
      reason,
    }),
  /** Identities the directory could not create because their e-mail address belongs to a local account. */
  syncConflicts: async (signal?: Signal): Promise<SyncConflictChoice[]> => {
    const page = await api.get<
      Page<{
        id: string;
        providerKey: string;
        outcome: string;
        conflicts: { kind: string; externalId: string; username: string }[];
      }>
    >('/directory-sync-runs', { signal, query: { limit: 5 } });
    const out: SyncConflictChoice[] = [];
    for (const run of page.items) {
      if (run.outcome !== 'succeeded' && run.outcome !== 'sweep_withheld') continue;
      for (const conflict of run.conflicts) {
        if (conflict.kind === 'email_in_use')
          out.push({
            runId: run.id,
            providerKey: run.providerKey,
            externalId: conflict.externalId,
            username: conflict.username,
          });
      }
    }
    return out;
  },
};
