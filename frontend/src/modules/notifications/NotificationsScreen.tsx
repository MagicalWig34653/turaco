import { TableDate } from '../../platform/ui/TableDate';
import { FilterBar } from '../../platform/ui/FilterBar';
import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../platform/router/Router';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Checkbox, Select } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { announceNotificationsChanged, notificationsApi } from './api';
import { ticketsApi } from '../tickets/api';
import { categoriesIn, filterByCategory, isProbedLink, isTargetGone } from './notificationModel';
import { PreferencesPanel } from './PreferencesPanel';
import { categoryLabel, notificationLink, notificationText } from './text';
import type { AppNotification } from './types';

export function NotificationsScreen() {
  const { t } = useI18n();
  const [unreadOnly, setUnreadOnly] = useState(false);
  const [category, setCategory] = useState('');
  const [gone, setGone] = useState<ReadonlySet<string>>(new Set());
  const [actionError, setActionError] = useState<ApiError | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const list = usePagedList(
    (cursor, signal) => notificationsApi.list(unreadOnly, cursor, signal),
    [unreadOnly],
  );

  const visibleItems = filterByCategory(list.items, category);

  // A target that no longer opens (deleted ticket) degrades to plain text instead of a dead link.
  const open = async (notification: AppNotification, path: string) => {
    void notificationsApi
      .markRead(notification.id)
      .then(announceNotificationsChanged, () => undefined);
    if (isProbedLink(notification) && notification.linkId) {
      try {
        await ticketsApi.get(notification.linkId);
      } catch (cause) {
        if (isTargetGone(cause)) {
          setGone((current) => new Set(current).add(notification.id));
          return;
        }
      }
    }
    navigate(path);
  };

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
        return link && !gone.has(notification.id) ? (
          <Link
            to={link}
            onClick={(event) => {
              if (!isProbedLink(notification)) {
                void notificationsApi
                  .markRead(notification.id)
                  .then(announceNotificationsChanged, () => undefined);
                return;
              }
              event.preventDefault();
              void open(notification, link);
            }}
          >
            {content}
          </Link>
        ) : (
          <>
            {content}
            {gone.has(notification.id) ? (
              <small className="field-hint"> {t('notifications.targetGone')}</small>
            ) : null}
          </>
        );
      },
    },
    {
      key: 'time',
      header: t('notifications.col.time'),
      render: (notification) => <TableDate value={notification.createdAt} />,
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

  const activeFilters = [
    ...(category
      ? [
          {
            key: 'category',
            label: categoryLabel(t, category),
            onRemove: () => setCategory(''),
          },
        ]
      : []),
    ...(unreadOnly
      ? [
          {
            key: 'unread',
            label: t('notifications.unreadOnly'),
            onRemove: () => {
              setUnreadOnly(false);
            },
          },
        ]
      : []),
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
      <FilterBar activeFilters={activeFilters}>
        <Select
          label={t('notifications.category')}
          value={category}
          onChange={(event) => setCategory(event.target.value)}
          options={[
            { value: '', label: t('notifications.category.any') },
            ...categoriesIn(list.items).map((value) => ({
              value,
              label: categoryLabel(t, value),
            })),
          ]}
        />
        <Checkbox
          label={t('notifications.unreadOnly')}
          checked={unreadOnly}
          onChange={(event) => setUnreadOnly(event.target.checked)}
        />
      </FilterBar>
      {actionError ? <ApiErrorAlert error={actionError} /> : null}
      <DataTable
        filterSummary={activeFilters.map((filter) => filter.label).join(' · ')}
        caption={t('nav.notifications')}
        columns={columns}
        rows={visibleItems}
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
