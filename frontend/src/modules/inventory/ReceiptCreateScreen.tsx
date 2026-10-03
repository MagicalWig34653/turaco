import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate, useLocation } from '../../platform/router/Router';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Checkbox, Select, TextArea, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { procurementApi } from '../procurement/api';
import type { PurchaseOrderDetail } from '../procurement/types';
import { inventoryApi } from './api';
import { useStorageLocationOptions } from './locations';
import { parseSerials } from './receiptForm';
import type { ReceiptBody } from './types';

type LineInput = { quantity: string; location: string; serials: string };

const receivable = ['sent', 'acknowledged', 'partially_received'] as const;

function ReceiptForm({ order }: { order: PurchaseOrderDetail }) {
  const { t } = useI18n();
  const locations = useStorageLocationOptions();
  const [note, setNote] = useState('');
  const [available, setAvailable] = useState(false);
  const [inputs, setInputs] = useState<Record<string, LineInput>>({});
  const [key] = useState(() => crypto.randomUUID());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [invalid, setInvalid] = useState<string | undefined>(undefined);

  const get = (id: string): LineInput => inputs[id] ?? { quantity: '', location: '', serials: '' };
  const set = (id: string, patch: Partial<LineInput>) =>
    setInputs((previous) => ({ ...previous, [id]: { ...get(id), ...patch } }));

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setInvalid(undefined);
    const lines: ReceiptBody['lines'] = [];
    for (const line of order.lines) {
      const input = get(line.id);
      if (input.quantity.trim() === '') continue;
      const quantity = Number.parseInt(input.quantity, 10);
      const open = line.quantity - line.receivedQuantity;
      if (!Number.isInteger(quantity) || quantity < 1 || quantity > open) {
        setInvalid(t('inventory.receipt.error.quantity', { line: line.lineNo, open }));
        return;
      }
      const tracking = order.productTracking[line.productId] ?? 'none';
      if (tracking === 'stock') {
        if (input.location === '') {
          setInvalid(t('inventory.receipt.error.location', { line: line.lineNo }));
          return;
        }
        lines.push({ orderLineId: line.id, quantity, storageLocationId: input.location });
      } else if (tracking === 'asset') {
        const units = parseSerials(input.serials);
        if (units.length !== quantity) {
          setInvalid(
            t('inventory.receipt.error.serials', {
              line: line.lineNo,
              quantity,
              given: units.length,
            }),
          );
          return;
        }
        lines.push({ orderLineId: line.id, quantity, units });
      } else {
        lines.push({ orderLineId: line.id, quantity });
      }
    }
    if (lines.length === 0) {
      setInvalid(t('inventory.receipt.error.empty'));
      return;
    }
    setBusy(true);
    setError(undefined);
    try {
      await inventoryApi.postReceipt({
        orderId: order.id,
        deliveryNote: note.trim(),
        assetsAvailable: available,
        idempotencyKey: key,
        lines,
      });
      navigate('/inventory/receipts');
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };

  return (
    <form className="form" onSubmit={(event) => void submit(event)}>
      {error ? <ApiErrorAlert error={error} /> : null}
      {invalid ? <Alert kind="error">{invalid}</Alert> : null}
      <TextField
        label={t('inventory.receipt.deliveryNote')}
        value={note}
        maxLength={100}
        onChange={(event) => setNote(event.target.value)}
      />
      <Checkbox
        label={t('inventory.receipt.assetsAvailable')}
        description={t('inventory.receipt.assetsAvailable.hint')}
        checked={available}
        onChange={(event) => setAvailable(event.target.checked)}
      />
      {order.lines.map((line) => {
        const open = line.quantity - line.receivedQuantity;
        const input = get(line.id);
        const tracking = order.productTracking[line.productId] ?? 'none';
        if (open <= 0) return null;
        return (
          <fieldset key={line.id} className="field">
            <legend>
              {line.lineNo}. {order.productNames[line.productId] ?? line.productId} —{' '}
              {t('inventory.receipt.open', { open, ordered: line.quantity })}
            </legend>
            <TextField
              label={t('inventory.receipt.quantity')}
              value={input.quantity}
              inputMode="numeric"
              onChange={(event) => set(line.id, { quantity: event.target.value })}
            />
            {tracking === 'stock' ? (
              <Select
                label={t('inventory.stock.location')}
                value={input.location}
                onChange={(event) => set(line.id, { location: event.target.value })}
                options={[
                  { value: '', label: t('inventory.stock.chooseLocation') },
                  ...(locations.data ?? []),
                ]}
              />
            ) : null}
            {tracking === 'asset' ? (
              <TextArea
                label={t('inventory.receipt.serials')}
                hint={t('inventory.receipt.serials.hint')}
                value={input.serials}
                rows={4}
                onChange={(event) => set(line.id, { serials: event.target.value })}
              />
            ) : null}
            {tracking === 'none' ? (
              <p className="field-hint">{t('inventory.receipt.untracked')}</p>
            ) : null}
          </fieldset>
        );
      })}
      <div className="form-actions">
        <Button type="submit" variant="primary" busy={busy}>
          {t('inventory.receipt.post')}
        </Button>
      </div>
    </form>
  );
}

export function ReceiptCreateScreen() {
  const { t } = useI18n();
  const { search } = useLocation();
  const [orderId, setOrderId] = useState(() => new URLSearchParams(search).get('order') ?? '');
  const orders = useAsync(async (signal) => {
    const pages = await Promise.all(
      receivable.map((status) => procurementApi.orders(status, undefined, signal)),
    );
    return pages.flatMap((p) => p.items);
  }, []);
  const order = useAsync(
    (signal) => (orderId ? procurementApi.order(orderId, signal) : Promise.resolve(undefined)),
    [orderId],
  );
  return (
    <>
      <PageHeader title={t('inventory.receipt.new')} intro={t('inventory.receipt.new.intro')} />
      <p>
        <Link to="/inventory/receipts">{t('inventory.receipt.back')}</Link>
      </p>
      {orders.error ? <ApiErrorAlert error={orders.error} onRetry={orders.reload} /> : null}
      <Select
        label={t('inventory.receipt.order')}
        value={orderId}
        onChange={(event) => setOrderId(event.target.value)}
        options={[
          { value: '', label: t('inventory.receipt.chooseOrder') },
          ...(orders.data ?? []).map((o) => ({
            value: o.id,
            label: `${o.reference} (${t(`procurement.status.${o.status}`)})`,
          })),
        ]}
      />
      {orders.data && orders.data.length === 0 ? (
        <p className="empty">{t('inventory.receipt.noOrders')}</p>
      ) : null}
      {order.error ? <ApiErrorAlert error={order.error} onRetry={order.reload} /> : null}
      {order.data ? <ReceiptForm key={order.data.id} order={order.data} /> : null}
    </>
  );
}
