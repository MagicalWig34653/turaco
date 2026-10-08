import { useAi } from '../ai/AiProvider';
import { TableDate } from '../../platform/ui/TableDate';
import { FilterBar } from '../../platform/ui/FilterBar';
import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Checkbox, Select, TextField } from '../../platform/ui/Field';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { PageHeader } from '../../platform/ui/PageHeader';
import { endpointsApi } from './api';
import {
  complianceStates,
  findingKinds,
  observationStates,
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
  managementState: '',
  hasFinding: '',
  osVersion: '',
  lastCheckinOlderThanDays: '',
};

export function DevicesScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const ai = useAi();
  const [filters, setFilters] = useState<DeviceFilters>(initial);
  const [syncing, setSyncing] = useState(false);
  const [syncError, setSyncError] = useState<ApiError>();
  const [syncResult, setSyncResult] = useState<SyncCounts>();
  const managementAllowed = can('endpoint.management.view') || can('endpoints.manage');
  const list = usePagedList(
    (cursor, signal) =>
      endpointsApi.devices(
        managementAllowed ? filters : { ...filters, managementState: '' },
        cursor,
        signal,
      ),
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
      sortValue: (d) => d.name,
      header: t('endpoints.name'),
      render: (d) => <Link to={`/devices/${encodeURIComponent(d.id)}`}>{d.name}</Link>,
    },
    {
      key: 'platform',
      sortValue: (d) => d.osPlatform,
      header: t('endpoints.platform'),
      render: (d) => t(`endpoints.platform.${d.osPlatform}` as MessageKey),
    },
    {
      key: 'compliance',
      sortValue: (d) => d.complianceState,
      header: t('endpoints.compliance'),
      render: (d) => (
        <Badge
          tone={
            d.complianceState === 'compliant'
              ? 'success'
              : d.complianceState === 'noncompliant'
                ? 'danger'
                : 'unknown'
          }
        >
          {t(`endpoints.compliance.${d.complianceState}` as MessageKey)}
        </Badge>
      ),
    },
    {
      key: 'asset',
      header: t('endpoints.asset'),
      render: (d) =>
        d.assetId && (can('assets.view') || can('assets.manage')) ? (
          <Link to={`/assets/${encodeURIComponent(d.assetId)}`}>{t('endpoints.linked.yes')}</Link>
        ) : d.assetId ? (
          t('endpoints.linked.yes')
        ) : (
          '–'
        ),
    },
    {
      key: 'observed',
      sortValue: (d) => d.observedAt,
      header: t('endpoints.observedAt'),
      render: (d) => <TableDate value={d.observedAt} />,
    },
    {
      key: 'synced',
      sortValue: (d) => d.lastSyncedAt,
      header: t('endpoints.lastSyncedAt'),
      render: (d) => <TableDate value={d.lastSyncedAt} />,
    },
  ];
  const activeFilters = [
    ...(filters.q
      ? [
          {
            key: 'q',
            label: `${t('endpoints.search')}: ${filters.q}`,
            onRemove: () => {
              change({ q: '' });
            },
          },
        ]
      : []),
    ...(filters.platform
      ? [
          {
            key: 'platform',
            label: t(`endpoints.platform.${filters.platform}` as MessageKey),
            onRemove: () => {
              change({ platform: '' });
            },
          },
        ]
      : []),
    ...(filters.compliance
      ? [
          {
            key: 'compliance',
            label: t(`endpoints.compliance.${filters.compliance}` as MessageKey),
            onRemove: () => {
              change({ compliance: '' });
            },
          },
        ]
      : []),
    ...(filters.linked
      ? [
          {
            key: 'linked',
            label: t(filters.linked === 'true' ? 'endpoints.linked.yes' : 'endpoints.linked.no'),
            onRemove: () => {
              change({ linked: '' });
            },
          },
        ]
      : []),
    ...(filters.managementState
      ? [
          {
            key: 'managementState',
            label: t(`management.state.${filters.managementState}` as MessageKey),
            onRemove: () => {
              change({ managementState: '' });
            },
          },
        ]
      : []),
    ...(filters.hasFinding
      ? [
          {
            key: 'hasFinding',
            label: t(`endpoints.finding.${filters.hasFinding}` as MessageKey),
            onRemove: () => {
              change({ hasFinding: '' });
            },
          },
        ]
      : []),
    ...(filters.osVersion
      ? [
          {
            key: 'osVersion',
            label: `${t('endpoints.osVersion')}: ${filters.osVersion}`,
            onRemove: () => {
              change({ osVersion: '' });
            },
          },
        ]
      : []),
    ...(filters.lastCheckinOlderThanDays
      ? [
          {
            key: 'lastCheckinOlderThanDays',
            label: `${t('endpoints.lastCheckinOlderThanDays')}: ${filters.lastCheckinOlderThanDays}`,
            onRemove: () => {
              change({ lastCheckinOlderThanDays: '' });
            },
          },
        ]
      : []),
    ...(filters.includeDeleted
      ? [
          {
            key: 'includeDeleted',
            label: t('endpoints.includeDeleted'),
            onRemove: () => {
              change({ includeDeleted: false });
            },
          },
        ]
      : []),
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
      <FilterBar
        activeFilters={activeFilters}
        onClear={() => {
          setFilters(initial);
        }}
        role="search"
        onSubmit={(e) => e.preventDefault()}
      >
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
        {managementAllowed ? (
          <Select
            label={t('endpoints.managementState')}
            value={filters.managementState}
            onChange={(e) => change({ managementState: e.target.value })}
            options={[
              { value: '', label: t('filters.all') },
              ...observationStates
                .filter((v) => ['failed', 'conflict', 'pending'].includes(v))
                .map((value) => ({ value, label: t(`management.state.${value}`) })),
            ]}
          />
        ) : null}
        <Select
          label={t('endpoints.hasFinding')}
          value={filters.hasFinding}
          onChange={(e) => change({ hasFinding: e.target.value })}
          options={[
            { value: '', label: t('filters.all') },
            ...findingKinds.map((value) => ({ value, label: t(`endpoints.finding.${value}`) })),
          ]}
        />
        <TextField
          label={t('endpoints.osVersion')}
          value={filters.osVersion}
          onChange={(e) => change({ osVersion: e.target.value })}
        />
        <TextField
          label={t('endpoints.lastCheckinOlderThanDays')}
          type="number"
          min={1}
          max={3650}
          value={filters.lastCheckinOlderThanDays}
          onChange={(e) => change({ lastCheckinOlderThanDays: e.target.value })}
        />
        <Checkbox
          label={t('endpoints.includeDeleted')}
          checked={filters.includeDeleted}
          onChange={(e) => change({ includeDeleted: e.target.checked })}
        />
      </FilterBar>
      <DataTable
        filterSummary={activeFilters.map((filter) => filter.label).join(' · ')}
        caption={t('nav.devices')}
        columns={columns}
        rows={list.items}
        rowKey={(d) => d.id}
        rowActions={(d) => [
          {
            id: 'open',
            label: t('contextMenu.open'),
            onSelect: () => navigate(`/devices/${encodeURIComponent(d.id)}`),
          },
          ...(ai.can('ai.use')
            ? [
                {
                  id: 'ask-ai',
                  label: t('ai.ask'),
                  onSelect: () => ai.open({ type: 'device', id: d.id }),
                },
              ]
            : []),
        ]}
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
