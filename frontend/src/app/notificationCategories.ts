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
  });
  registerNotificationLink('task', (id) => `/tasks/${encodeURIComponent(id)}`);
}
