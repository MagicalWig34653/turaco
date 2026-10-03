import { useState } from 'react';
import { usePagedList } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link } from '../../platform/router/Router';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Select, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { endpointsApi } from './api';
import { findingKinds, type Finding, type FindingFilters } from './types';

export function FindingsScreen() {
  const { t, locale } = useI18n();
  const [filters, setFilters] = useState<FindingFilters>(() => ({
    kind: '',
    status: 'open',
    deviceId: new URLSearchParams(window.location.search).get('deviceId') ?? '',
  }));
  const change = (patch: Partial<FindingFilters>) =>
    setFilters((current) => ({ ...current, ...patch }));
  const list = usePagedList(
    (cursor, signal) => endpointsApi.findings(filters, cursor, signal),
    [filters],
  );
  const columns: Column<Finding>[] = [
    {
      key: 'device',
      header: t('endpoints.name'),
      render: (f) => <Link to={`/devices/${encodeURIComponent(f.deviceId)}`}>{f.deviceName}</Link>,
    },
    {
      key: 'kind',
      header: t('endpoints.findingKind'),
      render: (f) => (
        <>
          {t(`endpoints.finding.${f.kind}` as MessageKey)}
          {f.kind === 'assignment_ineffective' ? (
            <small> · {t('endpoints.finding.derivedNote')}</small>
          ) : null}
        </>
      ),
    },
    {
      key: 'status',
      header: t('endpoints.findingStatus'),
      render: (f) => t(`endpoints.findingStatus.${f.status}` as MessageKey),
    },
    {
      key: 'raised',
      header: t('endpoints.raisedAt'),
      render: (f) => formatDateTime(locale, f.raisedAt),
    },
    {
      key: 'resolved',
      header: t('endpoints.resolvedAt'),
      render: (f) => (f.resolvedAt ? formatDateTime(locale, f.resolvedAt) : '–'),
    },
  ];
  return (
    <>
      <PageHeader title={t('nav.endpointFindings')} />
      <form className="filters" role="search" onSubmit={(e) => e.preventDefault()}>
        <Select
          label={t('endpoints.findingKind')}
          value={filters.kind}
          onChange={(e) => change({ kind: e.target.value })}
          options={[
            { value: '', label: t('filters.all') },
            ...findingKinds.map((value) => ({ value, label: t(`endpoints.finding.${value}`) })),
          ]}
        />
        <Select
          label={t('endpoints.findingStatus')}
          value={filters.status}
          onChange={(e) => change({ status: e.target.value })}
          options={[
            { value: 'open', label: t('endpoints.findingStatus.open') },
            { value: 'resolved', label: t('endpoints.findingStatus.resolved') },
          ]}
        />
        <TextField
          label={t('endpoints.deviceId')}
          value={filters.deviceId}
          onChange={(e) => change({ deviceId: e.target.value })}
        />
      </form>
      <DataTable
        caption={t('nav.endpointFindings')}
        columns={columns}
        rows={list.items}
        rowKey={(f) => f.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('endpoints.findingsEmpty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </>
  );
}
