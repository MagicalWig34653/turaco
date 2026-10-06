import { TableDate } from '../../platform/ui/TableDate';
import { FilterBar } from '../../platform/ui/FilterBar';
import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Dialog } from '../../platform/ui/Dialog';
import { Select, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { ReasonDialog } from '../../platform/ui/ReasonDialog';
import { ProductPicker, type ProductChoice } from '../products/ProductPicker';
import { procurementApi } from './api';
import { needStatuses, type NeedStatus, type ProcurementRequest } from './types';

function NewNeedDialog({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const { t } = useI18n();
  const [product, setProduct] = useState<ProductChoice | null>(null);
  const [quantity, setQuantity] = useState('1');
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const amount = Number.parseInt(quantity, 10);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!product) return;
    setBusy(true);
    setError(undefined);
    try {
      await procurementApi.createRequest({
        productId: product.id,
        quantity: amount,
        notes: notes.trim(),
      });
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('procurement.need.new')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <ProductPicker value={product} onChange={setProduct} />
        <TextField
          label={t('procurement.need.quantity')}
          value={quantity}
          inputMode="numeric"
          onChange={(event) => setQuantity(event.target.value)}
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
            disabled={!product || !Number.isInteger(amount) || amount < 1}
          >
            {t('procurement.save')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function NeedsScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const manage = can('procurement.manage');
  const [status, setStatus] = useState<NeedStatus | ''>('open');
  const [dialog, setDialog] = useState<
    { kind: 'new' } | { kind: 'cancel'; need: ProcurementRequest } | null
  >(null);
  const [products, setProducts] = useState<Record<string, string>>({});
  const list = usePagedList<ProcurementRequest>(
    async (cursor, signal) => {
      const page = await procurementApi.requests(status, cursor, signal);
      setProducts((previous) => ({ ...previous, ...page.names?.products }));
      return page;
    },
    [status],
  );
  const columns: Column<ProcurementRequest>[] = [
    { key: 'reference', header: t('procurement.col.reference'), render: (n) => n.reference },
    {
      key: 'product',
      header: t('procurement.col.product'),
      render: (n) => products[n.productId] ?? n.productId,
    },
    { key: 'quantity', header: t('procurement.col.quantity'), render: (n) => n.quantity },
    {
      key: 'status',
      header: t('procurement.col.status'),
      render: (n) => (
        <Badge tone={n.status === 'open' ? 'warning' : 'neutral'}>
          {t(`procurement.need.status.${n.status}`)}
        </Badge>
      ),
    },
    {
      key: 'created',
      header: t('procurement.col.created'),
      render: (n) => <TableDate value={n.createdAt} />,
    },
    ...(manage
      ? [
          {
            key: 'actions',
            header: '',
            render: (n: ProcurementRequest) =>
              n.status === 'open' ? (
                <Button onClick={() => setDialog({ kind: 'cancel', need: n })}>
                  {t('procurement.need.cancel')}
                </Button>
              ) : null,
          },
        ]
      : []),
  ];
  const activeFilters = [
    ...(status
      ? [
          {
            key: 'status',
            label: t(`procurement.need.status.${status}`),
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
        title={t('nav.procurementRequests')}
        intro={t('procurement.need.intro')}
        actions={
          manage ? (
            <Button variant="primary" onClick={() => setDialog({ kind: 'new' })}>
              {t('procurement.need.new')}
            </Button>
          ) : null
        }
      />
      <FilterBar
        activeFilters={activeFilters}
        role="search"
        onSubmit={(event) => event.preventDefault()}
      >
        <Select
          label={t('procurement.col.status')}
          value={status}
          onChange={(event) => setStatus(event.target.value as NeedStatus | '')}
          options={[
            { value: '', label: t('procurement.filter.anyStatus') },
            ...needStatuses.map((value) => ({
              value,
              label: t(`procurement.need.status.${value}`),
            })),
          ]}
        />
      </FilterBar>
      <DataTable
        filterSummary={activeFilters.map((filter) => filter.label).join(' · ')}
        caption={t('nav.procurementRequests')}
        columns={columns}
        rows={list.items}
        rowKey={(n) => n.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('procurement.need.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
      {dialog?.kind === 'new' ? (
        <NewNeedDialog
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null);
            list.reload();
          }}
        />
      ) : null}
      {dialog?.kind === 'cancel' ? (
        <ReasonDialog
          title={t('procurement.need.cancel')}
          label={t('procurement.reason.label')}
          confirmLabel={t('procurement.need.cancel')}
          danger
          onClose={() => setDialog(null)}
          onSubmit={async (reason) => {
            await procurementApi.cancelRequest(dialog.need.id, reason, dialog.need.version);
            setDialog(null);
            list.reload();
          }}
        />
      ) : null}
    </>
  );
}
