// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export type Supplier = {
  id: string;
  name: string;
  accountReference: string | null;
  active: boolean;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export const needStatuses = ['open', 'ordered', 'fulfilled', 'cancelled'] as const;
export type NeedStatus = (typeof needStatuses)[number];

export type ProcurementRequest = {
  id: string;
  reference: string;
  productId: string;
  quantity: number;
  status: NeedStatus;
  statusReason: string | null;
  contextType: string | null;
  contextId: string | null;
  notes: string | null;
  requestedBy: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export const orderStatuses = [
  'draft',
  'pending_approval',
  'approved',
  'sent',
  'acknowledged',
  'partially_received',
  'received',
  'closed',
  'cancelled',
] as const;
export type OrderStatus = (typeof orderStatuses)[number];

export type PurchaseOrder = {
  id: string;
  reference: string;
  supplierId: string;
  status: OrderStatus;
  statusReason: string | null;
  currency: string;
  notes: string | null;
  createdBy: string | null;
  sentAt: string | null;
  closedAt: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type OrderLine = {
  id: string;
  lineNo: number;
  productId: string;
  quantity: number;
  unitPriceCents: number;
  receivedQuantity: number;
  requestId: string | null;
};

export type PurchaseOrderDetail = PurchaseOrder & {
  lines: OrderLine[];
  totalCents: number;
  supplierName: string;
  productNames: Record<string, string>;
  productTracking: Record<string, 'asset' | 'stock' | 'none'>;
  allowedOperations: string[];
  linesEditable: boolean;
};

export type Names = { products?: Record<string, string>; suppliers?: Record<string, string> };
