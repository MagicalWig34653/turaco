import { useState, type FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync, usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { FilterBar } from '../../platform/ui/FilterBar';
import { Select, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { TableDate } from '../../platform/ui/TableDate';
import { useFilterQuery } from '../../platform/ui/useFilterQuery';
import { EmptyState, StatusBadge } from '../../platform/ui/Workspace';
import { softwareApi } from './api';
import {
  AttemptCounts,
  HashChip,
  HashMismatchBadge,
  PackageStatusBadge,
  PublishedAfterRevokeBadge,
} from './components';
import { isProviderNotConfigured, publishBlocker } from './helpers';
import {
  packageStatuses,
  type CatalogEntry,
  type PackageSyncResult,
  type SoftwarePackage,
} from './types';
import { PublishDialog } from './VersionScreens';

const enc = encodeURIComponent;

export function SoftwarePackagesScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const [status, setStatus] = useState(
    () => new URLSearchParams(window.location.search).get('status') ?? '',
  );
  useFilterQuery({ status });
  const list = usePagedList(
    (cursor, signal) => softwareApi.packages({ status }, cursor, signal),
    [status],
  );
  const [publishing, setPublishing] = useState<SoftwarePackage>();
  const [syncing, setSyncing] = useState(false);
  const [syncResult, setSyncResult] = useState<PackageSyncResult>();
  const [syncError, setSyncError] = useState<ApiError>();
  const canPackage = can('software.package');
  const canSeeArtifacts = can('endpoint.management.view') || can('endpoints.manage');

  const sync = async () => {
    setSyncing(true);
    setSyncError(undefined);
    setSyncResult(undefined);
    try {
      setSyncResult(await softwareApi.sync());
      list.reload();
    } catch (cause) {
      setSyncError(asApiError(cause));
    } finally {
      setSyncing(false);
    }
  };

  const activeFilters = status
    ? [
        {
          key: 'status',
          label: t(`software.packageStatus.${status}` as MessageKey),
          onRemove: () => setStatus(''),
        },
      ]
    : [];

  const columns: Column<SoftwarePackage>[] = [
    {
      key: 'version',
      header: t('software.package.version'),
      render: (pkg) => (
        <Link to={`/software/versions/${enc(pkg.versionId)}`}>
          {t('software.package.openVersion')}
        </Link>
      ),
    },
    {
      key: 'provider',
      header: t('software.package.provider'),
      sortValue: (pkg) => pkg.provider,
      render: (pkg) => (
        <>
          {pkg.provider}
          {pkg.providerPackageId ? (
            <span className="software-muted software-mono"> {pkg.providerPackageId}</span>
          ) : null}
        </>
      ),
    },
    {
      key: 'status',
      header: t('software.package.status'),
      sortValue: (pkg) => pkg.status,
      render: (pkg) => (
        <>
          <span className="software-badges">
            <PackageStatusBadge status={pkg.status} />
            {pkg.hashMismatch ? <HashMismatchBadge /> : null}
            {pkg.publishedAfterRevoke ? <PublishedAfterRevokeBadge /> : null}
            {pkg.versionRevoked ? (
              <StatusBadge tone="danger">{t('software.package.versionRevoked')}</StatusBadge>
            ) : null}
            {pkg.productBlocked ? (
              <StatusBadge tone="danger">{t('software.package.productBlocked')}</StatusBadge>
            ) : null}
          </span>
          <AttemptCounts packageAttempt={pkg.packageAttempt} publishAttempt={pkg.publishAttempt} />
        </>
      ),
    },
    {
      key: 'hash',
      header: t('software.package.reportedHash'),
      render: (pkg) =>
        pkg.installerSha256 ? (
          <HashChip short value={pkg.installerSha256} label={t('software.package.reportedHash')} />
        ) : (
          t('software.package.notReported')
        ),
    },
    {
      key: 'artifact',
      header: t('software.package.artifact'),
      render: (pkg) =>
        pkg.managementArtifactId && canSeeArtifacts ? (
          <Link to={`/management-artifacts/${enc(pkg.managementArtifactId)}`}>
            {t('software.package.openArtifact')}
          </Link>
        ) : pkg.managementArtifactExternalId ? (
          t('software.package.artifactPending')
        ) : (
          '—'
        ),
    },
    {
      key: 'observed',
      header: t('software.package.observedAt'),
      sortValue: (pkg) => pkg.observedAt ?? '',
      render: (pkg) => (
        <>
          <TableDate value={pkg.observedAt} />
          <span className="software-muted">
            {' '}
            {t(`software.source.${pkg.source}` as MessageKey)}
          </span>
        </>
      ),
    },
    {
      key: 'synced',
      header: t('software.package.lastSyncedAt'),
      sortValue: (pkg) => pkg.lastSyncedAt ?? '',
      render: (pkg) => <TableDate value={pkg.lastSyncedAt} />,
    },
  ];

  return (
    <div className="software-workspace">
      <PageHeader
        eyebrow={t('software.eyebrow')}
        title={t('software.packages.title')}
        intro={t('software.packages.intro')}
        actions={
          canPackage ? (
            <Button variant="primary" busy={syncing} onClick={() => void sync()}>
              {t('software.packages.sync')}
            </Button>
          ) : undefined
        }
      />
      {syncError ? <ApiErrorAlert error={syncError} /> : null}
      {syncResult ? (
        <Alert kind="success">{t('software.packages.syncResult', { ...syncResult })}</Alert>
      ) : null}
      <FilterBar activeFilters={activeFilters} onClear={() => setStatus('')}>
        <Select
          label={t('software.package.status')}
          value={status}
          onChange={(event) => setStatus(event.target.value)}
          options={[
            { value: '', label: t('filters.all') },
            ...packageStatuses.map((value) => ({
              value,
              label: t(`software.packageStatus.${value}` as MessageKey),
            })),
          ]}
        />
      </FilterBar>
      <DataTable
        caption={t('software.packages.title')}
        filterSummary={activeFilters.map((filter) => filter.label).join(' · ')}
        columns={columns}
        rows={list.items}
        rowKey={(pkg) => pkg.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('software.packages.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
        rowActions={(pkg) => {
          const blocker = publishBlocker(pkg);
          return [
            {
              id: 'open',
              label: t('software.package.openVersion'),
              onSelect: () => navigate(`/software/versions/${enc(pkg.versionId)}`),
            },
            ...(canPackage
              ? [
                  {
                    id: 'publish',
                    label: t('software.packageOp.publish'),
                    ...(blocker ? { disabledReason: t(blocker) } : {}),
                    onSelect: () => setPublishing(pkg),
                  },
                ]
              : []),
          ];
        }}
      />
      {publishing ? (
        <PublishDialog
          pkg={publishing}
          onClose={() => setPublishing(undefined)}
          onDone={() => {
            setPublishing(undefined);
            list.reload();
          }}
        />
      ) : null}
    </div>
  );
}

export function SoftwareCatalogScreen() {
  const { t } = useI18n();
  const [input, setInput] = useState(
    () => new URLSearchParams(window.location.search).get('q') ?? '',
  );
  const [query, setQuery] = useState(input.trim());
  useFilterQuery({ q: query });
  const tooShort = query.length > 0 && query.length < 2;
  const result = useAsync(
    (signal) =>
      query.length >= 2 ? softwareApi.catalog(query, signal) : Promise.resolve(undefined),
    [query],
  );
  const notConfigured = isProviderNotConfigured(result.error);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setQuery(input.trim());
  };

  const columns: Column<CatalogEntry>[] = [
    {
      key: 'name',
      header: t('software.product.name'),
      sortValue: (entry) => entry.name,
      render: (entry) => entry.name,
    },
    {
      key: 'publisher',
      header: t('software.publisher'),
      sortValue: (entry) => entry.publisher ?? '',
      render: (entry) => entry.publisher ?? '—',
    },
    {
      key: 'latest',
      header: t('software.catalog.latestVersion'),
      render: (entry) => entry.latestVersion ?? '—',
    },
    {
      key: 'source',
      header: t('software.catalog.source'),
      render: (entry) =>
        entry.sourceUrl ? (
          <a href={entry.sourceUrl} target="_blank" rel="noopener noreferrer">
            {t('software.catalog.openSource')}
          </a>
        ) : (
          '—'
        ),
    },
  ];

  return (
    <div className="software-workspace">
      <PageHeader
        eyebrow={t('software.eyebrow')}
        title={t('software.catalog.title')}
        intro={t('software.catalog.intro')}
      />
      <form className="software-search" role="search" onSubmit={submit}>
        <TextField
          label={t('software.catalog.search')}
          type="search"
          value={input}
          minLength={2}
          maxLength={100}
          hint={t('software.catalog.searchHint')}
          error={tooShort ? t('software.catalog.tooShort') : undefined}
          onChange={(event) => setInput(event.target.value)}
        />
        <Button type="submit" variant="primary" busy={result.loading && query.length >= 2}>
          {t('software.catalog.searchAction')}
        </Button>
      </form>
      {notConfigured ? (
        <EmptyState
          title={t('software.catalog.notConfiguredTitle')}
          description={t('software.catalog.notConfigured')}
        />
      ) : result.error ? (
        <ApiErrorAlert error={result.error} onRetry={result.reload} />
      ) : query.length < 2 ? (
        <EmptyState
          title={t('software.catalog.startTitle')}
          description={t('software.catalog.start')}
        />
      ) : (
        <DataTable
          caption={t('software.catalog.results', { query })}
          columns={columns}
          rows={result.data?.items ?? []}
          rowKey={(entry) => entry.providerId}
          loading={result.loading}
          emptyText={t('software.catalog.empty')}
        />
      )}
    </div>
  );
}
