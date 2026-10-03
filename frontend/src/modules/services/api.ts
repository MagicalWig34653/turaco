import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type {
  Service,
  ServiceDetail,
  ServiceLink,
  ServiceList,
  ServiceLinkList,
  ServiceStatus,
  Criticality,
  TargetType,
  Impact,
} from './types';
const enc = encodeURIComponent;
registerErrorMessages({
  'services.not_found': 'error.notFound',
  'services.conflict': 'services.error.conflict',
  'services.version_conflict': 'error.versionConflict',
  'services.retired': 'services.error.retired',
  'services.dependency_cycle': 'services.error.cycle',
  'services.dependency_graph_too_large': 'services.error.graph',
  'services.invalid_reference': 'services.error.reference',
  'services.invalid_cursor': 'error.invalidRequest',
  'services.invalid_limit': 'error.invalidRequest',
  'services.invalid_request': 'error.invalidRequest',
  'services.busy': 'services.error.busy',
  'services.impact_busy': 'services.error.impactBusy',
});
registerErrorResolver((error) =>
  error.code.startsWith('services.invalid_') ? 'error.invalidRequest' : undefined,
);
export type ServiceFields = {
  name: string;
  description: string;
  ownerUserId: string;
  ownerTeamId: string;
  supportTeamId: string;
  criticality: Criticality;
};
export const servicesApi = {
  list: (
    filter: {
      status?: string;
      criticality?: string;
      ownerUserId?: string;
      teamId?: string;
      q?: string;
    },
    cursor?: string,
    signal?: AbortSignal,
  ) => api.get<ServiceList>('/services', { query: { ...filter, cursor, limit: 50 }, signal }),
  get: (id: string, signal?: AbortSignal) =>
    api.get<ServiceDetail>(`/services/${enc(id)}`, { signal }),
  dependencies: (id: string, direction: 'out' | 'in', cursor: string, signal?: AbortSignal) =>
    api.get<ServiceLinkList>(`/services/${enc(id)}/dependencies`, {
      query: { direction, cursor, limit: 200 },
      signal,
    }),
  create: (body: ServiceFields & { status: 'operational' | 'planned' }) =>
    api.post<Service>('/services', { body }),
  update: (id: string, body: ServiceFields & { expectedVersion: number }) =>
    api.patch<Service>(`/services/${enc(id)}`, { body }),
  status: (
    id: string,
    status: Exclude<ServiceStatus, 'retired'>,
    reason: string,
    expectedVersion: number,
  ) =>
    api.post<Service>(`/services/${enc(id)}/status`, { body: { status, reason, expectedVersion } }),
  retire: (id: string, reason: string, expectedVersion: number) =>
    api.post<Service>(`/services/${enc(id)}/retire`, { body: { reason, expectedVersion } }),
  addDependency: (id: string, targetType: TargetType, targetId: string) =>
    api.post<ServiceLink>(`/services/${enc(id)}/dependencies`, { body: { targetType, targetId } }),
  removeDependency: (id: string, dependencyId: string, reason: string) =>
    api.delete<void>(`/services/${enc(id)}/dependencies/${enc(dependencyId)}`, {
      query: { reason },
    }),
  impact: (
    type: TargetType,
    id: string,
    direction: 'downstream' | 'upstream',
    depth: number,
    signal?: AbortSignal,
  ) => api.get<Impact>('/impact', { query: { type, id, direction, depth }, signal }),
};
