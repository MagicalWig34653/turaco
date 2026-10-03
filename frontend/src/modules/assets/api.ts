import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type {
  Asset,
  AssetCreate,
  AssetDetail,
  AssetList,
  AssetOperation,
  AssetStatus,
  AssetUpdate,
  ProvisioningStatus,
} from './types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

registerErrorMessages({
  'assets.conflict': 'error.assetsConflict',
  'assets.version_conflict': 'error.versionConflict',
  'assets.invalid_transition': 'error.assetsInvalidTransition',
  'assets.assignee_invalid': 'error.assetsAssigneeInvalid',
  'assets.product_invalid': 'error.assetsProductInvalid',
  'assets.invalid_reference': 'error.assetsInvalidReference',
  'assets.not_found': 'error.notFound',
});
registerErrorResolver((error) =>
  error.code.startsWith('assets.invalid_') ? 'error.invalidRequest' : undefined,
);

const segment = (op: AssetOperation) => op.replaceAll('_', '-');

export const assetsApi = {
  list: (
    filter: { status?: AssetStatus | ''; q?: string; assigneeId?: string },
    cursor?: string,
    signal?: Signal,
  ) => api.get<AssetList>('/assets', { signal, query: { ...filter, limit: 50, cursor } }),
  mine: (cursor?: string, signal?: Signal) =>
    api.get<AssetList>('/my-assets', { signal, query: { limit: 50, cursor } }),
  get: (id: string, signal?: Signal) => api.get<AssetDetail>(`/assets/${enc(id)}`, { signal }),
  lookup: (code: string, signal?: Signal) =>
    api.get<Asset>('/assets/lookup', { signal, query: { code } }),
  create: (body: AssetCreate) => api.post<Asset>('/assets', body),
  update: (id: string, body: AssetUpdate) => api.patch<Asset>(`/assets/${enc(id)}`, body),
  setProvisioning: (id: string, status: ProvisioningStatus, expectedVersion: number) =>
    api.post<Asset>(`/assets/${enc(id)}/provisioning`, { status, expectedVersion }),
  operate: (
    id: string,
    op: AssetOperation,
    body: {
      expectedVersion: number;
      reason?: string;
      note?: string;
      assigneeType?: string;
      assigneeId?: string;
    },
  ) => api.post<Asset>(`/assets/${enc(id)}/${segment(op)}`, body),
};
