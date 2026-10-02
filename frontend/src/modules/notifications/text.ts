import type { MessageKey } from '../../platform/i18n/i18n';
import type { AppNotification } from './types';

type Translate = (key: MessageKey, params?: Record<string, string | number>) => string;

/** Localized text of a notification; unknown categories get a generic text. */
export function notificationText(t: Translate, notification: AppNotification): string {
  const title = typeof notification.params.title === 'string' ? notification.params.title : '';
  switch (notification.category) {
    case 'task.assigned':
      return t('notifications.task.assigned', { title });
    case 'task.completed':
      return t('notifications.task.completed', { title });
    default:
      return t('notifications.generic');
  }
}

/** In-app link of a notification, or undefined when it has no known target. */
export function notificationLink(notification: AppNotification): string | undefined {
  if (notification.linkType === 'task' && notification.linkId) {
    return `/tasks/${encodeURIComponent(notification.linkId)}`;
  }
  return undefined;
}

/** Counter label: the server caps the count at `max`, which is shown as "99+"-style overflow. */
export function unreadLabel(count: number, max: number): string {
  return count >= max ? `${max - 1}+` : String(count);
}

/** Label of a notification category in the preferences list; unknown ones show their key. */
export function categoryLabel(t: Translate, category: string): string {
  switch (category) {
    case 'task.assigned':
      return t('notifications.category.task.assigned');
    case 'task.completed':
      return t('notifications.category.task.completed');
    default:
      return category;
  }
}
