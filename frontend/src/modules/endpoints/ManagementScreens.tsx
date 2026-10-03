import { useState } from 'react';
import { useAsync, usePagedList } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link } from '../../platform/router/Router';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Checkbox, Select, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { endpointsApi } from './api';
import {
  artifactKinds,
  observationStates,
  type ArtifactFilters,
  type ManagementArtifact,
  type ManagementAssignment,
  type ManagementFilter,
  type ManagementFilterFilters,
  type ManagementObservation,
} from './types';

const artifactInitial: ArtifactFilters = { kind: '', q: '', platform: '', includeDeleted: false };
const filterInitial: ManagementFilterFilters = { q: '', platform: '', includeDeleted: false };

export function ManagementArtifactsScreen() {
  const { t, locale } = useI18n();
  const [filters, setFilters] = useState(artifactInitial);
  const change = (patch: Partial<ArtifactFilters>) =>
    setFilters((current) => ({ ...current, ...patch }));
  const list = usePagedList(
    (cursor, signal) => endpointsApi.artifacts(filters, cursor, signal),
    [filters],
  );
  const columns: Column<ManagementArtifact>[] = [
    {
      key: 'name',
      header: t('endpoints.name'),
      render: (item) => (
        <Link to={`/management-artifacts/${encodeURIComponent(item.id)}`}>{item.name}</Link>
      ),
    },
    {
      key: 'kind',
      header: t('management.kind'),
      render: (item) => t(`management.kind.${item.kind}` as MessageKey),
    },
    { key: 'platform', header: t('endpoints.platform'), render: (item) => item.platform },
    {
      key: 'source',
      header: t('endpoints.source'),
      render: (item) => t(`endpoints.source.${item.source}` as MessageKey),
    },
    {
      key: 'observed',
      header: t('endpoints.observedAt'),
      render: (item) => formatDateTime(locale, item.observedAt),
    },
    {
      key: 'synced',
      header: t('endpoints.lastSyncedAt'),
      render: (item) => formatDateTime(locale, item.lastSyncedAt),
    },
    {
      key: 'removed',
      header: t('endpoints.deletedObservedAt'),
      render: (item) =>
        item.deletedObservedAt ? formatDateTime(locale, item.deletedObservedAt) : '–',
    },
  ];
  return (
    <>
      <PageHeader title={t('nav.managementArtifacts')} />
      <form className="filters" role="search" onSubmit={(event) => event.preventDefault()}>
        <TextField
          label={t('management.search')}
          type="search"
          maxLength={100}
          value={filters.q}
          onChange={(event) => change({ q: event.target.value })}
        />
        <Select
          label={t('management.kind')}
          value={filters.kind}
          onChange={(event) => change({ kind: event.target.value })}
          options={[
            { value: '', label: t('filters.all') },
            ...artifactKinds.map((kind) => ({ value: kind, label: t(`management.kind.${kind}`) })),
          ]}
        />
        <TextField
          label={t('endpoints.platform')}
          value={filters.platform}
          onChange={(event) => change({ platform: event.target.value })}
        />
        <Checkbox
          label={t('management.includeDeleted')}
          checked={filters.includeDeleted}
          onChange={(event) => change({ includeDeleted: event.target.checked })}
        />
      </form>
      <DataTable
        caption={t('nav.managementArtifacts')}
        columns={columns}
        rows={list.items}
        rowKey={(item) => item.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('management.artifactsEmpty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </>
  );
}

export function ManagementFiltersScreen() {
  const { t, locale } = useI18n();
  const [filters, setFilters] = useState(filterInitial);
  const change = (patch: Partial<ManagementFilterFilters>) =>
    setFilters((current) => ({ ...current, ...patch }));
  const list = usePagedList(
    (cursor, signal) => endpointsApi.managementFilters(filters, cursor, signal),
    [filters],
  );
  const columns: Column<ManagementFilter>[] = [
    { key: 'name', header: t('endpoints.name'), render: (item) => item.name },
    { key: 'platform', header: t('endpoints.platform'), render: (item) => item.platform },
    { key: 'rule', header: t('management.rule'), render: (item) => item.rule },
    {
      key: 'source',
      header: t('endpoints.source'),
      render: (item) => t(`endpoints.source.${item.source}` as MessageKey),
    },
    {
      key: 'observed',
      header: t('endpoints.observedAt'),
      render: (item) => formatDateTime(locale, item.observedAt),
    },
    {
      key: 'synced',
      header: t('endpoints.lastSyncedAt'),
      render: (item) => formatDateTime(locale, item.lastSyncedAt),
    },
    {
      key: 'removed',
      header: t('endpoints.deletedObservedAt'),
      render: (item) =>
        item.deletedObservedAt ? formatDateTime(locale, item.deletedObservedAt) : '–',
    },
  ];
  return (
    <>
      <PageHeader title={t('nav.managementFilters')} />
      <form className="filters" role="search" onSubmit={(event) => event.preventDefault()}>
        <TextField
          label={t('management.search')}
          type="search"
          maxLength={100}
          value={filters.q}
          onChange={(event) => change({ q: event.target.value })}
        />
        <TextField
          label={t('endpoints.platform')}
          value={filters.platform}
          onChange={(event) => change({ platform: event.target.value })}
        />
        <Checkbox
          label={t('management.includeDeleted')}
          checked={filters.includeDeleted}
          onChange={(event) => change({ includeDeleted: event.target.checked })}
        />
      </form>
      <DataTable
        caption={t('nav.managementFilters')}
        columns={columns}
        rows={list.items}
        rowKey={(item) => item.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('management.filtersEmpty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </>
  );
}

export function ManagementArtifactDetailScreen({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const loaded = useAsync((signal) => endpointsApi.artifact(id, signal), [id]);
  if (loaded.error) return <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />;
  const artifact = loaded.data;
  if (!artifact)
    return (
      <p className="loading" role="status">
        {t('state.loading')}
      </p>
    );
  const date = (value: string | null) => (value ? formatDateTime(locale, value) : '–');
  const columns: Column<ManagementAssignment>[] = [
    {
      key: 'target',
      header: t('management.targetKind'),
      render: (a) => t(`management.target.${a.targetKind}` as MessageKey),
    },
    {
      key: 'group',
      header: t('management.groupId'),
      render: (a) => a.targetGroupExternalId ?? '–',
    },
    {
      key: 'mode',
      header: t('management.mode'),
      render: (a) => t(`management.mode.${a.mode}` as MessageKey),
    },
    {
      key: 'intent',
      header: t('management.intent'),
      render: (a) => t(`management.intent.${a.intent}` as MessageKey),
    },
    {
      key: 'filter',
      header: t('management.filter'),
      render: (a) =>
        a.filter
          ? `${a.filter.name} (${a.filter.rule})${a.filter.deleted ? ` — ${t('management.removed')}` : ''}`
          : '–',
    },
    {
      key: 'filterMode',
      header: t('management.filterMode'),
      render: (a) => t(`management.filterMode.${a.filterMode}` as MessageKey),
    },
    {
      key: 'current',
      header: t('management.status'),
      render: (a) => t(a.current ? 'management.current' : 'management.closed'),
    },
    { key: 'validFrom', header: t('management.validFrom'), render: (a) => date(a.validFrom) },
    { key: 'validUntil', header: t('management.validUntil'), render: (a) => date(a.validUntil) },
    {
      key: 'source',
      header: t('endpoints.source'),
      render: (a) => t(`endpoints.source.${a.source}` as MessageKey),
    },
    { key: 'observed', header: t('endpoints.observedAt'), render: (a) => date(a.observedAt) },
    { key: 'synced', header: t('endpoints.lastSyncedAt'), render: (a) => date(a.lastSyncedAt) },
  ];
  return (
    <>
      <PageHeader title={artifact.name} />
      <p>
        <Link to="/management-artifacts">{t('management.back')}</Link>
      </p>
      <dl className="facts">
        <dt>{t('management.kind')}</dt>
        <dd>{t(`management.kind.${artifact.kind}` as MessageKey)}</dd>
        <dt>{t('endpoints.provider')}</dt>
        <dd>{artifact.provider}</dd>
        <dt>{t('endpoints.externalId')}</dt>
        <dd>{artifact.externalId}</dd>
        <dt>{t('endpoints.platform')}</dt>
        <dd>{artifact.platform}</dd>
        <dt>{t('management.softwareProductId')}</dt>
        <dd>{artifact.softwareProductId ?? '–'}</dd>
        <dt>{t('management.revision')}</dt>
        <dd>{artifact.revision ?? '–'}</dd>
        <dt>{t('endpoints.version')}</dt>
        <dd>{artifact.version}</dd>
        <dt>{t('endpoints.source')}</dt>
        <dd>{t(`endpoints.source.${artifact.source}` as MessageKey)}</dd>
        <dt>{t('endpoints.observedAt')}</dt>
        <dd>{date(artifact.observedAt)}</dd>
        <dt>{t('endpoints.lastSyncedAt')}</dt>
        <dd>{date(artifact.lastSyncedAt)}</dd>
        <dt>{t('endpoints.deletedObservedAt')}</dt>
        <dd>{date(artifact.deletedObservedAt)}</dd>
      </dl>
      <section>
        <h2>{t('management.observationCounts')}</h2>
        <dl className="facts">
          {observationStates.map((state) => (
            <div key={state}>
              <dt>{t(`management.state.${state}`)}</dt>
              <dd>{artifact.observationCounts[state] ?? 0}</dd>
            </div>
          ))}
        </dl>
      </section>
      <section>
        <h2>{t('management.assignments')}</h2>
        <DataTable
          caption={t('management.assignments')}
          columns={columns}
          rows={artifact.assignments}
          rowKey={(a) => a.id}
          emptyText={t('management.assignmentsEmpty')}
        />
      </section>
    </>
  );
}

export function DeviceManagementSection({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const list = usePagedList(
    (cursor, signal) => endpointsApi.observations(id, cursor, signal),
    [id],
  );
  const columns: Column<ManagementObservation>[] = [
    {
      key: 'artifact',
      header: t('management.artifact'),
      render: (o) => (
        <Link to={`/management-artifacts/${encodeURIComponent(o.artifactId)}`}>
          {o.artifactName}
        </Link>
      ),
    },
    {
      key: 'state',
      header: t('management.state'),
      render: (o) => (
        <Badge
          tone={
            o.normalizedState === 'applied'
              ? 'success'
              : o.normalizedState === 'failed' || o.normalizedState === 'conflict'
                ? 'danger'
                : 'neutral'
          }
        >
          {t(`management.state.${o.normalizedState}` as MessageKey)}
        </Badge>
      ),
    },
    { key: 'raw', header: t('management.rawStatus'), render: (o) => o.rawStatus },
    {
      key: 'observed',
      header: t('endpoints.observedAt'),
      render: (o) => formatDateTime(locale, o.observedAt),
    },
  ];
  return (
    <section>
      <h2>{t('management.section')}</h2>
      <DataTable
        caption={t('management.section')}
        columns={columns}
        rows={list.items}
        rowKey={(o) => o.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('management.observationsEmpty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </section>
  );
}
