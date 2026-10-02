import type { MessageKey } from '../../platform/i18n/i18n';
import type { AppNotification } from './types';

type Translate = (key: MessageKey, params?: Record<string, string | number>) => string;

export type NotificationCategoryInfo = {
  /** Message key of the notification text; receives { title }. */
  textKey: MessageKey;
  /** Message key of the label in the email preferences. */
  labelKey: MessageKey;
};

const categories = new Map<string, NotificationCategoryInfo>();
const linkTypes = new Map<string, (id: string) => string>();

/** Lets a module describe its notification categories (call from the app composition root). */
export function registerNotificationCategories(
  entries: Record<string, NotificationCategoryInfo>,
): void {
  for (const [name, info] of Object.entries(entries)) categories.set(name, info);
}

/** Lets a module say where notifications that link to its records point. */
export function registerNotificationLink(linkType: string, path: (id: string) => string): void {
  linkTypes.set(linkType, path);
}

/** Localized text of a notification; unknown categories get a generic text. */
export function notificationText(t: Translate, notification: AppNotification): string {
  const title = typeof notification.params.title === 'string' ? notification.params.title : '';
  const info = categories.get(notification.category);
  return info ? t(info.textKey, { title }) : t('notifications.generic');
}

/** In-app link of a notification, or undefined when it has no known target. */
export function notificationLink(notification: AppNotification): string | undefined {
  if (!notification.linkType || !notification.linkId) return undefined;
  return linkTypes.get(notification.linkType)?.(notification.linkId);
}

/** Counter label: the server caps the count at `max`, which is shown as "99+"-style overflow. */
export function unreadLabel(count: number, max: number): string {
  return count >= max ? `${max - 1}+` : String(count);
}

/** Label of a notification category in the preferences list; unknown ones show their key. */
export function categoryLabel(t: Translate, category: string): string {
  const info = categories.get(category);
  return info ? t(info.labelKey) : category;
}
