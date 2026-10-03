import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { formatDateTime, formatMoney, parseMoneyToCents } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Dialog } from '../../platform/ui/Dialog';
import { Select, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { ReasonDialog } from '../../platform/ui/ReasonDialog';
import { ProductPicker, type ProductChoice } from '../products/ProductPicker';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { procurementApi } from './api';
import { OrderStatusBadge } from './OrdersScreen';
import type { OrderLine, ProcurementRequest, PurchaseOrderDetail } from './types';

type Dialog =
  | { kind: 'addLine' }
  | { kind: 'editLine'; line: OrderLine }
  | { kind: 'submit' }
  | { kind: 'cancel' }
  | { kind: 'close' };

function priceText(cents: number): string {
  return (cents / 100).toFixed(2);
}

function LineDialog({
  order,
  line,
  onClose,
  onDone,
}: {
  order: PurchaseOrderDetail;
  line: OrderLine | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const openNeeds = useAsync(
    (signal) =>
      line ? Promise.resolve(undefined) : procurementApi.requests('open', undefined, signal),
    [line],
  );
  const [product, setProduct] = useState<ProductChoice | null>(null);
  const [request, setRequest] = useState<ProcurementRequest | null>(null);
  const [quantity, setQuantity] = useState(line ? String(line.quantity) : '1');
  const [price, setPrice] = useState(line ? priceText(line.unitPriceCents) : '');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const cents = parseMoneyToCents(price);
  const amount = Number.parseInt(quantity, 10);
  const productId = line?.productId ?? request?.productId ?? product?.id;
  const valid =
    productId !== undefined && cents !== undefined && Number.isInteger(amount) && amount >= 1;

  const pick = (id: string) => {
    const found = openNeeds.data?.items.find((n) => n.id === id) ?? null;
    setRequest(found);
    if (found) setQuantity(String(found.quantity));
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!valid || cents === undefined || productId === undefined) return;
    setBusy(true);
    setError(undefined);
    try {
      if (line)
        await procurementApi.updateLine(order.id, line.id, {
          quantity: amount,
          unitPriceCents: cents,
        });
      else
        await procurementApi.addLine(order.id, {
          productId,
          quantity: amount,
          unitPriceCents: cents,
          ...(request ? { requestId: request.id } : {}),
        });
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };

  return (
    <Dialog title={line ? t('procurement.line.edit') : t('procurement.line.add')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        {!line ? (
          <>
            <Select
              label={t('procurement.line.fromRequest')}
              hint={t('procurement.line.fromRequest.hint')}
              value={request?.id ?? ''}
              onChange={(event) => pick(event.target.value)}
              options={[
                { value: '', label: t('procurement.line.noRequest') },
                ...(openNeeds.data?.items ?? []).map((n) => ({
                  value: n.id,
                  label: `${n.reference} · ${n.quantity} × ${openNeeds.data?.names?.products?.[n.productId] ?? n.productId}`,
                })),
              ]}
            />
            {!request ? <ProductPicker value={product} onChange={setProduct} /> : null}
          </>
        ) : (
          <p>{order.productNames[line.productId] ?? line.productId}</p>
        )}
        <TextField
          label={t('procurement.line.quantity')}
          value={quantity}
          inputMode="numeric"
          onChange={(event) => setQuantity(event.target.value)}
        />
        <TextField
          label={t('procurement.line.price', { currency: order.currency })}
          value={price}
          inputMode="decimal"
          error={
            price !== '' && cents === undefined ? t('procurement.line.price.invalid') : undefined
          }
          onChange={(event) => setPrice(event.target.value)}
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!valid}>
            {t('procurement.save')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

function SubmitDialog({
  order,
  onClose,
  onDone,
}: {
  order: PurchaseOrderDetail;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [type, setType] = useState<'user' | 'team'>('user');
  const [approver, setApprover] = useState<Assignee | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!approver) return;
    setBusy(true);
    setError(undefined);
    try {
      await procurementApi.submit(
        order.id,
        order.version,
        type === 'user' ? { approverUserId: approver.id } : { approverTeamId: approver.id },
      );
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('procurement.order.submit')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        <p>{t('procurement.order.submit.hint')}</p>
        {error ? <ApiErrorAlert error={error} /> : null}
        <Select
          label={t('procurement.order.approverType')}
          value={type}
          onChange={(event) => {
            setType(event.target.value as 'user' | 'team');
            setApprover(null);
          }}
          options={[
            { value: 'user', label: t('assets.assign.user') },
            { value: 'team', label: t('assets.assign.team') },
          ]}
        />
        <AssigneePicker key={type} type={type} value={approver} onChange={setApprover} />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!approver}>
            {t('procurement.order.submit')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function OrderDetailScreen({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const loaded = useAsync((signal) => procurementApi.order(id, signal), [id]);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const [busyOp, setBusyOp] = useState<string | null>(null);
  const [actionError, setActionError] = useState<ApiError | undefined>(undefined);

  if (loaded.error) return <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />;
  const order = loaded.data;
  if (!order) {
    return (
      <p className="loading" role="status">
        {t('state.loading')}
      </p>
    );
  }
  const done = () => {
    setDialog(null);
    setActionError(undefined);
    loaded.reload();
  };
  const run = async (op: 'send' | 'acknowledge') => {
    setBusyOp(op);
    setActionError(undefined);
    try {
      await procurementApi.operate(order.id, op, order.version);
      done();
    } catch (cause) {
      setActionError(asApiError(cause));
    } finally {
      setBusyOp(null);
    }
  };
  const removeLine = async (line: OrderLine) => {
    setActionError(undefined);
    try {
      await procurementApi.removeLine(order.id, line.id);
      done();
    } catch (cause) {
      setActionError(asApiError(cause));
    }
  };

  const columns: Column<OrderLine>[] = [
    { key: 'no', header: '#', render: (l) => l.lineNo },
    {
      key: 'product',
      header: t('procurement.col.product'),
      render: (l) => order.productNames[l.productId] ?? l.productId,
    },
    { key: 'quantity', header: t('procurement.col.quantity'), render: (l) => l.quantity },
    { key: 'received', header: t('procurement.col.received'), render: (l) => l.receivedQuantity },
    {
      key: 'price',
      header: t('procurement.col.unitPrice'),
      render: (l) => formatMoney(locale, l.unitPriceCents, order.currency),
    },
    {
      key: 'total',
      header: t('procurement.col.lineTotal'),
      render: (l) => formatMoney(locale, l.unitPriceCents * l.quantity, order.currency),
    },
    ...(order.linesEditable
      ? [
          {
            key: 'actions',
            header: '',
            render: (l: OrderLine) => (
              <>
                <Button onClick={() => setDialog({ kind: 'editLine', line: l })}>
                  {t('procurement.line.edit')}
                </Button>{' '}
                <Button onClick={() => void removeLine(l)}>{t('procurement.line.remove')}</Button>
              </>
            ),
          },
        ]
      : []),
  ];
  const ops = order.allowedOperations;
  const receivable = ['sent', 'acknowledged', 'partially_received'].includes(order.status);
  return (
    <>
      <PageHeader
        title={`${order.reference} · ${order.supplierName}`}
        actions={
          <>
            {ops.includes('submit') && order.lines.length > 0 ? (
              <Button variant="primary" onClick={() => setDialog({ kind: 'submit' })}>
                {t('procurement.order.submit')}
              </Button>
            ) : null}
            {ops.includes('send') ? (
              <Button variant="primary" busy={busyOp === 'send'} onClick={() => void run('send')}>
                {t('procurement.order.send')}
              </Button>
            ) : null}
            {ops.includes('acknowledge') ? (
              <Button busy={busyOp === 'acknowledge'} onClick={() => void run('acknowledge')}>
                {t('procurement.order.acknowledge')}
              </Button>
            ) : null}
            {receivable && can('inventory.manage') ? (
              <Link
                to={`/inventory/receipts/new?order=${encodeURIComponent(order.id)}`}
                className="btn btn-primary"
              >
                {t('procurement.order.receive')}
              </Link>
            ) : null}
            {ops.includes('close') ? (
              <Button onClick={() => setDialog({ kind: 'close' })}>
                {t('procurement.order.close')}
              </Button>
            ) : null}
            {ops.includes('cancel') ? (
              <Button variant="danger" onClick={() => setDialog({ kind: 'cancel' })}>
                {t('procurement.order.cancel')}
              </Button>
            ) : null}
          </>
        }
      />
      <p>
        <Link to="/procurement/orders">{t('procurement.order.back')}</Link>
      </p>
      {actionError ? <ApiErrorAlert error={actionError} onRetry={loaded.reload} /> : null}
      <dl className="facts">
        <dt>{t('procurement.col.status')}</dt>
        <dd>
          <OrderStatusBadge status={order.status} />
          {order.statusReason ? (
            <>
              {' '}
              {order.statusReason === 'approval_rejected'
                ? t('procurement.order.rejected')
                : order.statusReason}
            </>
          ) : null}
        </dd>
        <dt>{t('procurement.col.supplier')}</dt>
        <dd>{order.supplierName}</dd>
        <dt>{t('procurement.order.currency')}</dt>
        <dd>{order.currency}</dd>
        <dt>{t('procurement.order.total')}</dt>
        <dd>{formatMoney(locale, order.totalCents, order.currency)}</dd>
        {order.sentAt ? (
          <>
            <dt>{t('procurement.order.sentAt')}</dt>
            <dd>{formatDateTime(locale, order.sentAt)}</dd>
          </>
        ) : null}
        {order.notes ? (
          <>
            <dt>{t('procurement.need.notes')}</dt>
            <dd className="preline">{order.notes}</dd>
          </>
        ) : null}
      </dl>
      <section>
        <h2>{t('procurement.order.lines')}</h2>
        {order.linesEditable ? (
          <p>
            <Button onClick={() => setDialog({ kind: 'addLine' })}>
              {t('procurement.line.add')}
            </Button>
          </p>
        ) : null}
        <DataTable
          caption={t('procurement.order.lines')}
          columns={columns}
          rows={order.lines}
          rowKey={(l) => l.id}
          emptyText={t('procurement.order.noLines')}
        />
      </section>
      {dialog?.kind === 'addLine' ? (
        <LineDialog order={order} line={null} onClose={() => setDialog(null)} onDone={done} />
      ) : null}
      {dialog?.kind === 'editLine' ? (
        <LineDialog
          order={order}
          line={dialog.line}
          onClose={() => setDialog(null)}
          onDone={done}
        />
      ) : null}
      {dialog?.kind === 'submit' ? (
        <SubmitDialog order={order} onClose={() => setDialog(null)} onDone={done} />
      ) : null}
      {dialog?.kind === 'cancel' ? (
        <ReasonDialog
          title={t('procurement.order.cancel')}
          label={t('procurement.reason.label')}
          confirmLabel={t('procurement.order.cancel')}
          danger
          onClose={() => setDialog(null)}
          onSubmit={async (reason) => {
            await procurementApi.operate(order.id, 'cancel', order.version, reason);
            done();
          }}
        />
      ) : null}
      {dialog?.kind === 'close' ? (
        <ReasonDialog
          title={t('procurement.order.close')}
          label={t('procurement.reason.label')}
          hint={
            order.status === 'partially_received' ? t('procurement.order.close.hint') : undefined
          }
          confirmLabel={t('procurement.order.close')}
          required={order.status === 'partially_received'}
          onClose={() => setDialog(null)}
          onSubmit={async (reason) => {
            await procurementApi.operate(order.id, 'close', order.version, reason);
            done();
          }}
        />
      ) : null}
    </>
  );
}
