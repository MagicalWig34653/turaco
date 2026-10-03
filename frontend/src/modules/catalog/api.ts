import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type { CatalogForm, CatalogItem, CatalogItemCreate, CatalogItemUpdate } from './types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

registerErrorMessages({
  'catalog.conflict': 'error.catalogConflict',
  'catalog.version_conflict': 'error.versionConflict',
  'catalog.invalid_reference': 'error.catalogInvalidReference',
  'catalog.not_found': 'error.notFound',
});
registerErrorResolver((error) =>
  error.code.startsWith('catalog.invalid_') ? 'error.invalidRequest' : undefined,
);

export const catalogApi = {
  list: (status: 'active' | 'inactive' | '', cursor?: string, signal?: Signal) =>
    api.get<Page<CatalogItem>>('/catalog-items', { signal, query: { status, limit: 50, cursor } }),
  form: (id: string, signal?: Signal) =>
    api.get<CatalogForm>(`/catalog-items/${enc(id)}`, { signal }),
  create: (body: CatalogItemCreate) => api.post<CatalogItem>('/catalog-items', body),
  update: (id: string, body: CatalogItemUpdate) =>
    api.patch<CatalogItem>(`/catalog-items/${enc(id)}`, body),
  setActive: (id: string, active: boolean, expectedVersion: number) =>
    api.post<CatalogItem>(`/catalog-items/${enc(id)}/${active ? 'activate' : 'deactivate'}`, {
      expectedVersion,
    }),
};
