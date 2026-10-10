import type { AppNotification } from './types';

/** Categories present in the loaded notifications, sorted, for the category filter. */
export function categoriesIn(items: readonly AppNotification[]): string[] {
  return [...new Set(items.map((item) => item.category))].sort();
}

/** Client-side category filter ('' keeps everything). */
export function filterByCategory(items: AppNotification[], category: string): AppNotification[] {
  return category ? items.filter((item) => item.category === category) : items;
}

/** Only ticket targets are probed on click; a 404 means the record no longer opens. */
export function isProbedLink(notification: AppNotification): boolean {
  return notification.linkType === 'ticket' && Boolean(notification.linkId);
}

export function isTargetGone(error: unknown): boolean {
  return (
    typeof error === 'object' && error !== null && (error as { status?: number }).status === 404
  );
}
