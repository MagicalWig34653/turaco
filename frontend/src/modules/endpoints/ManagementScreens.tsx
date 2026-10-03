import { useState } from 'react';
import { Dialog } from '../../platform/ui/Dialog';
import { useSession } from '../../platform/session/SessionProvider';
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
import { orderedCounts, visibleGroupName } from './viewHelpers';
import {
  artifactKinds,
  observationStates,
  type ArtifactFilters,
  type ManagementArtifact,
  type ManagementAssignment,
  type ManagementFilter,
  type ManagementFilterFilters,
  type DeviceManagementItem,
  type DeviceManagementFilters,
  type Expected,
  type Observed,
  type AssignedTarget,
  type Evaluation,
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
  const targets = useAsync((signal) => endpointsApi.artifactTargets(id, signal), [id]);
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
      <TargetsSection data={targets.data} error={targets.error} reload={targets.reload} />
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

const resultKeys = ['applicable', 'excluded', 'not_applicable', 'unknown'];
const stateKeys = [...observationStates, 'none'];
const mismatches = ['assigned_not_observed', 'expected_not_applied', 'observed_not_expected'];
const label = (t: ReturnType<typeof useI18n>['t'], prefix: string, value: string) =>
  t(`${prefix}.${value}` as MessageKey);

function ExpectedView({ value }: { value: Expected }) {
  const { t } = useI18n();
  return (
    <div>
      <Badge tone={value.result === 'applicable' ? 'success' : 'neutral'}>
        {label(t, 'management.result', value.result)}
      </Badge>{' '}
      {label(t, 'management.confidence', value.confidence)}
      <br />
      {value.reasons.map((reason) => label(t, 'management.reason', reason)).join(', ') || '–'}
    </div>
  );
}
function ObservedView({ value }: { value: Observed | null }) {
  const { t, locale } = useI18n();
  if (!value) return <span>{t('management.state.none')}</span>;
  return (
    <span>
      {label(t, 'management.state', value.state)} · {value.rawStatus} · {value.source}
      <br />
      {formatDateTime(locale, value.observedAt)} · {formatDateTime(locale, value.lastSyncedAt)} ·{' '}
      {t(value.stale ? 'management.stale' : 'management.fresh')}
    </span>
  );
}
function AssignedView({
  assigned,
  assignments,
}: {
  assigned: boolean;
  assignments: AssignedTarget[];
}) {
  const { t } = useI18n();
  return (
    <div>
      {t(assigned ? 'management.yes' : 'management.no')}
      {assignments.map((a) => (
        <div key={a.assignmentId}>
          {label(t, 'management.target', a.targetKind)} · {label(t, 'management.mode', a.mode)}
          {visibleGroupName(a.group) ? ` · ${visibleGroupName(a.group)}` : ''}
        </div>
      ))}
    </div>
  );
}
function EvaluationView({ value }: { value: Evaluation }) {
  const { t } = useI18n();
  return (
    <div>
      {value.shown ? (
        <>
          <div>
            {t('management.evaluated')}: {value.evaluated}
          </div>
          <div>
            {t('management.expected')}:{' '}
            {orderedCounts(resultKeys, value.expected)
              .map(({ key, count }) => `${label(t, 'management.result', key)} ${count}`)
              .join(' · ')}
          </div>
          <div>
            {t('management.observed')}:{' '}
            {stateKeys
              .map((k) => `${label(t, 'management.state', k)} ${value.observed[k] ?? 0}`)
              .join(' · ')}
          </div>
          {value.examples.map((x) => (
            <div key={x.deviceId}>
              <Link to={`/devices/${encodeURIComponent(x.deviceId)}`}>{x.name}</Link> ·{' '}
              {label(t, 'management.result', x.result)} ·{' '}
              {label(t, 'management.confidence', x.confidence)} ·{' '}
              {label(t, 'management.state', x.observed)}
            </div>
          ))}
          {value.truncated ? <p>{t('management.truncated')}</p> : null}
        </>
      ) : (
        t('management.evaluationHidden')
      )}
    </div>
  );
}
function TargetsSection({
  data,
  error,
  reload,
}: {
  data: Awaited<ReturnType<typeof endpointsApi.artifactTargets>> | undefined;
  error: Parameters<typeof ApiErrorAlert>[0]['error'] | undefined;
  reload: () => void;
}) {
  const { t } = useI18n();
  return (
    <section>
      <h2>{t('management.targets')}</h2>
      {error ? <ApiErrorAlert error={error} onRetry={reload} /> : null}
      {data ? (
        <>
          <h3>{t('management.assigned')}</h3>
          {data.assignments.length ? (
            data.assignments.map((a) => (
              <p key={a.assignmentId}>
                <AssignedView assigned assignments={[a]} />
              </p>
            ))
          ) : (
            <p>{t('management.assignmentsEmpty')}</p>
          )}
          <h3>{t('management.expected')}</h3>
          <EvaluationView value={data.evaluation} />
          <h3>{t('management.observedTotal')}</h3>
          <p>
            {stateKeys
              .map((k) => `${label(t, 'management.state', k)} ${data.observedTotal[k] ?? 0}`)
              .join(' · ')}
          </p>
        </>
      ) : null}
    </section>
  );
}
function WhyDialog({
  deviceId,
  artifactId,
  onClose,
}: {
  deviceId: string;
  artifactId: string;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const loaded = useAsync(
    (signal) => endpointsApi.assignmentPath(deviceId, artifactId, signal),
    [deviceId, artifactId],
  );
  return (
    <Dialog title={t('management.why')} onClose={onClose} wide>
      {loaded.error ? <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} /> : null}
      {loaded.data ? (
        <>
          <ExpectedView value={loaded.data.expected} />
          <ol>
            {loaded.data.path.map((step, i) => (
              <li key={i}>
                {label(t, 'management.step', step.kind)}
                {visibleGroupName(step.group) ? ` · ${visibleGroupName(step.group)}` : ''}
                {step.targetKind ? ` · ${label(t, 'management.target', step.targetKind)}` : ''}
                {step.mode ? ` · ${label(t, 'management.mode', step.mode)}` : ''}
                {step.filterResult ? ` · ${step.filterResult}` : ''}
                {step.result ? ` · ${label(t, 'management.result', step.result)}` : ''}
              </li>
            ))}
          </ol>
        </>
      ) : null}
      <button type="button" onClick={onClose}>
        {t('action.close')}
      </button>
    </Dialog>
  );
}
export function DeviceManagementSection({ id }: { id: string }) {
  const { t } = useI18n();
  const [filters, setFilters] = useState<DeviceManagementFilters>({
    kind: '',
    state: '',
    mismatch: '',
  });
  const [why, setWhy] = useState<string | null>(null);
  const summary = useAsync(
    (signal) => endpointsApi.deviceManagement(id, filters, undefined, signal),
    [id, filters],
  );
  const list = usePagedList(
    (cursor, signal) => endpointsApi.deviceManagement(id, filters, cursor, signal),
    [id, filters],
  );
  const columns: Column<DeviceManagementItem>[] = [
    {
      key: 'artifact',
      header: t('management.artifact'),
      render: (x) => (
        <Link to={`/management-artifacts/${encodeURIComponent(x.artifact.id)}`}>
          {x.artifact.name}
        </Link>
      ),
    },
    {
      key: 'assigned',
      header: t('management.assigned'),
      render: (x) => <AssignedView assigned={x.assigned} assignments={x.assignments} />,
    },
    {
      key: 'expected',
      header: t('management.expected'),
      render: (x) => (
        <>
          <ExpectedView value={x.expected} />
          <button type="button" onClick={() => setWhy(x.artifact.id)}>
            {t('management.why')}
          </button>
        </>
      ),
    },
    {
      key: 'observed',
      header: t('management.observed'),
      render: (x) => <ObservedView value={x.observed} />,
    },
    {
      key: 'mismatch',
      header: t('management.mismatch'),
      render: (x) => (x.mismatch ? label(t, 'management.mismatch', x.mismatch) : '–'),
    },
  ];
  return (
    <section>
      <h2>{t('management.section')}</h2>
      <div className="filters">
        <Select
          label={t('management.kind')}
          value={filters.kind}
          onChange={(e) => setFilters({ ...filters, kind: e.target.value })}
          options={[
            { value: '', label: t('filters.all') },
            ...artifactKinds.map((k) => ({ value: k, label: label(t, 'management.kind', k) })),
          ]}
        />
        <Select
          label={t('management.observed')}
          value={filters.state}
          onChange={(e) => setFilters({ ...filters, state: e.target.value })}
          options={[
            { value: '', label: t('filters.all') },
            ...stateKeys.map((k) => ({ value: k, label: label(t, 'management.state', k) })),
          ]}
        />
        <Select
          label={t('management.mismatch')}
          value={filters.mismatch}
          onChange={(e) => setFilters({ ...filters, mismatch: e.target.value })}
          options={[
            { value: '', label: t('filters.all') },
            ...mismatches.map((k) => ({ value: k, label: label(t, 'management.mismatch', k) })),
          ]}
        />
      </div>
      <DataTable
        caption={t('management.section')}
        columns={columns}
        rows={list.items}
        rowKey={(x) => x.artifact.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('management.observationsEmpty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
      {summary.data?.truncated ? <p>{t('management.truncated')}</p> : null}
      {why ? <WhyDialog deviceId={id} artifactId={why} onClose={() => setWhy(null)} /> : null}
    </section>
  );
}
export function DirectoryGroupManagementScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const summary = useAsync((signal) => endpointsApi.groupManagement(id, undefined, signal), [id]);
  const list = usePagedList(
    (cursor, signal) => endpointsApi.groupManagement(id, cursor, signal),
    [id],
  );
  return (
    <>
      <PageHeader title={summary.data?.name ?? t('management.groupPage')} />
      {summary.data ? (
        <p>
          {t('management.candidateDevices')}: {summary.data.candidateDevices}
          {summary.data.candidatesTruncated ? ` · ${t('management.truncated')}` : ''}
        </p>
      ) : null}
      <DataTable
        caption={t('management.groupPage')}
        rows={list.items}
        rowKey={(x) => x.artifact.id}
        columns={[
          {
            key: 'artifact',
            header: t('management.artifact'),
            render: (x) => (
              <Link to={`/management-artifacts/${encodeURIComponent(x.artifact.id)}`}>
                {x.artifact.name}
              </Link>
            ),
          },
          {
            key: 'assigned',
            header: t('management.assigned'),
            render: (x) => (
              <AssignedView assigned={x.assignments.length > 0} assignments={x.assignments} />
            ),
          },
          {
            key: 'evaluation',
            header: t('management.expected'),
            render: (x) => <EvaluationView value={x.evaluation} />,
          },
        ]}
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
export function UserManagementScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const { can } = useSession();
  const summary = useAsync((signal) => endpointsApi.userManagement(id, undefined, signal), [id]);
  const list = usePagedList(
    (cursor, signal) => endpointsApi.userManagement(id, cursor, signal),
    [id],
  );
  return (
    <>
      <PageHeader title={summary.data?.name ?? t('management.userPage')} />
      {summary.data && !summary.data.devicesShown ? (
        <p>{t('management.evaluationHidden')}</p>
      ) : null}
      {summary.data?.devicesTruncated || summary.data?.truncated ? (
        <p>{t('management.truncated')}</p>
      ) : null}
      <DataTable
        caption={t('management.userPage')}
        rows={list.items}
        rowKey={(x) => x.artifact.id}
        columns={[
          {
            key: 'artifact',
            header: t('management.artifact'),
            render: (x) => (
              <Link to={`/management-artifacts/${encodeURIComponent(x.artifact.id)}`}>
                {x.artifact.name}
              </Link>
            ),
          },
          {
            key: 'assigned',
            header: t('management.assigned'),
            render: (x) => (
              <AssignedView assigned={x.targeting.length > 0} assignments={x.targeting} />
            ),
          },
          {
            key: 'userResult',
            header: t('management.expected'),
            render: (x) => label(t, 'management.result', x.userResult),
          },
          {
            key: 'devices',
            header: t('management.devices'),
            render: (x) =>
              x.devices.map((d) => (
                <div key={d.deviceId}>
                  {can('endpoints.view') || can('endpoints.manage') ? (
                    <Link to={`/devices/${encodeURIComponent(d.deviceId)}`}>{d.name}</Link>
                  ) : (
                    d.name
                  )}
                  <ExpectedView value={d.expected} />
                  <ObservedView value={d.observed} />
                </div>
              )),
          },
        ]}
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
