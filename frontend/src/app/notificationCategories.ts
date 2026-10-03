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
  registerNotificationLink('approval', (id) => `/approvals/${encodeURIComponent(id)}`);
  registerNotificationLink('service_request', (id) => `/requests/${encodeURIComponent(id)}`);
  registerNotificationLink('task', (id) => `/tasks/${encodeURIComponent(id)}`);
}
