import { useAi } from '../ai/AiProvider';
import { TableDate } from '../../platform/ui/TableDate';
import { FilterBar } from '../../platform/ui/FilterBar';
import { useEffect, useState } from 'react';
import { useQueryList } from '../../platform/ui/query/useQueryList';
import { QueryWorkbench } from '../../platform/ui/query/QueryWorkbench';
import { useFilterQuery } from '../../platform/ui/useFilterQuery';
import { organizationApi } from '../organization/api';
import { Button } from '../../platform/ui/Button';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../platform/router/Router';
import { Badge } from '../../platform/ui/Alert';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { copyContextText, type MenuItem } from '../../platform/ui/ContextMenu';
import { Checkbox, Select } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { useSession } from '../../platform/session/SessionProvider';
import { Toast } from '../../platform/ui/Workspace';
import { IncidentBanner } from '../incidents/IncidentBanner';
import { ticketsApi } from './api';
import { ticketStatuses, type Ticket, type TicketStatus } from './types';

const tone: Record<TicketStatus, 'neutral' | 'success' | 'warning' | 'danger' | 'info'> = {
  new: 'info',
  open: 'info',
  in_progress: 'info',
  waiting: 'warning',
  resolved: 'success',
  closed: 'neutral',
  cancelled: 'neutral',
};

export function TicketStatusBadge({ status }: { status: TicketStatus }) {
  const { t } = useI18n();
  return (
    <Badge tone={tone[status]} live={status === 'in_progress'}>
      {t(`tickets.status.${status}`)}
    </Badge>
  );
}

