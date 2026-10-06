import { TableDate } from '../../platform/ui/TableDate';
import { FilterBar } from '../../platform/ui/FilterBar';
import { useEffect, useState } from 'react';
import { useAsync, usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { organizationApi } from '../organization/api';
import { Link, navigate, useLocation } from '../../platform/router/Router';
import { Badge } from '../../platform/ui/Alert';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Checkbox, Select, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Button } from '../../platform/ui/Button';
import { endpointsApi } from './api';
import { diffBadge, historyChanges, visibleGroupName } from './viewHelpers';
import {
  artifactKinds,
  type AssignedTarget,
  type DiffFilters,
  type DiffSide,
  type HistoryEntry,
} from './types';

const label = (t: ReturnType<typeof useI18n>['t'], prefix: string, value: string) =>
  t(`${prefix}.${value}` as MessageKey);

export function HistorySection({ id, type }: { id: string; type: 'device' | 'artifact' }) {
  const { t } = useI18n();
  const list = usePagedList(
    (cursor, signal) =>
      type === 'device'
        ? endpointsApi.deviceHistory(id, cursor, signal)
        : endpointsApi.artifactHistory(id, cursor, signal),
    [id, type],
  );
  const columns: Column<HistoryEntry>[] = [
    {
      key: 'kind',
      header: t('management.historyKind'),
      render: (x) => label(t, 'management.history', x.kind),
    },
    {
      key: 'occurred',
      header: t('management.occurredAt'),
      render: (x) => <TableDate value={x.occurredAt} />,
    },
    { key: 'source', header: t('endpoints.source'), render: (x) => x.source },
    {
      key: 'observed',
      header: t('endpoints.observedAt'),
      render: (x) => <TableDate value={x.observedAt} />,
    },
    {
      key: 'detail',
      header: t('management.changedFields'),
      render: (x) => (
        <>
          {x.artifact?.name ?? ''}
          {x.assignment ? (
            <div>
              {historyChanges(x.assignment.changes)
                .map((field) => label(t, 'management.change', field))
                .join(', ') || '–'}
              {x.assignment.previous ? (
                <span>
                  {' '}
                  · {t('management.previous')}: {x.assignment.previous.mode} /{' '}
                  {x.assignment.previous.intent}
                </span>
              ) : null}
              <span>
                {' '}
                · {t('management.current')}: {x.assignment.current.mode} /{' '}
                {x.assignment.current.intent}
              </span>
              {visibleGroupName(x.assignment.current.group)
                ? ` · ${visibleGroupName(x.assignment.current.group)}`
                : ''}
            </div>
          ) : null}
          {x.observation ? (
            <div>
              {x.observation.previousState
                ? `${label(t, 'management.state', x.observation.previousState)} → `
                : ''}
              {label(t, 'management.state', x.observation.state)} · {x.observation.rawStatus}
            </div>
          ) : null}
          {visibleGroupName(x.group ?? null) ? (
            <div>{visibleGroupName(x.group ?? null)}</div>
          ) : null}
        </>
      ),
    },
  ];
  return (
    <section>
      <h2>{t('management.historyTitle')}</h2>
      <DataTable
        caption={t('management.historyTitle')}
        columns={columns}
        rows={list.items}
        rowKey={(x) =>
          `${x.kind}:${x.occurredAt}:${x.assignment?.assignmentId ?? x.artifact?.id ?? x.group?.externalId ?? x.observation?.rawStatus ?? ''}`
        }
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('management.historyEmpty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </section>
  );
}

function ClassBadge({ value, uncertain }: { value: string; uncertain?: boolean }) {
  const { t } = useI18n();
  const shown = diffBadge(value, !!uncertain);
  return (
    <Badge tone={shown === 'same' ? 'success' : 'neutral'}>
      {label(t, 'management.diffClass', shown)}
    </Badge>
  );
}

function Assignments({ items }: { items: AssignedTarget[] }) {
  const { t } = useI18n();
  return (
    <>
      {items.map((item) => (
        <div key={item.assignmentId}>
          {label(t, 'management.target', item.targetKind)} ·{' '}
          {label(t, 'management.mode', item.mode)} · {label(t, 'management.intent', item.intent)}
          {visibleGroupName(item.group) ? ` · ${visibleGroupName(item.group)}` : ''}
          {item.filterMode !== 'none'
            ? ` · ${t('management.filter')}: ${label(t, 'management.filterMode', item.filterMode)}${item.filter?.name ? ` (${item.filter.name})` : ''}`
            : ''}
        </div>
      ))}
    </>
  );
}
function Side({ value }: { value: DiffSide }) {
  const { t } = useI18n();
  return (
    <div>
      <div>
        <strong>{t('management.assigned')}:</strong>{' '}
        {value.assignedUnknown
          ? label(t, 'management.result', 'unknown')
          : t(value.assigned ? 'management.yes' : 'management.no')}
        <Assignments items={value.assignments} />
      </div>
      <div>
        <strong>{t('management.expected')}:</strong>{' '}
        {label(t, 'management.result', value.expected.result)} ·{' '}
        {label(t, 'management.confidence', value.expected.confidence)}
      </div>
      <div>
        <strong>{t('management.observed')}:</strong>{' '}
        {value.observed
          ? label(t, 'management.state', value.observed.state)
          : t('management.state.none')}
      </div>
    </div>
  );
}
function DiffFiltersView({
  filters,
  onChange,
}: {
  filters: DiffFilters;
  onChange: (value: DiffFilters) => void;
}) {
  const { t } = useI18n();
  return (
    <FilterBar>
      <Select
        label={t('management.kind')}
        value={filters.kind}
        onChange={(e) => onChange({ ...filters, kind: e.target.value })}
        options={[
          { value: '', label: t('filters.all') },
          ...artifactKinds.map((kind) => ({
            value: kind,
            label: label(t, 'management.kind', kind),
          })),
        ]}
      />
      <Checkbox
        label={t('management.differencesOnly')}
        checked={filters.differences}
        onChange={(e) => onChange({ ...filters, differences: e.target.checked })}
      />
    </FilterBar>
  );
}

export function DeviceDiffScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const { search } = useLocation();
  const other = new URLSearchParams(search).get('other') ?? '';
  const [query, setQuery] = useState(other);
  useEffect(() => setQuery(other), [other]);
  const [filters, setFilters] = useState<DiffFilters>({ kind: '', differences: false });
  const candidates = useAsync(
    (signal) =>
      endpointsApi.devices(
        {
          platform: '',
          compliance: '',
          linked: '',
          includeDeleted: false,
          q: query,
          managementState: '',
          hasFinding: '',
          osVersion: '',
          lastCheckinOlderThanDays: '',
        },
        undefined,
        signal,
      ),
    [query],
  );
  const list = usePagedList(
    (cursor, signal) =>
      other
        ? endpointsApi.deviceDiff(id, other, filters, cursor, signal)
        : Promise.resolve({ items: [] }),
    [id, other, filters],
  );
  const summary = useAsync(
    (signal) =>
      other
        ? endpointsApi.deviceDiff(id, other, filters, undefined, signal)
        : Promise.resolve(undefined),
    [id, other, filters],
  );
  const columns: Column<NonNullable<typeof summary.data>['items'][number]>[] = [
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
      key: 'left',
      header: summary.data?.left.name ?? t('management.left'),
      render: (x) => <Side value={x.left} />,
    },
    {
      key: 'right',
      header: summary.data?.right.name ?? t('management.right'),
      render: (x) => <Side value={x.right} />,
    },
    {
      key: 'class',
      header: t('management.diffClass'),
      render: (x) => (
        <>
          <ClassBadge value={x.class} uncertain={x.uncertain} />
          <div>
            {Object.entries(x.dimensions)
              .map(
                ([key, value]) =>
                  `${label(t, 'management.dimension', key)}: ${label(t, 'management.dimension', value)}`,
              )
              .join(' · ')}
          </div>
        </>
      ),
    },
  ];
  return (
    <>
      <PageHeader title={t('management.compareDevices')} />
      <p>
        <Link to={`/devices/${encodeURIComponent(id)}`}>{t('endpoints.back')}</Link>
      </p>
      <FilterBar
        onSubmit={(e) => {
          e.preventDefault();
          navigate(
            `/endpoints/devices/${encodeURIComponent(id)}/diff?other=${encodeURIComponent(query.trim())}`,
          );
        }}
      >
        <TextField
          label={t('management.otherDevice')}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <Button type="submit">{t('management.compare')}</Button>
      </FilterBar>
      {candidates.data ? (
        <div className="filters">
          {candidates.data.items
            .filter((x) => x.id !== id)
            .map((x) => (
              <Link
                key={x.id}
                to={`/endpoints/devices/${encodeURIComponent(id)}/diff?other=${encodeURIComponent(x.id)}`}
              >
                {x.name}
              </Link>
            ))}
        </div>
      ) : null}
      {other ? (
        <>
          <DiffFiltersView filters={filters} onChange={setFilters} />
          {summary.data?.truncated ? <p>{t('management.truncated')}</p> : null}
          <DataTable
            caption={t('management.compareDevices')}
            columns={columns}
            rows={list.items}
            rowKey={(x) => x.artifact.id}
            loading={list.loading}
            error={list.error}
            onRetry={list.reload}
            emptyText={t('management.diffEmpty')}
            hasMore={list.hasMore}
            loadingMore={list.loadingMore}
            loadMoreError={list.loadMoreError}
            onLoadMore={list.loadMore}
          />
        </>
      ) : null}
    </>
  );
}

