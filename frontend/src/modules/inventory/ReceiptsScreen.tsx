import { TableDate } from '../../platform/ui/TableDate';
import { useState } from 'react';
import { usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { PageHeader } from '../../platform/ui/PageHeader';
import { inventoryApi } from './api';
import type { GoodsReceipt } from './types';

export function ReceiptsScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const [products, setProducts] = useState<Record<string, string>>({});
  const list = usePagedList<GoodsReceipt>(async (cursor, signal) => {
    const page = await inventoryApi.receipts(cursor, signal);
    setProducts((previous) => ({ ...previous, ...page.names?.products }));
    return page;
  }, []);
  const columns: Column<GoodsReceipt>[] = [
    { key: 'reference', header: t('inventory.receipt.reference'), render: (r) => r.reference },
    {
      key: 'time',
      header: t('inventory.col.time'),
      render: (r) => <TableDate value={r.createdAt} />,
    },
    {
      key: 'note',
      header: t('inventory.receipt.deliveryNote'),
      render: (r) => r.deliveryNote ?? '–',
    },
    {
      key: 'lines',
      header: t('inventory.receipt.lines'),
      render: (r) => (
        <ul className="plain-list">
          {r.lines.map((l) => (
            <li key={l.id}>
              {l.quantity} × {products[l.productId] ?? l.productId}
              {l.assetIds.length > 0 ? (
                <>
                  {' · '}
                  {l.assetIds.map((id, index) => (
                    <span key={id}>
                      {index > 0 ? ', ' : ''}
                      <Link to={`/assets/${encodeURIComponent(id)}`}>
                        {t('inventory.receipt.asset', { n: index + 1 })}
                      </Link>
                    </span>
                  ))}
                </>
              ) : null}
            </li>
          ))}
        </ul>
      ),
    },
    {
      key: 'order',
      header: t('inventory.receipt.order'),
      render: (r) =>
        can('procurement.view') || can('procurement.manage') ? (
          <Link to={`/procurement/orders/${encodeURIComponent(r.orderId)}`}>
            {t('inventory.receipt.openOrder')}
          </Link>
        ) : (
          '–'
        ),
    },
  ];
  return (
    <>
      <PageHeader
        title={t('nav.receipts')}
        intro={t('inventory.receipt.intro')}
        actions={
          can('inventory.manage') ? (
            <Link to="/inventory/receipts/new" className="btn btn-primary">
              {t('inventory.receipt.new')}
            </Link>
          ) : null
        }
      />
      <DataTable
        caption={t('nav.receipts')}
        columns={columns}
        rows={list.items}
        rowKey={(r) => r.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('inventory.receipt.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </>
  );
}
