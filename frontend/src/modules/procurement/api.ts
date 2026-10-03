import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type {
  Names,
  ProcurementRequest,
  PurchaseOrder,
  PurchaseOrderDetail,
  OrderLine,
  Supplier,
} from './types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;
type Named<T> = Page<T> & { names?: Names };

registerErrorMessages({
  'procurement.conflict': 'error.procurementConflict',
  'procurement.version_conflict': 'error.versionConflict',
  'procurement.invalid_transition': 'error.invalidTransition',
  'procurement.request_unavailable': 'error.procurementRequestUnavailable',
  'procurement.no_eligible_approver': 'error.procurementNoApprover',
  'procurement.product_invalid': 'error.procurementProductInvalid',
  'procurement.supplier_invalid': 'error.procurementSupplierInvalid',
  'procurement.not_found': 'error.notFound',
});
registerErrorResolver((error) =>
  error.code.startsWith('procurement.invalid_') ? 'error.invalidRequest' : undefined,
);

export const procurementApi = {
  suppliers: (includeInactive: boolean, q = '', cursor?: string, signal?: Signal) =>
    api.get<Page<Supplier>>('/suppliers', {
      signal,
      query: { includeInactive, q, limit: 100, cursor },
    }),
  createSupplier: (name: string, accountReference: string) =>
    api.post<Supplier>('/suppliers', { name, accountReference }),
  updateSupplier: (
    id: string,
    body: { expectedVersion: number; name?: string; accountReference?: string },
  ) => api.patch<Supplier>(`/suppliers/${enc(id)}`, body),
  setSupplierActive: (id: string, active: boolean, expectedVersion: number) =>
    api.post<Supplier>(`/suppliers/${enc(id)}/${active ? 'activate' : 'deactivate'}`, {
      expectedVersion,
    }),

  requests: (status: string, cursor?: string, signal?: Signal) =>
    api.get<Named<ProcurementRequest>>('/procurement-requests', {
      signal,
      query: { status, limit: 50, cursor },
    }),
  createRequest: (body: { productId: string; quantity: number; notes?: string }) =>
    api.post<ProcurementRequest>('/procurement-requests', body),
  cancelRequest: (id: string, reason: string, expectedVersion: number) =>
    api.post<ProcurementRequest>(`/procurement-requests/${enc(id)}/cancel`, {
      reason,
      expectedVersion,
    }),

  orders: (status: string, cursor?: string, signal?: Signal) =>
    api.get<Named<PurchaseOrder>>('/purchase-orders', {
      signal,
      query: { status, limit: 50, cursor },
    }),
  order: (id: string, signal?: Signal) =>
    api.get<PurchaseOrderDetail>(`/purchase-orders/${enc(id)}`, { signal }),
  createOrder: (body: { supplierId: string; currency?: string; notes?: string }) =>
    api.post<PurchaseOrder>('/purchase-orders', body),
  updateOrder: (
    id: string,
    body: { expectedVersion: number; supplierId?: string; currency?: string; notes?: string },
  ) => api.patch<PurchaseOrder>(`/purchase-orders/${enc(id)}`, body),
  addLine: (
    id: string,
    body: { productId: string; quantity?: number; unitPriceCents: number; requestId?: string },
  ) => api.post<OrderLine>(`/purchase-orders/${enc(id)}/lines`, body),
  updateLine: (id: string, lineId: string, body: { quantity: number; unitPriceCents: number }) =>
    api.patch<OrderLine>(`/purchase-orders/${enc(id)}/lines/${enc(lineId)}`, body),
  removeLine: (id: string, lineId: string) =>
    api.delete<void>(`/purchase-orders/${enc(id)}/lines/${enc(lineId)}`),
  submit: (
    id: string,
    expectedVersion: number,
    approver: { approverUserId?: string; approverTeamId?: string },
  ) =>
    api.post<PurchaseOrder>(`/purchase-orders/${enc(id)}/submit`, { expectedVersion, ...approver }),
  operate: (
    id: string,
    op: 'send' | 'acknowledge' | 'cancel' | 'close',
    expectedVersion: number,
    reason = '',
  ) => api.post<PurchaseOrder>(`/purchase-orders/${enc(id)}/${op}`, { expectedVersion, reason }),
};
