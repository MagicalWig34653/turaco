import { api } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import { normalizePage } from './helpers';
import type {
  CatalogEntry,
  PackageSyncResult,
  ProductOperation,
  RegisterVersionInput,
  SoftwarePackage,
  SoftwareProduct,
  SoftwareVersion,
  SoftwareVersionDetail,
  VersionOperation,
} from './types';

// The generic endpoints.* codes are registered by the endpoints module; these are the software ones.
registerErrorMessages({
  'endpoints.separation_of_duties': 'software.error.separationOfDuties',
  'endpoints.invalid_transition': 'software.error.invalidTransition',
  'endpoints.software_provider_not_configured': 'software.error.providerNotConfigured',
  'endpoints.software_sync_disabled': 'software.error.syncDisabled',
  'endpoints.product_not_approved': 'software.error.productNotApproved',
  'endpoints.product_blocked': 'software.error.productBlocked',
  'endpoints.version_not_approved': 'software.error.versionNotApproved',
  'endpoints.package_not_ready': 'software.error.packageNotReady',
  'endpoints.hash_mismatch': 'software.error.hashMismatch',
  'endpoints.software_sync_cooldown': 'software.error.syncCooldown',
  'endpoints.software_rate_limited': 'software.error.rateLimited',
});

const enc = encodeURIComponent;
const pageSize = 50;

export const softwareApi = {
  products: async (status: string, cursor?: string, signal?: AbortSignal) =>
    normalizePage(
      await api.get<Page<SoftwareProduct>>('/software/products', {
        query: { status: status || undefined, cursor, limit: pageSize },
        signal,
      }),
    ),
  productOperation: (
    id: string,
    operation: ProductOperation,
    expectedVersion: number,
    reason?: string,
  ) =>
    api.post<SoftwareProduct>(`/software/products/${enc(id)}/${operation}`, {
      expectedVersion,
      ...(reason ? { reason } : {}),
    }),
  versions: async (
    filter: { productId?: string; status?: string },
    cursor?: string,
    signal?: AbortSignal,
  ) =>
    normalizePage(
      await api.get<Page<SoftwareVersion>>('/software/versions', {
        query: {
          productId: filter.productId || undefined,
          status: filter.status || undefined,
          cursor,
          limit: pageSize,
        },
        signal,
      }),
    ),
  register: (input: RegisterVersionInput) => api.post<SoftwareVersion>('/software/versions', input),
  version: (id: string, signal?: AbortSignal) =>
    api.get<SoftwareVersionDetail>(`/software/versions/${enc(id)}`, { signal }),
  versionOperation: (
    id: string,
    operation: VersionOperation,
    expectedVersion: number,
    reason?: string,
  ) =>
    api.post<SoftwareVersion>(`/software/versions/${enc(id)}/${operation}`, {
      expectedVersion,
      ...(reason ? { reason } : {}),
    }),
  package: (id: string, expectedVersion: number) =>
    api.post<SoftwarePackage>(`/software/versions/${enc(id)}/package`, { expectedVersion }),
  catalog: (q: string, signal?: AbortSignal) =>
    api.get<{ items: CatalogEntry[] }>('/software/catalog/search', { query: { q }, signal }),
  packages: async (
    filter: { status?: string; versionId?: string },
    cursor?: string,
    signal?: AbortSignal,
  ) =>
    normalizePage(
      await api.get<Page<SoftwarePackage>>('/software/packages', {
        query: {
          status: filter.status || undefined,
          versionId: filter.versionId || undefined,
          cursor,
          limit: pageSize,
        },
        signal,
      }),
    ),
  publish: (id: string, expectedVersion: number) =>
    api.post<SoftwarePackage>(`/software/packages/${enc(id)}/publish`, { expectedVersion }),
  sync: () => api.post<PackageSyncResult>('/software/packages/sync', {}),
};