export function GroupDiffScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const { search } = useLocation();
  const other = new URLSearchParams(search).get('other') ?? '';
  const [query, setQuery] = useState(other);
  useEffect(() => setQuery(other), [other]);
  const [filters, setFilters] = useState<DiffFilters>({ kind: '', differences: false });
  const candidates = useAsync(
    (signal) => organizationApi.searchDirectoryGroups(query, signal),
    [query],
  );
  const list = usePagedList(
    (cursor, signal) =>
      other
        ? endpointsApi.groupDiff(id, other, filters, cursor, signal)
        : Promise.resolve({ items: [] }),
    [id, other, filters],
  );
  const summary = useAsync(
    (signal) =>
      other
        ? endpointsApi.groupDiff(id, other, filters, undefined, signal)
        : Promise.resolve(undefined),
    [id, other, filters],
  );
  const columns: Column<NonNullable<typeof summary.data>['items'][number]>[] = [
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
      key: 'left',
      header: summary.data?.left.name ?? t('management.left'),
      render: (x) => <Assignments items={x.left} />,
    },
    {
      key: 'right',
      header: summary.data?.right.name ?? t('management.right'),
      render: (x) => <Assignments items={x.right} />,
    },
    {
      key: 'class',
      header: t('management.diffClass'),
      render: (x) => (
        <>
          <ClassBadge value={x.class} />
          {x.differences.length ? (
            <div>{x.differences.map((d) => label(t, 'management.change', d)).join(', ')}</div>
          ) : null}
        </>
      ),
    },
  ];
  return (
    <>
      <PageHeader title={t('management.compareGroups')} />
      <p>
        <Link to={`/endpoints/groups/${encodeURIComponent(id)}`}>{t('management.back')}</Link>
      </p>
      <FilterBar
        onSubmit={(e) => {
          e.preventDefault();
          navigate(
            `/endpoints/groups/${encodeURIComponent(id)}/diff?other=${encodeURIComponent(query.trim())}`,
          );
        }}
      >
        <TextField
          label={t('management.otherGroup')}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <Button type="submit">{t('management.compare')}</Button>
      </FilterBar>
      {candidates.data ? (
        <div className="filters">
          {candidates.data.items
            .filter((x) => x.id !== id)
            .map((x) => (
              <Link
                key={x.id}
                to={`/endpoints/groups/${encodeURIComponent(id)}/diff?other=${encodeURIComponent(x.id)}`}
              >
                {x.displayName}
              </Link>
            ))}
        </div>
      ) : null}
      {other ? (
        <>
          <DiffFiltersView filters={filters} onChange={setFilters} />
          {summary.data?.truncated ? <p>{t('management.truncated')}</p> : null}
          <DataTable
            caption={t('management.compareGroups')}
            columns={columns}
            rows={list.items}
            rowKey={(x) => x.artifact.id}
            loading={list.loading}
            error={list.error}
            onRetry={list.reload}
            emptyText={t('management.diffEmpty')}
            hasMore={list.hasMore}
            loadingMore={list.loadingMore}
            loadMoreError={list.loadMoreError}
            onLoadMore={list.loadMore}
          />
        </>
      ) : null}
    </>
  );
}
