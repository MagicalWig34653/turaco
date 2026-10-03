import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync, usePagedList } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Dialog } from '../../platform/ui/Dialog';
import { Select, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { procurementApi } from './api';
import { orderTone } from './format';
import { orderStatuses, type OrderStatus, type PurchaseOrder } from './types';

export function OrderStatusBadge({ status }: { status: OrderStatus }) {
  const { t } = useI18n();
  return <Badge tone={orderTone[status]}>{t(`procurement.status.${status}`)}</Badge>;
}

function NewOrderDialog({ onClose }: { onClose: () => void }) {
  const { t } = useI18n();
  const suppliers = useAsync(
    (signal) => procurementApi.suppliers(false, '', undefined, signal),
    [],
  );
  const [supplier, setSupplier] = useState('');
  const [currency, setCurrency] = useState('EUR');
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      const created = await procurementApi.createOrder({
        supplierId: supplier,
        currency: currency.trim().toUpperCase(),
        notes: notes.trim(),
      });
      navigate(`/procurement/orders/${encodeURIComponent(created.id)}`);
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('procurement.order.new')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        {suppliers.error ? (
          <ApiErrorAlert error={suppliers.error} onRetry={suppliers.reload} />
        ) : null}
        <Select
          label={t('procurement.col.supplier')}
          value={supplier}
          onChange={(event) => setSupplier(event.target.value)}
          options={[
            { value: '', label: t('procurement.order.chooseSupplier') },
            ...(suppliers.data?.items ?? []).map((s) => ({ value: s.id, label: s.name })),
          ]}
        />
        <TextField
          label={t('procurement.order.currency')}
          value={currency}
          maxLength={3}
          onChange={(event) => setCurrency(event.target.value)}
        />
        <TextField
          label={t('procurement.need.notes')}
          value={notes}
          maxLength={2000}
          onChange={(event) => setNotes(event.target.value)}
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button
            type="submit"
            variant="primary"
            busy={busy}
            disabled={supplier === '' || !/^[A-Za-z]{3}$/.test(currency.trim())}
          >
            {t('procurement.order.create')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function OrdersScreen() {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const [status, setStatus] = useState<OrderStatus | ''>('');
  const [creating, setCreating] = useState(false);
  const [suppliers, setSuppliers] = useState<Record<string, string>>({});
  const list = usePagedList<PurchaseOrder>(
    async (cursor, signal) => {
      const page = await procurementApi.orders(status, cursor, signal);
      setSuppliers((previous) => ({ ...previous, ...page.names?.suppliers }));
      return page;
    },
    [status],
  );
  const columns: Column<PurchaseOrder>[] = [
    {
      key: 'reference',
      header: t('procurement.col.reference'),
      render: (o) => (
        <Link to={`/procurement/orders/${encodeURIComponent(o.id)}`}>{o.reference}</Link>
      ),
    },
    {
      key: 'supplier',
      header: t('procurement.col.supplier'),
      render: (o) => suppliers[o.supplierId] ?? '–',
    },
    {
      key: 'status',
      header: t('procurement.col.status'),
      render: (o) => <OrderStatusBadge status={o.status} />,
    },
    {
      key: 'created',
      header: t('procurement.col.created'),
      render: (o) => formatDateTime(locale, o.createdAt),
    },
  ];
  return (
    <>
      <PageHeader
        title={t('nav.purchaseOrders')}
        intro={t('procurement.order.intro')}
        actions={
          can('procurement.manage') ? (
            <Button variant="primary" onClick={() => setCreating(true)}>
              {t('procurement.order.new')}
            </Button>
          ) : null
        }
      />
      <form className="filters" role="search" onSubmit={(event) => event.preventDefault()}>
        <Select
          label={t('procurement.col.status')}
          value={status}
          onChange={(event) => setStatus(event.target.value as OrderStatus | '')}
          options={[
            { value: '', label: t('procurement.filter.anyStatus') },
            ...orderStatuses.map((value) => ({ value, label: t(`procurement.status.${value}`) })),
          ]}
        />
      </form>
      <DataTable
        caption={t('nav.purchaseOrders')}
        columns={columns}
        rows={list.items}
        rowKey={(o) => o.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('procurement.order.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
      {creating ? <NewOrderDialog onClose={() => setCreating(false)} /> : null}
    </>
  );
}
