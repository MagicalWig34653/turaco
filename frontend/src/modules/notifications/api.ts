import { api } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type { AppNotification, EmailPreference, UnreadCount } from './types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

registerErrorMessages({
  'notifications.not_found': 'error.notFound',
  'notifications.unknown_category': 'error.notFound',
});

/** Own notifications only; the server never returns another user's. */
export const notificationsApi = {
  list: (unread: boolean, cursor?: string, signal?: Signal) =>
    api.get<Page<AppNotification>>('/notifications', {
      signal,
      query: { unread, limit: 50, cursor },
    }),
  unreadCount: (signal?: Signal) => api.get<UnreadCount>('/notifications/unread-count', { signal }),
  preferences: (signal?: Signal) =>
    api.get<{ items: EmailPreference[] }>('/notifications/preferences', { signal }),
  setEmailPreference: (category: string, enabled: boolean) =>
    api.put<EmailPreference>(`/notifications/preferences/${enc(category)}/email`, { enabled }),
  markRead: (id: string) => api.post<void>(`/notifications/${enc(id)}/read`),
  markAllRead: () => api.post<{ marked: number }>('/notifications/read-all'),
};

const CHANGED_EVENT = 'turaco:notifications-changed';

/** Tells the unread indicator to reload (after marking notifications read). */
export function announceNotificationsChanged(): void {
  window.dispatchEvent(new Event(CHANGED_EVENT));
}

export function onNotificationsChanged(listener: () => void): () => void {
  window.addEventListener(CHANGED_EVENT, listener);
  return () => window.removeEventListener(CHANGED_EVENT, listener);
}
