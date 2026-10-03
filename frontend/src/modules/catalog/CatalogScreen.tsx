import { usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { PageHeader } from '../../platform/ui/PageHeader';
import { catalogApi } from './api';
import type { CatalogItem } from './types';

/** Catalog of requestable items for every signed-in user (the server returns active items only). */
export function CatalogScreen() {
  const { t } = useI18n();
  const list = usePagedList((cursor, signal) => catalogApi.list('active', cursor, signal), []);
  const columns: Column<CatalogItem>[] = [
    { key: 'title', header: t('catalogAdmin.col.title'), render: (item) => item.title },
    {
      key: 'description',
      header: t('catalogAdmin.field.description'),
      render: (item) => item.description,
    },
    {
      key: 'action',
      header: '',
      render: (item) => (
        <Link to={`/catalog/${encodeURIComponent(item.id)}`} className="btn btn-primary">
          {t('catalog.request')}
          <span className="visually-hidden">: {item.title}</span>
        </Link>
      ),
    },
  ];
  return (
    <>
      <PageHeader title={t('nav.catalog')} intro={t('catalog.intro')} />
      <DataTable
        caption={t('nav.catalog')}
        columns={columns}
        rows={list.items}
        rowKey={(item) => item.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('catalog.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </>
  );
}
