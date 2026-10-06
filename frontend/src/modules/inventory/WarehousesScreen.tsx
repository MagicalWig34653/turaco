import { FilterBar } from '../../platform/ui/FilterBar';
import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { Checkbox, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { inventoryApi } from './api';
import type { StorageLocation, Warehouse } from './types';

function NameDialog({
  title,
  initial,
  confirmLabel,
  onSubmit,
  onClose,
}: {
  title: string;
  initial: string;
  confirmLabel: string;
  onSubmit: (name: string) => Promise<void>;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await onSubmit(name.trim());
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={title} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <TextField
          label={t('inventory.warehouse.name')}
          value={name}
          maxLength={100}
          required
          autoFocus
          onChange={(event) => setName(event.target.value)}
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={name.trim() === ''}>
            {confirmLabel}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

type Dialog =
  | { kind: 'newLocation' }
  | { kind: 'renameWarehouse' }
  | { kind: 'renameLocation'; location: StorageLocation };

function WarehouseSection({
  warehouse,
  includeInactive,
  manage,
  onChanged,
}: {
  warehouse: Warehouse;
  includeInactive: boolean;
  manage: boolean;
  onChanged: () => void;
}) {
  const { t } = useI18n();
  const locations = useAsync(
    (signal) => inventoryApi.locations(warehouse.id, includeInactive, signal),
    [warehouse.id, includeInactive],
  );
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const guard = async (action: () => Promise<unknown>, reload: () => void) => {
    setError(undefined);
    try {
      await action();
      reload();
    } catch (cause) {
      setError(asApiError(cause));
    }
  };
  const closeAnd = (reload: () => void) => async () => {
    setDialog(null);
    reload();
  };
  return (
    <section>
      <h2>
        {warehouse.name}{' '}
        {!warehouse.active ? <Badge>{t('inventory.warehouse.inactive')}</Badge> : null}
      </h2>
      {manage ? (
        <p>
          <Button onClick={() => setDialog({ kind: 'renameWarehouse' })}>
            {t('inventory.warehouse.rename')}
          </Button>{' '}
          <Button
            onClick={() =>
              void guard(
                () =>
                  inventoryApi.setWarehouseActive(
                    warehouse.id,
                    !warehouse.active,
                    warehouse.version,
                  ),
                onChanged,
              )
            }
          >
            {t(
              warehouse.active ? 'inventory.warehouse.deactivate' : 'inventory.warehouse.activate',
            )}
          </Button>{' '}
          <Button onClick={() => setDialog({ kind: 'newLocation' })} disabled={!warehouse.active}>
            {t('inventory.location.new')}
          </Button>
        </p>
      ) : null}
      {error ? <ApiErrorAlert error={error} /> : null}
      {locations.error ? (
        <ApiErrorAlert error={locations.error} onRetry={locations.reload} />
      ) : null}
      {locations.data && locations.data.items.length === 0 ? (
        <p className="empty">{t('inventory.location.none')}</p>
      ) : null}
      <ul className="plain-list">
        {(locations.data?.items ?? []).map((l) => (
          <li key={l.id}>
            {l.name} {!l.active ? <Badge>{t('inventory.warehouse.inactive')}</Badge> : null}
            {manage ? (
              <>
                {' '}
                <Button onClick={() => setDialog({ kind: 'renameLocation', location: l })}>
                  {t('inventory.warehouse.rename')}
                </Button>{' '}
                <Button
                  onClick={() =>
                    void guard(
                      () => inventoryApi.setLocationActive(l.id, !l.active, l.version),
                      locations.reload,
                    )
                  }
                >
                  {t(l.active ? 'inventory.warehouse.deactivate' : 'inventory.warehouse.activate')}
                </Button>
              </>
            ) : null}
          </li>
        ))}
      </ul>
      {dialog?.kind === 'newLocation' ? (
        <NameDialog
          title={t('inventory.location.new')}
          initial=""
          confirmLabel={t('inventory.warehouse.save')}
          onClose={() => setDialog(null)}
          onSubmit={async (name) => {
            await inventoryApi.createLocation(warehouse.id, name);
            await closeAnd(locations.reload)();
          }}
        />
      ) : null}
      {dialog?.kind === 'renameWarehouse' ? (
        <NameDialog
          title={t('inventory.warehouse.rename')}
          initial={warehouse.name}
          confirmLabel={t('inventory.warehouse.save')}
          onClose={() => setDialog(null)}
          onSubmit={async (name) => {
            await inventoryApi.renameWarehouse(warehouse.id, name, warehouse.version);
            await closeAnd(onChanged)();
          }}
        />
      ) : null}
      {dialog?.kind === 'renameLocation' ? (
        <NameDialog
          title={t('inventory.warehouse.rename')}
          initial={dialog.location.name}
          confirmLabel={t('inventory.warehouse.save')}
          onClose={() => setDialog(null)}
          onSubmit={async (name) => {
            await inventoryApi.renameLocation(dialog.location.id, name, dialog.location.version);
            await closeAnd(locations.reload)();
          }}
        />
      ) : null}
    </section>
  );
}

export function WarehousesScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const manage = can('inventory.manage');
  const [includeInactive, setIncludeInactive] = useState(false);
  const [creating, setCreating] = useState(false);
  const warehouses = useAsync(
    (signal) => inventoryApi.warehouses(includeInactive, signal),
    [includeInactive],
  );
  return (
    <>
      <PageHeader
        title={t('nav.warehouses')}
        intro={t('inventory.warehouse.intro')}
        actions={
          manage ? (
            <Button variant="primary" onClick={() => setCreating(true)}>
              {t('inventory.warehouse.new')}
            </Button>
          ) : null
        }
      />
      <FilterBar
        activeFilters={[
          ...(includeInactive
            ? [
                {
                  key: 'includeInactive',
                  label: t('inventory.warehouse.showInactive'),
                  onRemove: () => {
                    setIncludeInactive(false);
                  },
                },
              ]
            : []),
        ]}
        onSubmit={(event) => event.preventDefault()}
      >
        <Checkbox
          label={t('inventory.warehouse.showInactive')}
          checked={includeInactive}
          onChange={(event) => setIncludeInactive(event.target.checked)}
        />
      </FilterBar>
      {warehouses.error ? (
        <ApiErrorAlert error={warehouses.error} onRetry={warehouses.reload} />
      ) : null}
      {warehouses.loading ? (
        <p className="loading" role="status">
          {t('state.loading')}
        </p>
      ) : null}
      {warehouses.data && warehouses.data.items.length === 0 ? (
        <p className="empty">{t('inventory.warehouse.none')}</p>
      ) : null}
      {(warehouses.data?.items ?? []).map((w) => (
        <WarehouseSection
          key={w.id}
          warehouse={w}
          includeInactive={includeInactive}
          manage={manage}
          onChanged={warehouses.reload}
        />
      ))}
      {creating ? (
        <NameDialog
          title={t('inventory.warehouse.new')}
          initial=""
          confirmLabel={t('inventory.warehouse.save')}
          onClose={() => setCreating(false)}
          onSubmit={async (name) => {
            await inventoryApi.createWarehouse(name);
            setCreating(false);
            warehouses.reload();
          }}
        />
      ) : null}
    </>
  );
}
