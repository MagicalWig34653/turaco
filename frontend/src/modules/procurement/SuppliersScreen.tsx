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
import { Checkbox, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { procurementApi } from './api';
import type { Supplier } from './types';

function SupplierDialog({
  supplier,
  onClose,
  onDone,
}: {
  supplier: Supplier | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState(supplier?.name ?? '');
  const [account, setAccount] = useState(supplier?.accountReference ?? '');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      if (supplier) {
        await procurementApi.updateSupplier(supplier.id, {
          expectedVersion: supplier.version,
          name: name.trim(),
          accountReference: account.trim(),
        });
      } else {
        await procurementApi.createSupplier(name.trim(), account.trim());
      }
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog
      title={supplier ? t('procurement.supplier.edit') : t('procurement.supplier.new')}
      onClose={onClose}
    >
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <TextField
          label={t('procurement.supplier.name')}
          value={name}
          maxLength={150}
          required
          autoFocus
          onChange={(event) => setName(event.target.value)}
        />
        <TextField
          label={t('procurement.supplier.account')}
          hint={t('procurement.supplier.account.hint')}
          value={account}
          maxLength={100}
          onChange={(event) => setAccount(event.target.value)}
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={name.trim() === ''}>
            {t('procurement.save')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function SuppliersScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const manage = can('procurement.manage');
  const [includeInactive, setIncludeInactive] = useState(false);
  const [editing, setEditing] = useState<Supplier | 'new' | null>(null);
  const [actionError, setActionError] = useState<ApiError | undefined>(undefined);
  const list = usePagedList(
    (cursor, signal) => procurementApi.suppliers(includeInactive, '', cursor, signal),
    [includeInactive],
  );
  const toggle = async (s: Supplier) => {
    setActionError(undefined);
    try {
      await procurementApi.setSupplierActive(s.id, !s.active, s.version);
      list.reload();
    } catch (cause) {
      setActionError(asApiError(cause));
    }
  };
  const columns: Column<Supplier>[] = [
    { key: 'name', header: t('procurement.supplier.name'), render: (s) => s.name },
    {
      key: 'account',
      header: t('procurement.supplier.account'),
      render: (s) => s.accountReference ?? '–',
    },
    {
      key: 'status',
      header: t('procurement.col.status'),
      render: (s) => (
        <Badge tone={s.active ? 'success' : 'neutral'}>
          {t(s.active ? 'procurement.supplier.active' : 'procurement.supplier.inactive')}
        </Badge>
      ),
    },
    ...(manage
      ? [
          {
            key: 'actions',
            header: '',
            render: (s: Supplier) => (
              <>
                <Button onClick={() => setEditing(s)}>{t('procurement.supplier.edit')}</Button>{' '}
                <Button onClick={() => void toggle(s)}>
                  {t(
                    s.active ? 'procurement.supplier.deactivate' : 'procurement.supplier.activate',
                  )}
                </Button>
              </>
            ),
          },
        ]
      : []),
  ];
  return (
    <>
      <PageHeader
        title={t('nav.suppliers')}
        intro={t('procurement.supplier.intro')}
        actions={
          manage ? (
            <Button variant="primary" onClick={() => setEditing('new')}>
              {t('procurement.supplier.new')}
            </Button>
          ) : null
        }
      />
      <form className="filters" onSubmit={(event) => event.preventDefault()}>
        <Checkbox
          label={t('procurement.supplier.showInactive')}
          checked={includeInactive}
          onChange={(event) => setIncludeInactive(event.target.checked)}
        />
      </form>
      {actionError ? <ApiErrorAlert error={actionError} onRetry={list.reload} /> : null}
      <DataTable
        caption={t('nav.suppliers')}
        columns={columns}
        rows={list.items}
        rowKey={(s) => s.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('procurement.supplier.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
      {editing ? (
        <SupplierDialog
          key={editing === 'new' ? 'new' : editing.id}
          supplier={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onDone={() => {
            setEditing(null);
            list.reload();
          }}
        />
      ) : null}
    </>
  );
}
