import type { OrderStatus } from './types';

export const orderTone: Record<OrderStatus, 'neutral' | 'success' | 'warning' | 'danger' | 'info'> =
  {
    draft: 'neutral',
    pending_approval: 'warning',
    approved: 'info',
    sent: 'info',
    acknowledged: 'info',
    partially_received: 'warning',
    received: 'success',
    closed: 'neutral',
    cancelled: 'danger',
  };