/** "My tickets" for everyone, the full queue for people with tickets.view. */
export function TicketsScreen({ scope }: { scope: 'mine' | 'all' }) {
  const { t } = useI18n();
  const { can, session } = useSession();
  const ai = useAi();
  const [actionError, setActionError] = useState<string | null>(null);
  const [actionSuccess, setActionSuccess] = useState(false);
  const [visibleKeys, setVisibleKeys] = useState<string[]>();
  const [names, setNames] = useState<Record<string, string>>({});
  const [assignment, setAssignment] = useState<'mine' | 'unassigned' | 'all'>(() => {
    const value = new URLSearchParams(window.location.search).get('assignment');
    return value === 'mine' || value === 'unassigned' ? value : 'all';
  });
  const [status, setStatus] = useState<TicketStatus | ''>(() => {
    const value = new URLSearchParams(window.location.search).get('status');
    return ticketStatuses.includes(value as TicketStatus) ? (value as TicketStatus) : '';
  });
  const [openOnly, setOpenOnly] = useState(
    () => new URLSearchParams(window.location.search).get('open') !== 'false',
  );
  useFilterQuery({ status, open: openOnly ? 'true' : 'false', assignment });
  const query = useQueryList<Ticket>(
    'tickets',
    {
      scope,
      status,
      open: openOnly,
      assigneeId: scope === 'all' && assignment === 'mine' ? session?.userId : undefined,
    },
    scope === 'all' && assignment === 'unassigned'
      ? { type: 'condition', field: 'assignee', op: 'is_empty' }
      : undefined,
  );
  const { list } = query;
  const personIds = [
    ...new Set(
      list.items
        .flatMap((ticket) => [ticket.assigneeId, ticket.reporterId])
        .filter((id): id is string => Boolean(id)),
    ),
  ]
    .sort()
    .join(',');
  useEffect(() => {
    if (scope !== 'all' || !personIds) return;
    const controller = new AbortController();
    void Promise.all(
      personIds.split(',').map(async (id) => {
        try {
          return [id, (await organizationApi.user(id, controller.signal)).displayName] as const;
        } catch {
          return [id, ''] as const;
        }
      }),
    ).then((entries) => {
      if (!controller.signal.aborted) setNames(Object.fromEntries(entries));
    });
    return () => controller.abort();
  }, [personIds, scope]);
  const title = t(scope === 'mine' ? 'nav.myTickets' : 'nav.ticketQueue');
  const columns: Column<Ticket>[] = [
    {
      key: 'reference',
      sortField: 'reference',
      sortValue: (x) => x.reference,
      header: t('tickets.col.reference'),
      render: (x) => <Link to={`/support/${encodeURIComponent(x.id)}`}>{x.reference}</Link>,
    },
    {
      key: 'title',
      sortValue: (x) => x.title,
      header: t('tickets.col.title'),
      render: (x) => x.title,
    },
    {
      key: 'status',
      sortValue: (x) => x.status,
      header: t('tickets.col.status'),
      render: (x) => <TicketStatusBadge status={x.status} />,
    },
    ...(scope === 'all'
      ? [
          {
            key: 'priority',
            sortField: 'priority',
            sortValue: (x: Ticket) => ({ low: 0, normal: 1, high: 2, urgent: 3 })[x.priority],
            header: t('tickets.col.priority'),
            render: (x: Ticket) => t(`tickets.priority.${x.priority}`),
          },
        ]
      : []),
    {
      key: 'updated',
      sortField: 'updated_at',
      sortValue: (x) => x.updatedAt,
      header: t('tickets.col.updated'),
      render: (x) => <TableDate value={x.updatedAt} />,
    },
  ];
  if (scope === 'all')
    columns.splice(
      columns.length - 1,
      0,
      {
        key: 'assignee',
        header: t('tickets.col.assignee'),
        render: (ticket) =>
          ticket.assigneeId
            ? names[ticket.assigneeId] || t('tickets.personUnknown')
            : t('tickets.fact.unassigned'),
      },
      {
        key: 'requester',
        header: t('tickets.col.requester'),
        render: (ticket) => names[ticket.reporterId] || t('tickets.personUnknown'),
      },
    );
  const visibleColumns = visibleKeys
    ? visibleKeys.flatMap((key) => columns.filter((column) => column.key === key))
    : columns;
  const rowActions = (ticket: Ticket): MenuItem[] => {
    const path = `/support/${encodeURIComponent(ticket.id)}`;
    const copy = async (value: string) => {
      if (!(await copyContextText(value))) window.prompt(t('contextMenu.copyFallback'), value);
    };
    return [
      { id: 'open', label: t('contextMenu.open'), onSelect: () => navigate(path) },
      ...(ai.can('ai.use')
        ? [
            {
              id: 'ask-ai',
              label: t('ai.ask'),
              onSelect: () => ai.open({ type: 'ticket', id: ticket.id }),
            },
          ]
        : []),
      ...(scope === 'all' &&
      can('tickets.manage') &&
      session &&
      ticket.assigneeId !== session.userId &&
      !['closed', 'cancelled'].includes(ticket.status)
        ? [
            {
              id: 'assign-me',
              label: t('tickets.action.assignMe'),
              onSelect: () => {
                setActionSuccess(false);
                void ticketsApi
                  .assign(ticket.id, ticket.version, { assigneeId: session.userId })
                  .then(
                    () => {
                      setActionError(null);
                      setActionSuccess(true);
                      list.reload();
                    },
                    () => setActionError(t('error.generic')),
                  );
              },
            },
          ]
        : []),
      {
        id: 'copy-reference',
        label: t('contextMenu.copyReference'),
        onSelect: () => void copy(ticket.reference),
      },
      {
        id: 'copy-link',
        label: t('contextMenu.copyLink'),
        onSelect: () => void copy(new URL(path, window.location.origin).href),
      },
    ];
  };
  const activeFilters = [
    ...(scope === 'all' && assignment !== 'all'
      ? [
          {
            key: 'assignment',
            label: t(`tickets.filter.${assignment}`),
            onRemove: () => setAssignment('all'),
          },
        ]
      : []),
    ...(status
      ? [
          {
            key: 'status',
            label: t(`tickets.status.${status}`),
            onRemove: () => {
              setStatus('');
            },
          },
        ]
      : []),
    ...(openOnly
      ? [
          {
            key: 'open',
            label: t('tickets.filter.openOnly'),
            onRemove: () => {
              setOpenOnly(false);
            },
          },
        ]
      : []),
  ];

  return (
    <>
      <PageHeader
        title={title}
        intro={t(scope === 'mine' ? 'tickets.mine.intro' : 'tickets.queue.intro')}
        actions={
          <Link to="/support/new" className="btn btn-primary">
            {t('tickets.create.action')}
          </Link>
        }
      />
      {scope === 'mine' ? <IncidentBanner /> : null}
      {actionSuccess ? <Toast kind="success">{t('tickets.assignedToMe')}</Toast> : null}
      {actionError ? <Toast kind="error">{actionError}</Toast> : null}
      <FilterBar
        activeFilters={activeFilters}
        role="search"
        onSubmit={(event) => event.preventDefault()}
      >
        {scope === 'all' ? (
          <div role="group" aria-label={t('tickets.filter.assignment')}>
            {(['mine', 'unassigned', 'all'] as const).map((value) => (
              <Button
                key={value}
                aria-pressed={assignment === value}
                onClick={() => setAssignment(value)}
              >
                {t(`tickets.filter.${value}`)}
              </Button>
            ))}
          </div>
        ) : null}
        <Select
          label={t('tickets.col.status')}
          value={status}
          onChange={(event) => setStatus(event.target.value as TicketStatus | '')}
          options={[
            { value: '', label: t('tickets.filter.anyStatus') },
            ...ticketStatuses.map((value) => ({ value, label: t(`tickets.status.${value}`) })),
          ]}
        />
        <Checkbox
          label={t('tickets.filter.openOnly')}
          checked={openOnly}
          onChange={(event) => setOpenOnly(event.target.checked)}
        />
      </FilterBar>
      <QueryWorkbench
        query={query}
        columns={columns}
        listKey={`tickets-${scope}`}
        onColumnsChange={setVisibleKeys}
      />
      <DataTable
        filterSummary={activeFilters.map((filter) => filter.label).join(' · ')}
        caption={title}
        columns={visibleColumns}
        serverSort={query.state.sort}
        onSortChange={(sort) => query.setState({ ...query.state, sort })}
        totalCount={query.countCapped ? undefined : query.count}
        rows={list.items}
        rowKey={(x) => x.id}
        rowActions={rowActions}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('tickets.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </>
  );
}
