import { useState } from 'react';
import { usePagedList, useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Checkbox, Select } from '../../platform/ui/Field';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { FilterBar } from '../../platform/ui/FilterBar';
import { PageHeader } from '../../platform/ui/PageHeader';
import { TableDate } from '../../platform/ui/TableDate';
import { Card, Skeleton } from '../../platform/ui/Workspace';
import { useFilterQuery } from '../../platform/ui/useFilterQuery';
import { remoteAccessApi } from './api';
import { reasonKey, sessionFacts } from './model';
import { SessionPanel, SessionStatusBadge, UserName } from './SessionPanel';
import { sessionStatuses, type RemoteSession, type Transition } from './types';

function initialFilters() {
  const params = new URLSearchParams(window.location.search);
  return {
    status: params.get('status') ?? '',
    deviceId: params.get('deviceId') ?? '',
    ticketId: params.get('ticketId') ?? '',
  };
}

export function SessionsScreen() {
  const { t, locale } = useI18n();
  const { can, session: auth } = useSession();
  const seeAll = can('remote_access.view_sessions');
  const [filters, setFilters] = useState(initialFilters);
  const [mine, setMine] = useState(!seeAll);
  useFilterQuery(filters);
  const summary = useAsync(
    (signal) => (seeAll ? remoteAccessApi.observationsSummary(signal) : Promise.resolve(undefined)),
    [seeAll],
  );
  const list = usePagedList(
    (cursor, signal) =>
      remoteAccessApi.sessions(
        { ...filters, ...(mine && auth ? { initiatedBy: auth.userId } : {}) },
        cursor,
        signal,
      ),
    [filters, mine],
  );
  const change = (patch: Partial<typeof filters>) =>
    setFilters((current) => ({ ...current, ...patch }));
  const activeFilters = [
    ...(filters.status
      ? [
          {
            key: 'status',
            label: `${t('remoteaccess.col.status')}: ${t(`remoteaccess.status.${filters.status}` as MessageKey)}`,
            onRemove: () => change({ status: '' }),
          },
        ]
      : []),
    ...(filters.deviceId
      ? [
          {
            key: 'deviceId',
            label: t('remoteaccess.filter.device'),
            onRemove: () => change({ deviceId: '' }),
          },
        ]
      : []),
    ...(filters.ticketId
      ? [
          {
            key: 'ticketId',
            label: t('remoteaccess.filter.ticket'),
            onRemove: () => change({ ticketId: '' }),
          },
        ]
      : []),
  ];
  const columns: Column<RemoteSession>[] = [
    {
      key: 'reference',
      header: t('remoteaccess.col.reference'),
      render: (s) => (
        <Link to={`/remote-access/sessions/${encodeURIComponent(s.id)}`}>{s.reference}</Link>
      ),
    },
    {
      key: 'status',
      header: t('remoteaccess.col.status'),
      sortValue: (s) => s.status,
      render: (s) => <SessionStatusBadge status={s.status} />,
    },
    {
      key: 'provider',
      header: t('remoteaccess.provider'),
      render: (s) => t(`remoteaccess.provider.${s.provider}` as MessageKey),
    },
    {
      key: 'device',
      header: t('remoteaccess.device'),
      render: (s) => (
        <Link to={`/devices/${encodeURIComponent(s.deviceId)}`}>
          {t('remoteaccess.openDevice')}
        </Link>
      ),
    },
    {
      key: 'ticket',
      header: t('remoteaccess.ticket'),
      render: (s) => (
        <Link to={`/support/${encodeURIComponent(s.ticketId)}`}>
          {t('remoteaccess.openTicket')}
        </Link>
      ),
    },
    {
      key: 'by',
      header: t('remoteaccess.initiatedBy'),
      render: (s) => <UserName id={s.initiatedBy} />,
    },
    {
      key: 'created',
      header: t('remoteaccess.col.created'),
      sortValue: (s) => s.createdAt,
      render: (s) => <TableDate value={s.createdAt} />,
    },
  ];
  return (
    <>
      <PageHeader title={t('remoteaccess.sessions.title')} />
      {seeAll && summary.data && summary.data.unattributedRecords > 0 ? (
        <p role="note">
          {t('remoteaccess.observations.note', {
            count: summary.data.unattributedRecords,
            since: formatDateTime(locale, summary.data.since),
          })}
        </p>
      ) : null}
      <FilterBar
        activeFilters={activeFilters}
        onClear={() => setFilters({ status: '', deviceId: '', ticketId: '' })}
        role="search"
        onSubmit={(event) => event.preventDefault()}
      >
        <Select
          label={t('remoteaccess.col.status')}
          value={filters.status}
          onChange={(event) => change({ status: event.target.value })}
          options={[
            { value: '', label: t('filters.all') },
            ...sessionStatuses.map((value) => ({
              value,
              label: t(`remoteaccess.status.${value}` as MessageKey),
            })),
          ]}
        />
        {seeAll ? (
          <Checkbox
            label={t('remoteaccess.filter.mine')}
            checked={mine}
            onChange={(event) => setMine(event.target.checked)}
          />
        ) : null}
      </FilterBar>
      <DataTable
        caption={t('remoteaccess.sessions.title')}
        filterSummary={activeFilters.map((f) => f.label).join(' · ')}
        columns={columns}
        rows={list.items}
        rowKey={(s) => s.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('remoteaccess.sessions.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </>
  );
}

export function SessionDetailScreen({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const loaded = useAsync((signal) => remoteAccessApi.session(id, signal), [id]);
  if (loaded.error) return <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />;
  if (!loaded.data)
    return (
      <Card>
        <Skeleton lines={6} />
      </Card>
    );
  const { session: s, transitions } = loaded.data;
  const date = (value: string | null) => (value ? formatDateTime(locale, value) : null);
  const unknown = t('remoteaccess.unknown');
  const columns: Column<Transition>[] = [
    {
      key: 'at',
      header: t('remoteaccess.col.created'),
      render: (x) => <TableDate value={x.createdAt} />,
    },
    {
      key: 'to',
      header: t('remoteaccess.col.status'),
      render: (x) => t(`remoteaccess.status.${x.toStatus}` as MessageKey),
    },
    { key: 'op', header: t('remoteaccess.col.operation'), render: (x) => x.operation },
    { key: 'reason', header: t('remoteaccess.reason'), render: (x) => x.reason ?? '–' },
    {
      key: 'actor',
      header: t('remoteaccess.col.actor'),
      render: (x) => (x.actorUserId ? <UserName id={x.actorUserId} /> : (x.actorSystem ?? '–')),
    },
  ];
  return (
    <>
      <PageHeader title={s.reference} />
      <p>
        <Link to="/remote-access/sessions">{t('remoteaccess.back')}</Link>
      </p>
      <Card title={t('remoteaccess.card.title')} className="ra-card">
        <h2>{t('remoteaccess.card.title')}</h2>
        <SessionPanel session={s} onChanged={loaded.reload} />
      </Card>
      <Card title={t('remoteaccess.facts')}>
        <h2>{t('remoteaccess.facts')}</h2>
        <dl className="facts">
          <dt>{t('remoteaccess.provider')}</dt>
          <dd>{t(`remoteaccess.provider.${s.provider}` as MessageKey)}</dd>
          <dt>{t('remoteaccess.initiatedBy')}</dt>
          <dd>
            <UserName id={s.initiatedBy} />
          </dd>
          <dt>{t('remoteaccess.device')}</dt>
          <dd>
            <Link to={`/devices/${encodeURIComponent(s.deviceId)}`}>
              {t('remoteaccess.openDevice')}
            </Link>
          </dd>
          <dt>{t('remoteaccess.ticket')}</dt>
          <dd>
            <Link to={`/support/${encodeURIComponent(s.ticketId)}`}>
              {t('remoteaccess.openTicket')}
            </Link>
          </dd>
          <dt>{t('remoteaccess.consent')}</dt>
          <dd>
            {t(`remoteaccess.consent.${s.consent}` as MessageKey)}
            {s.consentRecordedAt ? ` · ${date(s.consentRecordedAt) ?? ''}` : ''}
          </dd>
          {s.mismatchReason ? (
            <>
              <dt>{t('remoteaccess.mismatchReason')}</dt>
              <dd>{t(`remoteaccess.mismatch.${s.mismatchReason}` as MessageKey)}</dd>
            </>
          ) : null}
          {s.statusReason ? (
            <>
              <dt>{t('remoteaccess.statusReason')}</dt>
              <dd>{t(reasonKey(s.statusReason))}</dd>
            </>
          ) : null}
          <dt>{t('remoteaccess.expiresAt')}</dt>
          <dd>{date(s.expiresAt)}</dd>
          {s.note ? (
            <>
              <dt>{t('remoteaccess.note')}</dt>
              <dd>{s.note}</dd>
            </>
          ) : null}
        </dl>
      </Card>
      <Card title={t('remoteaccess.provenance')}>
        <h2>{t('remoteaccess.provenance')}</h2>
        <p>{t('remoteaccess.provenanceHint')}</p>
        <dl className="facts">
          {sessionFacts(s).map((fact) => (
            <div key={fact.label}>
              <dt>{t(fact.label)}</dt>
              <dd>
                {date(fact.value) ?? unknown}{' '}
                <small>
                  {fact.origin === 'provider'
                    ? t('remoteaccess.origin.provider', { source: s.observedSource ?? unknown })
                    : t('remoteaccess.origin.turaco')}
                </small>
              </dd>
            </div>
          ))}
          <div>
            <dt>{t('remoteaccess.observedAt')}</dt>
            <dd>{date(s.observedAt) ?? unknown}</dd>
          </div>
        </dl>
      </Card>
      <section>
        <h2>{t('remoteaccess.transitions')}</h2>
        <DataTable
          caption={t('remoteaccess.transitions')}
          columns={columns}
          rows={transitions}
          rowKey={(x) => x.id}
          emptyText={t('remoteaccess.transitions.empty')}
        />
      </section>
    </>
  );
}
