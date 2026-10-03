import {
  registerNotificationCategories,
  registerNotificationLink,
} from '../modules/notifications/text';

/**
 * Notification categories and link targets of the modules, registered once at start-up. The
 * notifications module itself knows none of its producers.
 */
export function registerModuleNotifications(): void {
  registerNotificationCategories({
    'task.assigned': {
      textKey: 'notifications.task.assigned',
      labelKey: 'notifications.category.task.assigned',
    },
    'task.completed': {
      textKey: 'notifications.task.completed',
      labelKey: 'notifications.category.task.completed',
    },
    'ticket.assigned': {
      textKey: 'notifications.ticket.assigned',
      labelKey: 'notifications.category.ticket.assigned',
    },
    'ticket.comment': {
      textKey: 'notifications.ticket.comment',
      labelKey: 'notifications.category.ticket.comment',
    },
    'ticket.resolved': {
      textKey: 'notifications.ticket.resolved',
      labelKey: 'notifications.category.ticket.resolved',
    },
    'asset.assigned': {
      textKey: 'notifications.asset.assigned',
      labelKey: 'notifications.category.asset.assigned',
    },
    'approval.requested': {
      textKey: 'notifications.approval.requested',
      labelKey: 'notifications.category.approval.requested',
    },
    'request.approved': {
      textKey: 'notifications.request.approved',
      labelKey: 'notifications.category.request.approved',
    },
    'request.rejected': {
      textKey: 'notifications.request.rejected',
      labelKey: 'notifications.category.request.rejected',
    },
    'request.completed': {
      textKey: 'notifications.request.completed',
      labelKey: 'notifications.category.request.completed',
    },
  });
  registerNotificationLink('ticket', (id) => `/support/${encodeURIComponent(id)}`);
  registerNotificationLink('asset', (id) => `/assets/${encodeURIComponent(id)}`);
  registerNotificationLink('approval', (id) => `/approvals/${encodeURIComponent(id)}`);
  registerNotificationLink('service_request', (id) => `/requests/${encodeURIComponent(id)}`);
  registerNotificationLink('task', (id) => `/tasks/${encodeURIComponent(id)}`);
}
