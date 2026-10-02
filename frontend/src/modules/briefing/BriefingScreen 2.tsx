import { useState } from 'react';
import { usePagedList } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Select } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { briefingApi } from './api';
import { severityTone } from './actions';
import { briefingStatuses, type BriefingItem, type BriefingStatus } from './types';

export function SeverityBadge({ severity }: { severity: BriefingItem['severity'] }) {
  const { t } = useI18n();
  return <Badge tone={severityTone(severity)}>{t(`briefing.severity.${severity}`)}</Badge>;
}

/** Published briefing items for everybody with briefing.view; managers also see and filter all items. */
export function BriefingScreen() {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const manage = can('briefing.manage');
  const [status, setStatus] = useState<BriefingStatus | ''>('');
  const list = usePagedList(
    (cursor, signal) => briefingApi.list(manage ? status : '', cursor, signal),
    [manage, status],
  );

  const columns: Column<BriefingItem>[] = [
    {
      key: 'title',
      header: t('tasks.col.title'),
      render: (item) => <Link to={`/briefing/${encodeURIComponent(item.id)}`}>{item.title}</Link>,
    },
    {
      key: 'severity',
      header: t('briefing.col.severity'),
      render: (item) => <SeverityBadge severity={item.severity} />,
    },
    ...(manage
      ? [
          {
            key: 'status',
            header: t('tasks.col.status'),
            render: (item: BriefingItem) => t(`briefing.status.${item.status}`),
          },
        ]
      : []),
    {
      key: 'published',
      header: t('briefing.col.published'),
      render: (item) => (item.publishedAt ? formatDateTime(locale, item.publishedAt) : '–'),
    },
    {
      key: 'valid',
      header: t('briefing.col.validUntil'),
      render: (item) => (item.validUntil ? formatDateTime(locale, item.validUntil) : '–'),
    },
  ];

  return (
    <>
      <PageHeader
        title={t('nav.briefing')}
        intro={t('briefing.intro')}
        actions={
          manage ? (
            <Link to="/briefing/new" className="btn btn-primary">
              {t('briefing.create.action')}
            </Link>
          ) : null
        }
      />
      {manage ? (
        <div className="filters">
          <Select
            label={t('tasks.col.status')}
            value={status}
            onChange={(event) => setStatus(event.target.value as BriefingStatus | '')}
            options={[
              { value: '', label: t('tasks.filter.anyStatus') },
              ...briefingStatuses.map((value) => ({ value, label: t(`briefing.status.${value}`) })),
            ]}
          />
        </div>
      ) : null}
      <DataTable
        caption={t('nav.briefing')}
        columns={columns}
        rows={list.items}
        rowKey={(item) => item.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('briefing.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </>
  );
}
