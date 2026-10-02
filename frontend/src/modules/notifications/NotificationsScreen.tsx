import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, usePagedList } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Checkbox } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { announceNotificationsChanged, notificationsApi } from './api';
import { PreferencesPanel } from './PreferencesPanel';
import { notificationLink, notificationText } from './text';
import type { AppNotification } from './types';

export function NotificationsScreen() {
  const { t, locale } = useI18n();
  const [unreadOnly, setUnreadOnly] = useState(false);
  const [actionError, setActionError] = useState<ApiError | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const list = usePagedList(
    (cursor, signal) => notificationsApi.list(unreadOnly, cursor, signal),
    [unreadOnly],
  );

  const run = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setActionError(undefined);
    try {
      await action();
      announceNotificationsChanged();
      list.reload();
    } catch (cause) {
      setActionError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };

  const columns: Column<AppNotification>[] = [
    {
      key: 'text',
      header: t('notifications.col.text'),
      render: (notification) => {
        const text = notificationText(t, notification);
        const link = notificationLink(notification);
        const content = notification.readAt ? text : <strong>{text}</strong>;
        return link ? (
          <Link
            to={link}
            onClick={() =>
              void notificationsApi
                .markRead(notification.id)
                .then(announceNotificationsChanged, () => undefined)
            }
          >
            {content}
          </Link>
        ) : (
          content
        );
      },
    },
    {
      key: 'time',
      header: t('notifications.col.time'),
      render: (notification) => formatDateTime(locale, notification.createdAt),
    },
    {
      key: 'state',
      header: t('notifications.col.state'),
      render: (notification) =>
        notification.readAt ? (
          t('notifications.read')
        ) : (
          <Badge tone="info">{t('notifications.unread')}</Badge>
        ),
    },
    {
      key: 'action',
      header: t('notifications.col.action'),
      render: (notification) =>
        notification.readAt ? null : (
          <Button
            disabled={busy}
            onClick={() => void run(() => notificationsApi.markRead(notification.id))}
          >
            {t('notifications.markRead')}
          </Button>
        ),
    },
  ];

  return (
    <>
      <PageHeader
        title={t('nav.notifications')}
        intro={t('notifications.intro')}
        actions={
          <Button busy={busy} onClick={() => void run(() => notificationsApi.markAllRead())}>
            {t('notifications.markAllRead')}
          </Button>
        }
      />
      <div className="filters">
        <Checkbox
          label={t('notifications.unreadOnly')}
          checked={unreadOnly}
          onChange={(event) => setUnreadOnly(event.target.checked)}
        />
      </div>
      {actionError ? <ApiErrorAlert error={actionError} /> : null}
      <DataTable
        caption={t('nav.notifications')}
        columns={columns}
        rows={list.items}
        rowKey={(notification) => notification.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('notifications.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
      <PreferencesPanel />
    </>
  );
}
