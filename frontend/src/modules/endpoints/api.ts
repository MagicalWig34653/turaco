import { api } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type {
  Device,
  DeviceDetail,
  DeviceFilters,
  Finding,
  FindingFilters,
  ReasonCode,
  SyncCounts,
  ArtifactFilters,
  ManagementArtifact,
  ManagementArtifactDetail,
  ManagementFilter,
  ManagementFilterFilters,
  ManagementObservation,
  DeviceManagementFilters,
  DeviceManagementPage,
  AssignmentPath,
  GroupManagement,
  UserManagement,
  ArtifactTargets,
} from './types';

registerErrorMessages({
  'endpoints.invalid_request': 'error.endpointsInvalidRequest',
  'endpoints.not_found': 'error.endpointsNotFound',
  'endpoints.conflict': 'error.endpointsConflict',
  'endpoints.version_conflict': 'error.endpointsVersionConflict',
  'endpoints.asset_invalid': 'error.endpointsAssetInvalid',
  'endpoints.sync_disabled': 'error.endpointsSyncDisabled',
  'endpoints.sync_running': 'error.endpointsSyncRunning',
  'endpoints.provider_not_configured': 'error.endpointsProviderNotConfigured',
  'endpoints.invalid_cursor': 'error.invalidRequest',
  'endpoints.invalid_limit': 'error.invalidRequest',
  'endpoints.sync_cooldown': 'error.endpointsSyncCooldown',
});

const enc = encodeURIComponent;
export const endpointsApi = {
  devices: (filters: DeviceFilters, cursor?: string, signal?: AbortSignal) =>
    api.get<Page<Device>>('/devices', { signal, query: { ...filters, limit: 50, cursor } }),
  device: (id: string, signal?: AbortSignal) =>
    api.get<DeviceDetail>(`/devices/${enc(id)}`, { signal }),
  link: (id: string, assetId: string, reason: ReasonCode, expectedVersion: number) =>
    api.post<Device>(`/devices/${enc(id)}/link`, { assetId, reason, expectedVersion }),
  unlink: (id: string, reason: ReasonCode, expectedVersion: number) =>
    api.post<Device>(`/devices/${enc(id)}/unlink`, { reason, expectedVersion }),
  findings: (filters: FindingFilters, cursor?: string, signal?: AbortSignal) =>
    api.get<Page<Finding>>('/endpoint-findings', {
      signal,
      query: { ...filters, limit: 50, cursor },
    }),
  artifacts: (filters: ArtifactFilters, cursor?: string, signal?: AbortSignal) =>
    api.get<Page<ManagementArtifact>>('/management-artifacts', {
      signal,
      query: { ...filters, limit: 50, cursor },
    }),
  artifact: (id: string, signal?: AbortSignal) =>
    api.get<ManagementArtifactDetail>(`/management-artifacts/${enc(id)}`, { signal }),
  managementFilters: (filters: ManagementFilterFilters, cursor?: string, signal?: AbortSignal) =>
    api.get<Page<ManagementFilter>>('/management-filters', {
      signal,
      query: { ...filters, limit: 50, cursor },
    }),
  observations: (id: string, cursor?: string, signal?: AbortSignal) =>
    api.get<Page<ManagementObservation>>(`/devices/${enc(id)}/management-observations`, {
      signal,
      query: { limit: 50, cursor },
    }),
  deviceManagement: (
    id: string,
    filters: DeviceManagementFilters,
    cursor?: string,
    signal?: AbortSignal,
  ) =>
    api.get<DeviceManagementPage>(`/devices/${enc(id)}/management`, {
      signal,
      query: { ...filters, limit: 50, cursor },
    }),
  assignmentPath: (id: string, artifactId: string, signal?: AbortSignal) =>
    api.get<AssignmentPath>(`/devices/${enc(id)}/management/${enc(artifactId)}/path`, { signal }),
  groupManagement: (id: string, cursor?: string, signal?: AbortSignal) =>
    api.get<GroupManagement>(`/directory-groups/${enc(id)}/management`, {
      signal,
      query: { limit: 50, cursor },
    }),
  userManagement: (id: string, cursor?: string, signal?: AbortSignal) =>
    api.get<UserManagement>(`/users/${enc(id)}/management`, {
      signal,
      query: { limit: 50, cursor },
    }),
  artifactTargets: (id: string, signal?: AbortSignal) =>
    api.get<ArtifactTargets>(`/management-artifacts/${enc(id)}/targets`, { signal }),
  sync: () => api.post<SyncCounts>('/endpoint-sync', {}),
};
