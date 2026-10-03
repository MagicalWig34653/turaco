import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type {
  Manufacturer,
  Product,
  ProductBody,
  ProductCategory,
  ProductFilter,
  ProductUpdateBody,
} from './types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

registerErrorMessages({
  'products.conflict': 'error.productsConflict',
  'products.version_conflict': 'error.versionConflict',
  'products.invalid_reference': 'error.productsInvalidReference',
  'products.not_found': 'error.notFound',
});
registerErrorResolver((error) =>
  error.code.startsWith('products.invalid_') ? 'error.invalidRequest' : undefined,
);

export const productsApi = {
  products: (filter: ProductFilter, cursor?: string, signal?: Signal) =>
    api.get<Page<Product>>('/products', { signal, query: { ...filter, limit: 50, cursor } }),
  createProduct: (body: ProductBody) => api.post<Product>('/products', body),
  updateProduct: (id: string, body: ProductUpdateBody) =>
    api.patch<Product>(`/products/${enc(id)}`, body),
  setProductActive: (id: string, active: boolean, expectedVersion: number) =>
    api.post<Product>(`/products/${enc(id)}/${active ? 'activate' : 'deactivate'}`, {
      expectedVersion,
    }),

  manufacturers: (cursor?: string, signal?: Signal) =>
    api.get<Page<Manufacturer>>('/manufacturers', { signal, query: { limit: 200, cursor } }),
  createManufacturer: (name: string) => api.post<Manufacturer>('/manufacturers', { name }),
  renameManufacturer: (id: string, name: string, expectedVersion: number) =>
    api.patch<Manufacturer>(`/manufacturers/${enc(id)}`, { name, expectedVersion }),

  categories: (cursor?: string, signal?: Signal) =>
    api.get<Page<ProductCategory>>('/product-categories', {
      signal,
      query: { limit: 200, cursor },
    }),
  createCategory: (name: string, parentId?: string) =>
    api.post<ProductCategory>('/product-categories', { name, ...(parentId ? { parentId } : {}) }),
  renameCategory: (id: string, name: string, expectedVersion: number) =>
    api.patch<ProductCategory>(`/product-categories/${enc(id)}`, { name, expectedVersion }),
};
