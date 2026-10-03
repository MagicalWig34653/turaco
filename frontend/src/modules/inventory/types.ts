// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export type Warehouse = {
  id: string;
  name: string;
  locationId: string | null;
  active: boolean;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type StorageLocation = {
  id: string;
  warehouseId: string;
  name: string;
  active: boolean;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type Names = {
  products?: Record<string, string>;
  storageLocations?: Record<string, string>;
};

export type Balance = {
  productId: string;
  storageLocationId: string;
  warehouseId: string;
  onHand: number;
  reserved: number;
  available: number;
  updatedAt: string;
};

export const transactionTypes = [
  'goods_receipt',
  'reservation',
  'release',
  'issue',
  'return',
  'transfer',
  'correction',
  'disposal',
] as const;
export type TransactionType = (typeof transactionTypes)[number];

export type InventoryTransaction = {
  id: string;
  type: TransactionType;
  productId: string;
  storageLocationId: string;
  onHandDelta: number;
  reservedDelta: number;
  groupId: string;
  reservationId: string | null;
  contextType: string | null;
  contextId: string | null;
  reason: string | null;
  actorUserId: string | null;
  createdAt: string;
};

export type Reservation = {
  id: string;
  kind: 'quantity' | 'asset';
  productId: string;
  storageLocationId: string | null;
  quantity: number | null;
  assetId: string | null;
  status: 'active' | 'fulfilled' | 'released' | 'expired' | 'cancelled';
  contextType: string | null;
  contextId: string | null;
  reason: string | null;
  closedAt: string | null;
  createdBy: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type GoodsReceiptLine = {
  id: string;
  orderLineId: string;
  productId: string;
  quantity: number;
  storageLocationId: string | null;
  assetIds: string[];
};

export type GoodsReceipt = {
  id: string;
  reference: string;
  orderId: string;
  supplierId: string;
  deliveryNote: string | null;
  receivedBy: string | null;
  createdAt: string;
  lines: GoodsReceiptLine[];
};

export type ReceiptBody = {
  orderId: string;
  deliveryNote?: string;
  assetsAvailable?: boolean;
  lines: Array<{
    orderLineId: string;
    quantity: number;
    storageLocationId?: string;
    units?: Array<{ serialNumber: string; assetTag?: string }>;
  }>;
};

export type StockMoveBody = {
  productId: string;
  storageLocationId?: string;
  fromStorageLocationId?: string;
  toStorageLocationId?: string;
  quantity?: number;
  delta?: number;
  reason?: string;
};
