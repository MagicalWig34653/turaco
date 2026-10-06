import { FilterBar } from '../../platform/ui/FilterBar';
import { useState } from 'react';
import { usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, useLocation } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Checkbox } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { runbooksApi, type Runbook } from './api';

export function RunbooksScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const { search } = useLocation();
  const ticket = new URLSearchParams(search).get('ticket');
  const suffix = ticket ? `?ticket=${encodeURIComponent(ticket)}` : '';
  const [activeOnly, setActiveOnly] = useState(true);
  const list = usePagedList(
    (cursor, signal) => runbooksApi.list(activeOnly, cursor, signal),
    [activeOnly],
  );
  const columns: Column<Runbook>[] = [
    {
      key: 'ref',
      header: t('runbooks.col.reference'),
      render: (r) => (
        <Link to={`/runbooks/${encodeURIComponent(r.id)}${suffix}`}>{r.reference}</Link>
      ),
    },
    { key: 'title', header: t('runbooks.col.title'), render: (r) => r.title },
    { key: 'steps', header: t('runbooks.col.steps'), render: (r) => r.steps.length },
    {
      key: 'active',
      header: t('runbooks.col.status'),
      render: (r) => (
        <Badge tone={r.active ? 'success' : 'neutral'}>
          {t(r.active ? 'runbooks.active' : 'runbooks.inactive')}
        </Badge>
      ),
    },
  ];
  const activeFilters = [
    ...(activeOnly
      ? [
          {
            key: 'active',
            label: t('runbooks.filter.activeOnly'),
            onRemove: () => {
              setActiveOnly(false);
            },
          },
        ]
      : []),
  ];

  return (
    <>
      <PageHeader
        title={t('nav.runbooks')}
        intro={t('runbooks.intro')}
        actions={
          can('knowledge.manage') ? (
            <Link to="/runbooks/new" className="btn btn-primary">
              {t('runbooks.new')}
            </Link>
          ) : null
        }
      />
      <FilterBar activeFilters={activeFilters} onSubmit={(event) => event.preventDefault()}>
        <Checkbox
          label={t('runbooks.filter.activeOnly')}
          checked={activeOnly}
          onChange={(event) => setActiveOnly(event.target.checked)}
        />
      </FilterBar>
      <DataTable
        filterSummary={activeFilters.map((filter) => filter.label).join(' · ')}
        caption={t('nav.runbooks')}
        columns={columns}
        rows={list.items}
        rowKey={(r) => r.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('runbooks.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </>
  );
}
