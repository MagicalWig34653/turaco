import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, usePagedList } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Checkbox, Select, TextField } from '../../platform/ui/Field';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { PageHeader } from '../../platform/ui/PageHeader';
import { endpointsApi } from './api';
import {
  complianceStates,
  platforms,
  type Device,
  type DeviceFilters,
  type SyncCounts,
} from './types';

const initial: DeviceFilters = {
  platform: '',
  compliance: '',
  q: '',
  linked: '',
  includeDeleted: false,
};

export function DevicesScreen() {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const [filters, setFilters] = useState<DeviceFilters>(initial);
  const [syncing, setSyncing] = useState(false);
  const [syncError, setSyncError] = useState<ApiError>();
  const [syncResult, setSyncResult] = useState<SyncCounts>();
  const list = usePagedList(
    (cursor, signal) => endpointsApi.devices(filters, cursor, signal),
    [filters],
  );
  const change = (patch: Partial<DeviceFilters>) =>
    setFilters((current) => ({ ...current, ...patch }));
  const sync = async () => {
    setSyncing(true);
    setSyncError(undefined);
    setSyncResult(undefined);
    try {
      setSyncResult(await endpointsApi.sync());
      list.reload();
    } catch (cause) {
      setSyncError(asApiError(cause));
    } finally {
      setSyncing(false);
    }
  };
  const columns: Column<Device>[] = [
    {
      key: 'name',
      header: t('endpoints.name'),
      render: (d) => <Link to={`/devices/${encodeURIComponent(d.id)}`}>{d.name}</Link>,
    },
    {
      key: 'platform',
      header: t('endpoints.platform'),
      render: (d) => t(`endpoints.platform.${d.osPlatform}` as MessageKey),
    },
    {
      key: 'compliance',
      header: t('endpoints.compliance'),
      render: (d) => t(`endpoints.compliance.${d.complianceState}` as MessageKey),
    },
    {
      key: 'asset',
      header: t('endpoints.asset'),
      render: (d) =>
        d.assetId && (can('assets.view') || can('assets.manage')) ? (
          <Link to={`/assets/${encodeURIComponent(d.assetId)}`}>{d.assetId}</Link>
        ) : (
          (d.assetId ?? '–')
        ),
    },
    {
      key: 'observed',
      header: t('endpoints.observedAt'),
      render: (d) => formatDateTime(locale, d.observedAt),
    },
    {
      key: 'synced',
      header: t('endpoints.lastSyncedAt'),
      render: (d) => formatDateTime(locale, d.lastSyncedAt),
    },
  ];
  return (
    <>
      <PageHeader
        title={t('nav.devices')}
        actions={
          can('endpoints.manage') ? (
            <Button variant="primary" busy={syncing} onClick={() => void sync()}>
              {t('endpoints.syncNow')}
            </Button>
          ) : null
        }
      />
      {syncError ? <ApiErrorAlert error={syncError} /> : null}
      {syncResult ? (
        <section aria-label={t('endpoints.syncResult')}>
          <h2>{t('endpoints.syncResult')}</h2>
          <dl className="facts">
            {(Object.entries(syncResult) as [keyof SyncCounts, number][]).map(([key, value]) => (
              <div key={key}>
                <dt>{t(`endpoints.sync.${key}`)}</dt>
                <dd>{value}</dd>
              </div>
            ))}
          </dl>
        </section>
      ) : null}
      <form className="filters" role="search" onSubmit={(e) => e.preventDefault()}>
        <TextField
          label={t('endpoints.search')}
          type="search"
          value={filters.q}
          maxLength={100}
          onChange={(e) => change({ q: e.target.value })}
        />
        <Select
          label={t('endpoints.platform')}
          value={filters.platform}
          onChange={(e) => change({ platform: e.target.value })}
          options={[
            { value: '', label: t('filters.all') },
            ...platforms.map((value) => ({ value, label: t(`endpoints.platform.${value}`) })),
          ]}
        />
        <Select
          label={t('endpoints.compliance')}
          value={filters.compliance}
          onChange={(e) => change({ compliance: e.target.value })}
          options={[
            { value: '', label: t('filters.all') },
            ...complianceStates.map((value) => ({
              value,
              label: t(`endpoints.compliance.${value}`),
            })),
          ]}
        />
        <Select
          label={t('endpoints.linked')}
          value={filters.linked}
          onChange={(e) => change({ linked: e.target.value })}
          options={[
            { value: '', label: t('filters.all') },
            { value: 'true', label: t('endpoints.linked.yes') },
            { value: 'false', label: t('endpoints.linked.no') },
          ]}
        />
        <Checkbox
          label={t('endpoints.includeDeleted')}
          checked={filters.includeDeleted}
          onChange={(e) => change({ includeDeleted: e.target.checked })}
        />
      </form>
      <DataTable
        caption={t('nav.devices')}
        columns={columns}
        rows={list.items}
        rowKey={(d) => d.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('endpoints.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </>
  );
}
