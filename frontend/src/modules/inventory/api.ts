import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type {
  Balance,
  GoodsReceipt,
  InventoryTransaction,
  Names,
  ReceiptBody,
  Reservation,
  StockMoveBody,
  StorageLocation,
  Warehouse,
} from './types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;
type Named<T> = Page<T> & { names?: Names };

registerErrorMessages({
  'inventory.conflict': 'error.inventoryConflict',
  'inventory.version_conflict': 'error.versionConflict',
  'inventory.insufficient_stock': 'error.inventoryInsufficientStock',
  'inventory.asset_unavailable': 'error.inventoryAssetUnavailable',
  'inventory.invalid_transition': 'error.invalidTransition',
  'inventory.product_invalid': 'error.inventoryProductInvalid',
  'inventory.location_invalid': 'error.inventoryLocationInvalid',
  'inventory.assignee_invalid': 'error.assetsAssigneeInvalid',
  'inventory.duplicate_asset': 'error.inventoryDuplicateAsset',
  'inventory.order_not_receivable': 'error.inventoryOrderNotReceivable',
  'inventory.over_receipt': 'error.inventoryOverReceipt',
  'inventory.not_found': 'error.notFound',
});
registerErrorResolver((error) =>
  error.code.startsWith('inventory.invalid_') ? 'error.invalidRequest' : undefined,
);

export const inventoryApi = {
  warehouses: (includeInactive: boolean, signal?: Signal) =>
    api.get<Page<Warehouse>>('/warehouses', { signal, query: { limit: 200, includeInactive } }),
  createWarehouse: (name: string) => api.post<Warehouse>('/warehouses', { name }),
  renameWarehouse: (id: string, name: string, expectedVersion: number) =>
    api.patch<Warehouse>(`/warehouses/${enc(id)}`, { name, expectedVersion }),
  setWarehouseActive: (id: string, active: boolean, expectedVersion: number) =>
    api.post<Warehouse>(`/warehouses/${enc(id)}/${active ? 'activate' : 'deactivate'}`, {
      expectedVersion,
    }),
  locations: (warehouseId: string, includeInactive: boolean, signal?: Signal) =>
    api.get<Page<StorageLocation>>(`/warehouses/${enc(warehouseId)}/storage-locations`, {
      signal,
      query: { limit: 200, includeInactive },
    }),
  createLocation: (warehouseId: string, name: string) =>
    api.post<StorageLocation>(`/warehouses/${enc(warehouseId)}/storage-locations`, { name }),
  renameLocation: (id: string, name: string, expectedVersion: number) =>
    api.patch<StorageLocation>(`/storage-locations/${enc(id)}`, { name, expectedVersion }),
  setLocationActive: (id: string, active: boolean, expectedVersion: number) =>
    api.post<StorageLocation>(
      `/storage-locations/${enc(id)}/${active ? 'activate' : 'deactivate'}`,
      {
        expectedVersion,
      },
    ),

  stock: (
    filter: { productId?: string; withStockOnly?: boolean },
    cursor?: string,
    signal?: Signal,
  ) => api.get<Named<Balance>>('/stock', { signal, query: { ...filter, limit: 50, cursor } }),
  transactions: (filter: { productId?: string; type?: string }, cursor?: string, signal?: Signal) =>
    api.get<Named<InventoryTransaction>>('/inventory-transactions', {
      signal,
      query: { ...filter, limit: 50, cursor },
    }),
  moveStock: (op: 'issue' | 'return' | 'dispose' | 'transfer' | 'correct', body: StockMoveBody) =>
    api.post<{ items: InventoryTransaction[] }>(`/stock/${op}`, body),

  reservations: (status: string, cursor?: string, signal?: Signal) =>
    api.get<Named<Reservation>>('/reservations', { signal, query: { status, limit: 50, cursor } }),
  reserveQuantity: (productId: string, storageLocationId: string, quantity: number) =>
    api.post<Reservation>('/reservations', {
      kind: 'quantity',
      productId,
      storageLocationId,
      quantity,
    }),
  reserveAsset: (assetId: string) =>
    api.post<Reservation>('/reservations', { kind: 'asset', assetId }),
  release: (id: string, expectedVersion: number, reason: string) =>
    api.post<Reservation>(`/reservations/${enc(id)}/release`, { expectedVersion, reason }),
  fulfill: (
    id: string,
    expectedVersion: number,
    body: { note?: string; assigneeType?: string; assigneeId?: string },
  ) => api.post<Reservation>(`/reservations/${enc(id)}/fulfill`, { expectedVersion, ...body }),

  receipts: (cursor?: string, signal?: Signal) =>
    api.get<Named<GoodsReceipt>>('/goods-receipts', { signal, query: { limit: 50, cursor } }),
  postReceipt: (body: ReceiptBody) => api.post<GoodsReceipt>('/goods-receipts', body),
};
