import { TableDate } from '../../platform/ui/TableDate';
import { FilterBar } from '../../platform/ui/FilterBar';
import { useState } from 'react';
import { usePagedList } from '../../platform/api/useAsync';
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
  const [actionError, setActionError] = useState<string | null>(null);
  const [status, setStatus] = useState<TicketStatus | ''>('');
  const [openOnly, setOpenOnly] = useState(true);
  const list = usePagedList(
    (cursor, signal) => ticketsApi.list(scope, { status, open: openOnly }, cursor, signal),
    [scope, status, openOnly],
  );
  const title = t(scope === 'mine' ? 'nav.myTickets' : 'nav.ticketQueue');
  const columns: Column<Ticket>[] = [
    {
      key: 'reference',
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
            sortValue: (x: Ticket) => ({ low: 0, normal: 1, high: 2, urgent: 3 })[x.priority],
            header: t('tickets.col.priority'),
            render: (x: Ticket) => t(`tickets.priority.${x.priority}`),
          },
        ]
      : []),
    {
      key: 'updated',
      sortValue: (x) => x.updatedAt,
      header: t('tickets.col.updated'),
      render: (x) => <TableDate value={x.updatedAt} />,
    },
  ];
  const rowActions = (ticket: Ticket): MenuItem[] => {
    const path = `/support/${encodeURIComponent(ticket.id)}`;
    const copy = async (value: string) => {
      if (!(await copyContextText(value))) window.prompt(t('contextMenu.copyFallback'), value);
    };
    return [
      { id: 'open', label: t('contextMenu.open'), onSelect: () => navigate(path) },
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
                void ticketsApi
                  .assign(ticket.id, ticket.version, { assigneeId: session.userId })
                  .then(
                    () => {
                      setActionError(null);
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
      {actionError ? <Toast kind="error">{actionError}</Toast> : null}
      <FilterBar
        activeFilters={activeFilters}
        role="search"
        onSubmit={(event) => event.preventDefault()}
      >
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
      <DataTable
        filterSummary={activeFilters.map((filter) => filter.label).join(' · ')}
        caption={title}
        columns={columns}
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
