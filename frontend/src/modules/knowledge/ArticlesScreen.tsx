import { TableDate } from '../../platform/ui/TableDate';
import { FilterBar } from '../../platform/ui/FilterBar';
import { useState } from 'react';
import { usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Select, TextField } from '../../platform/ui/Field';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { PageHeader } from '../../platform/ui/PageHeader';
import { knowledgeApi } from './api';
import type { Article, ArticleStatus } from './types';

export function ArticlesScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const manage = can('knowledge.manage');
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState<ArticleStatus | ''>('');
  const q = useDebouncedValue(query.trim(), 300);
  const list = usePagedList(
    (cursor, signal) => knowledgeApi.list(q, status, cursor, signal),
    [q, status],
  );
  const columns: Column<Article>[] = [
    {
      key: 'title',
      header: t('knowledge.col.title'),
      render: (a) => (
        <>
          <Link to={`/knowledge/${encodeURIComponent(a.id)}`}>{a.title}</Link>
          {a.summary ? <span className="field-hint"> – {a.summary}</span> : null}
        </>
      ),
    },
    ...(can('knowledge.view') || manage
      ? [
          {
            key: 'audience',
            header: t('knowledge.col.audience'),
            render: (a: Article) => t(`knowledge.audience.${a.audience}`),
          },
        ]
      : []),
    ...(manage
      ? [
          {
            key: 'status',
            header: t('knowledge.col.status'),
            render: (a: Article) => (
              <Badge tone={a.status === 'published' ? 'success' : 'neutral'}>
                {t(`knowledge.status.${a.status}`)}
              </Badge>
            ),
          },
        ]
      : []),
    {
      key: 'updated',
      header: t('knowledge.col.updated'),
      render: (a) => <TableDate value={a.updatedAt} />,
    },
  ];
  const activeFilters = [
    ...(query
      ? [
          {
            key: 'q',
            label: `${t('knowledge.search')}: ${query}`,
            onRemove: () => {
              setQuery('');
            },
          },
        ]
      : []),
    ...(status
      ? [
          {
            key: 'status',
            label: t(`knowledge.status.${status}`),
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
        title={t('nav.knowledge')}
        intro={t('knowledge.intro')}
        actions={
          manage ? (
            <Link to="/knowledge/new" className="btn btn-primary">
              {t('knowledge.new')}
            </Link>
          ) : null
        }
      />
      <FilterBar
        activeFilters={activeFilters}
        role="search"
        onSubmit={(event) => event.preventDefault()}
      >
        <TextField
          label={t('knowledge.search')}
          type="search"
          value={query}
          maxLength={200}
          autoComplete="off"
          onChange={(event) => setQuery(event.target.value)}
        />
        {manage ? (
          <Select
            label={t('knowledge.col.status')}
            value={status}
            onChange={(event) => setStatus(event.target.value as ArticleStatus | '')}
            options={[
              { value: '', label: t('knowledge.filter.anyStatus') },
              ...(['draft', 'published', 'retired'] as const).map((value) => ({
                value,
                label: t(`knowledge.status.${value}`),
              })),
            ]}
          />
        ) : null}
      </FilterBar>
      <DataTable
        filterSummary={activeFilters.map((filter) => filter.label).join(' · ')}
        caption={t('nav.knowledge')}
        columns={columns}
        rows={list.items}
        rowKey={(a) => a.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('knowledge.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </>
  );
}
