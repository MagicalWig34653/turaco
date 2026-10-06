import { TableDate } from '../../platform/ui/TableDate';
import { FilterBar } from '../../platform/ui/FilterBar';
import { useState } from 'react';
import { usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { Badge } from '../../platform/ui/Alert';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Select } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { requestsApi } from './api';
import { requestStatuses, type RequestStatus, type ServiceRequest } from './types';

const tone: Record<RequestStatus, 'neutral' | 'success' | 'warning' | 'danger' | 'info'> = {
  pending_approval: 'warning',
  in_fulfillment: 'info',
  waiting: 'warning',
  completed: 'success',
  rejected: 'danger',
  cancelled: 'neutral',
};

export function RequestStatusBadge({ status }: { status: RequestStatus }) {
  const { t } = useI18n();
  return <Badge tone={tone[status]}>{t(`requests.status.${status}`)}</Badge>;
}

/** The own requests of the signed-in user (scope "mine") or all requests of the organization. */
export function RequestsScreen({ scope }: { scope: 'mine' | 'all' }) {
  const { t } = useI18n();
  const [status, setStatus] = useState<RequestStatus | ''>('');
  const list = usePagedList(
    (cursor, signal) => requestsApi.list(scope, status, cursor, signal),
    [scope, status],
  );
  const title = t(scope === 'mine' ? 'nav.myRequests' : 'nav.allRequests');
  const columns: Column<ServiceRequest>[] = [
    {
      key: 'reference',
      sortValue: (r) => r.reference,
      header: t('requests.col.reference'),
      render: (r) => <Link to={`/requests/${encodeURIComponent(r.id)}`}>{r.reference}</Link>,
    },
    {
      key: 'item',
      sortValue: (r) => r.catalogItemTitle,
      header: t('requests.col.item'),
      render: (r) => r.catalogItemTitle,
    },
    {
      key: 'status',
      sortValue: (r) => r.status,
      header: t('requests.col.status'),
      render: (r) => <RequestStatusBadge status={r.status} />,
    },
    {
      key: 'submitted',
      sortValue: (r) => r.submittedAt,
      header: t('requests.col.submitted'),
      render: (r) => <TableDate value={r.submittedAt} />,
    },
  ];
  const activeFilters = [
    ...(status
      ? [
          {
            key: 'status',
            label: t(`requests.status.${status}`),
            onRemove: () => {
              setStatus('');
            },
          },
        ]
      : []),
  ];

  return (
    <>
      <PageHeader
        title={title}
        intro={t(scope === 'mine' ? 'requests.mine.intro' : 'requests.all.intro')}
        actions={
          scope === 'mine' ? (
            <Link to="/catalog" className="btn btn-primary">
              {t('nav.catalog')}
            </Link>
          ) : null
        }
      />
      <FilterBar
        activeFilters={activeFilters}
        role="search"
        onSubmit={(event) => event.preventDefault()}
      >
        <Select
          label={t('requests.filter.status')}
          value={status}
          onChange={(event) => setStatus(event.target.value as RequestStatus | '')}
          options={[
            { value: '', label: t('requests.filter.any') },
            ...requestStatuses.map((value) => ({ value, label: t(`requests.status.${value}`) })),
          ]}
        />
      </FilterBar>
      <DataTable
        filterSummary={activeFilters.map((filter) => filter.label).join(' · ')}
        caption={title}
        columns={columns}
        rows={list.items}
        rowKey={(r) => r.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('requests.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </>
  );
}
