import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Dialog } from '../../platform/ui/Dialog';
import { Checkbox, Select, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { ProductPicker, type ProductChoice } from '../products/ProductPicker';
import { inventoryApi } from './api';
import { useStorageLocationOptions } from './locations';
import type { Balance } from './types';

type Op = 'issue' | 'return' | 'dispose' | 'transfer' | 'correct' | 'reserve';

function StockDialog({
  op,
  balance,
  onClose,
  onDone,
}: {
  op: Op;
  /** The balance the operation starts from; absent for "correct stock" at a new place. */
  balance?: Balance;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const locations = useStorageLocationOptions();
  const [product, setProduct] = useState<ProductChoice | null>(null);
  const [location, setLocation] = useState(balance?.storageLocationId ?? '');
  const [target, setTarget] = useState('');
  const [amount, setAmount] = useState('');
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const productId = balance?.productId ?? product?.id ?? '';
  const needsReason = op === 'dispose' || op === 'correct';
  const number = Number.parseInt(amount, 10);
  const valid =
    productId !== '' &&
    location !== '' &&
    Number.isInteger(number) &&
    (op === 'correct' ? number !== 0 : number > 0) &&
    (op !== 'transfer' || (target !== '' && target !== location)) &&
    (!needsReason || reason.trim() !== '');

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      if (op === 'reserve') await inventoryApi.reserveQuantity(productId, location, number);
      else if (op === 'transfer')
        await inventoryApi.moveStock('transfer', {
          productId,
          fromStorageLocationId: location,
          toStorageLocationId: target,
          quantity: number,
          reason: reason.trim(),
        });
      else if (op === 'correct')
        await inventoryApi.moveStock('correct', {
          productId,
          storageLocationId: location,
          delta: number,
          reason: reason.trim(),
        });
      else
        await inventoryApi.moveStock(op, {
          productId,
          storageLocationId: location,
          quantity: number,
          reason: reason.trim(),
        });
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };

  const options = [
    { value: '', label: t('inventory.stock.chooseLocation') },
    ...(locations.data ?? []),
  ];
  return (
    <Dialog title={t(`inventory.op.${op}`)} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        {locations.error ? (
          <ApiErrorAlert error={locations.error} onRetry={locations.reload} />
        ) : null}
        {!balance ? <ProductPicker value={product} onChange={setProduct} /> : null}
        <Select
          label={op === 'transfer' ? t('inventory.stock.from') : t('inventory.stock.location')}
          value={location}
          disabled={balance !== undefined}
          onChange={(event) => setLocation(event.target.value)}
          options={options}
        />
        {op === 'transfer' ? (
          <Select
            label={t('inventory.stock.to')}
            value={target}
            onChange={(event) => setTarget(event.target.value)}
            options={options}
          />
        ) : null}
        <TextField
          label={t(op === 'correct' ? 'inventory.stock.delta' : 'inventory.stock.quantity')}
          hint={op === 'correct' ? t('inventory.stock.delta.hint') : undefined}
          value={amount}
          inputMode="numeric"
          onChange={(event) => setAmount(event.target.value)}
        />
        {op !== 'reserve' ? (
          <TextField
            label={t('inventory.stock.reason')}
            hint={needsReason ? t('inventory.stock.reason.required') : undefined}
            value={reason}
            maxLength={500}
            required={needsReason}
            onChange={(event) => setReason(event.target.value)}
          />
        ) : null}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button
            type="submit"
            variant={op === 'dispose' ? 'danger' : 'primary'}
            busy={busy}
            disabled={!valid}
          >
            {t(`inventory.op.${op}`)}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function StockScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const manage = can('inventory.manage');
  const [withStockOnly, setWithStockOnly] = useState(true);
  const [dialog, setDialog] = useState<{ op: Op; balance?: Balance } | null>(null);
  const [products, setProducts] = useState<Record<string, string>>({});
  const [locations, setLocations] = useState<Record<string, string>>({});
  const list = usePagedList<Balance>(
    async (cursor, signal) => {
      const page = await inventoryApi.stock({ withStockOnly }, cursor, signal);
      setProducts((previous) => ({ ...previous, ...page.names?.products }));
      setLocations((previous) => ({ ...previous, ...page.names?.storageLocations }));
      return page;
    },
    [withStockOnly],
  );

  const columns: Column<Balance>[] = [
    {
      key: 'product',
      header: t('inventory.col.product'),
      render: (b) => products[b.productId] ?? b.productId,
    },
    {
      key: 'location',
      header: t('inventory.col.location'),
      render: (b) => locations[b.storageLocationId] ?? '–',
    },
    { key: 'onHand', header: t('inventory.col.onHand'), render: (b) => b.onHand },
    { key: 'reserved', header: t('inventory.col.reserved'), render: (b) => b.reserved },
    { key: 'available', header: t('inventory.col.available'), render: (b) => b.available },
    ...(manage
      ? [
          {
            key: 'actions',
            header: '',
            render: (b: Balance) => (
              <>
                {(['issue', 'return', 'transfer', 'reserve', 'dispose', 'correct'] as const).map(
                  (op) => (
                    <Button key={op} onClick={() => setDialog({ op, balance: b })}>
                      {t(`inventory.op.${op}`)}
                    </Button>
                  ),
                )}
              </>
            ),
          },
        ]
      : []),
  ];

  return (
    <>
      <PageHeader
        title={t('nav.stock')}
        intro={t('inventory.stock.intro')}
        actions={
          manage ? (
            <Button variant="primary" onClick={() => setDialog({ op: 'correct' })}>
              {t('inventory.stock.addCorrection')}
            </Button>
          ) : null
        }
      />
      <form className="filters" role="search" onSubmit={(event) => event.preventDefault()}>
        <Checkbox
          label={t('inventory.stock.withStockOnly')}
          checked={withStockOnly}
          onChange={(event) => setWithStockOnly(event.target.checked)}
        />
      </form>
      <DataTable
        caption={t('nav.stock')}
        columns={columns}
        rows={list.items}
        rowKey={(b) => `${b.productId}:${b.storageLocationId}`}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('inventory.stock.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
      {dialog ? (
        <StockDialog
          op={dialog.op}
          {...(dialog.balance ? { balance: dialog.balance } : {})}
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null);
            list.reload();
          }}
        />
      ) : null}
    </>
  );
}
