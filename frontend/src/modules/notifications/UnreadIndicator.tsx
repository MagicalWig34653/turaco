import { useEffect } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { notificationsApi, onNotificationsChanged } from './api';
import { unreadLabel } from './text';

const POLL_MS = 60_000;

/**
 * Unread counter next to the Notifications navigation entry. It polls (SSE is not needed yet),
 * reloads when the window regains focus and after notifications were marked read. A failed load
 * simply shows nothing.
 */
export function UnreadIndicator() {
  const { t } = useI18n();
  const count = useAsync((signal) => notificationsApi.unreadCount(signal), []);
  const reload = count.reload;

  useEffect(() => {
    const timer = window.setInterval(reload, POLL_MS);
    const onFocus = () => reload();
    window.addEventListener('focus', onFocus);
    const off = onNotificationsChanged(reload);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener('focus', onFocus);
      off();
    };
  }, [reload]);

  const value = count.data;
  if (!value || value.count === 0) return null;
  const label = unreadLabel(value.count, value.max);
  return (
    <span
      className="badge badge-info nav-count"
      aria-label={t('notifications.unreadCount', { count: label })}
    >
      {label}
    </span>
  );
}
