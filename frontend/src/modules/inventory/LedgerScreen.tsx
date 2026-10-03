import { useState } from 'react';
import { usePagedList } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Select } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { inventoryApi } from './api';
import { transactionTypes, type InventoryTransaction } from './types';

export function LedgerScreen() {
  const { t, locale } = useI18n();
  const [type, setType] = useState('');
  const [products, setProducts] = useState<Record<string, string>>({});
  const [locations, setLocations] = useState<Record<string, string>>({});
  const list = usePagedList<InventoryTransaction>(
    async (cursor, signal) => {
      const page = await inventoryApi.transactions({ type }, cursor, signal);
      setProducts((previous) => ({ ...previous, ...page.names?.products }));
      setLocations((previous) => ({ ...previous, ...page.names?.storageLocations }));
      return page;
    },
    [type],
  );
  const columns: Column<InventoryTransaction>[] = [
    {
      key: 'time',
      header: t('inventory.col.time'),
      render: (r) => formatDateTime(locale, r.createdAt),
    },
    { key: 'type', header: t('inventory.col.type'), render: (r) => t(`inventory.type.${r.type}`) },
    {
      key: 'product',
      header: t('inventory.col.product'),
      render: (r) => products[r.productId] ?? r.productId,
    },
    {
      key: 'location',
      header: t('inventory.col.location'),
      render: (r) => locations[r.storageLocationId] ?? '–',
    },
    { key: 'onHand', header: t('inventory.col.onHandDelta'), render: (r) => signed(r.onHandDelta) },
    {
      key: 'reserved',
      header: t('inventory.col.reservedDelta'),
      render: (r) => signed(r.reservedDelta),
    },
    { key: 'reason', header: t('inventory.col.reason'), render: (r) => r.reason ?? '' },
  ];
  return (
    <>
      <PageHeader title={t('nav.ledger')} intro={t('inventory.ledger.intro')} />
      <form className="filters" role="search" onSubmit={(event) => event.preventDefault()}>
        <Select
          label={t('inventory.col.type')}
          value={type}
          onChange={(event) => setType(event.target.value)}
          options={[
            { value: '', label: t('inventory.ledger.anyType') },
            ...transactionTypes.map((value) => ({ value, label: t(`inventory.type.${value}`) })),
          ]}
        />
      </form>
      <DataTable
        caption={t('nav.ledger')}
        columns={columns}
        rows={list.items}
        rowKey={(r) => r.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('inventory.ledger.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </>
  );
}

function signed(n: number): string {
  return n > 0 ? `+${n}` : String(n);
}
