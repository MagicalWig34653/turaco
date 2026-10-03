import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, usePagedList } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Dialog } from '../../platform/ui/Dialog';
import { Select, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { ReasonDialog } from '../../platform/ui/ReasonDialog';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { inventoryApi } from './api';
import type { Reservation } from './types';

function FulfillDialog({
  reservation,
  onClose,
  onDone,
}: {
  reservation: Reservation;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const isAsset = reservation.kind === 'asset';
  const [type, setType] = useState<'user' | 'team'>('user');
  const [assignee, setAssignee] = useState<Assignee | null>(null);
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await inventoryApi.fulfill(reservation.id, reservation.version, {
        note: note.trim(),
        ...(isAsset && assignee ? { assigneeType: type, assigneeId: assignee.id } : {}),
      });
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('inventory.reservation.fulfill')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        {isAsset ? (
          <>
            <Select
              label={t('assets.assign.type')}
              value={type}
              onChange={(event) => {
                setType(event.target.value as 'user' | 'team');
                setAssignee(null);
              }}
              options={[
                { value: 'user', label: t('assets.assign.user') },
                { value: 'team', label: t('assets.assign.team') },
              ]}
            />
            <AssigneePicker key={type} type={type} value={assignee} onChange={setAssignee} />
          </>
        ) : (
          <p>{t('inventory.reservation.fulfill.stock', { quantity: reservation.quantity ?? 0 })}</p>
        )}
        <TextField
          label={t('assets.assign.note')}
          value={note}
          maxLength={500}
          onChange={(event) => setNote(event.target.value)}
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={isAsset && !assignee}>
            {t('inventory.reservation.fulfill')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function ReservationsScreen() {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const manage = can('inventory.manage');
  const [status, setStatus] = useState('active');
  const [dialog, setDialog] = useState<{
    kind: 'release' | 'fulfill';
    reservation: Reservation;
  } | null>(null);
  const [products, setProducts] = useState<Record<string, string>>({});
  const [locations, setLocations] = useState<Record<string, string>>({});
  const list = usePagedList<Reservation>(
    async (cursor, signal) => {
      const page = await inventoryApi.reservations(status, cursor, signal);
      setProducts((previous) => ({ ...previous, ...page.names?.products }));
      setLocations((previous) => ({ ...previous, ...page.names?.storageLocations }));
      return page;
    },
    [status],
  );
  const columns: Column<Reservation>[] = [
    {
      key: 'time',
      header: t('inventory.col.time'),
      render: (r) => formatDateTime(locale, r.createdAt),
    },
    {
      key: 'product',
      header: t('inventory.col.product'),
      render: (r) => products[r.productId] ?? r.productId,
    },
    {
      key: 'what',
      header: t('inventory.col.reserved'),
      render: (r) =>
        r.kind === 'asset' && r.assetId ? (
          <Link to={`/assets/${encodeURIComponent(r.assetId)}`}>
            {t('inventory.reservation.asset')}
          </Link>
        ) : (
          `${r.quantity ?? 0} · ${r.storageLocationId ? (locations[r.storageLocationId] ?? '') : ''}`
        ),
    },
    {
      key: 'status',
      header: t('inventory.col.status'),
      render: (r) => (
        <Badge tone={r.status === 'active' ? 'warning' : 'neutral'}>
          {t(`inventory.reservation.status.${r.status}`)}
        </Badge>
      ),
    },
    ...(manage
      ? [
          {
            key: 'actions',
            header: '',
            render: (r: Reservation) =>
              r.status === 'active' ? (
                <>
                  <Button onClick={() => setDialog({ kind: 'fulfill', reservation: r })}>
                    {t('inventory.reservation.fulfill')}
                  </Button>{' '}
                  <Button onClick={() => setDialog({ kind: 'release', reservation: r })}>
                    {t('inventory.reservation.release')}
                  </Button>
                </>
              ) : null,
          },
        ]
      : []),
  ];
  const done = () => {
    setDialog(null);
    list.reload();
  };
  return (
    <>
      <PageHeader title={t('nav.reservations')} intro={t('inventory.reservation.intro')} />
      <form className="filters" role="search" onSubmit={(event) => event.preventDefault()}>
        <Select
          label={t('inventory.col.status')}
          value={status}
          onChange={(event) => setStatus(event.target.value)}
          options={[
            { value: '', label: t('inventory.reservation.anyStatus') },
            ...(['active', 'fulfilled', 'released', 'cancelled'] as const).map((value) => ({
              value,
              label: t(`inventory.reservation.status.${value}`),
            })),
          ]}
        />
      </form>
      <DataTable
        caption={t('nav.reservations')}
        columns={columns}
        rows={list.items}
        rowKey={(r) => r.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('inventory.reservation.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
      {dialog?.kind === 'release' ? (
        <ReasonDialog
          title={t('inventory.reservation.release')}
          label={t('inventory.stock.reason')}
          confirmLabel={t('inventory.reservation.release')}
          required={false}
          onClose={() => setDialog(null)}
          onSubmit={async (reason) => {
            await inventoryApi.release(dialog.reservation.id, dialog.reservation.version, reason);
            done();
          }}
        />
      ) : null}
      {dialog?.kind === 'fulfill' ? (
        <FulfillDialog
          reservation={dialog.reservation}
          onClose={() => setDialog(null)}
          onDone={done}
        />
      ) : null}
    </>
  );
}
