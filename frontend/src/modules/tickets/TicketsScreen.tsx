import { useState } from 'react';
import { usePagedList } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../platform/router/Router';
import { Badge } from '../../platform/ui/Alert';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { copyContextText, type MenuItem } from '../../platform/ui/ContextMenu';
import { Checkbox, Select } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { useSession } from '../../platform/session/SessionProvider';
import { useTheme } from '../../platform/theme/ThemeProvider';
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
  return <Badge tone={tone[status]}>{t(`tickets.status.${status}`)}</Badge>;
}

/** "My tickets" for everyone, the full queue for people with tickets.view. */
export function TicketsScreen({ scope }: { scope: 'mine' | 'all' }) {
  const { t, locale } = useI18n();
  const { can, session } = useSession();
  const { density, setDensity } = useTheme();
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
      header: t('tickets.col.reference'),
      render: (x) => <Link to={`/support/${encodeURIComponent(x.id)}`}>{x.reference}</Link>,
    },
    { key: 'title', header: t('tickets.col.title'), render: (x) => x.title },
    {
      key: 'status',
      header: t('tickets.col.status'),
      render: (x) => <TicketStatusBadge status={x.status} />,
    },
    ...(scope === 'all'
      ? [
          {
            key: 'priority',
            header: t('tickets.col.priority'),
            render: (x: Ticket) => t(`tickets.priority.${x.priority}`),
          },
        ]
      : []),
    {
      key: 'updated',
      header: t('tickets.col.updated'),
      render: (x) => <time dateTime={x.updatedAt}>{formatDateTime(locale, x.updatedAt)}</time>,
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
      <form
        className="filters tickets-filterbar"
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
        <Select
          label={t('shell.density')}
          value={density}
          onChange={(event) => setDensity(event.target.value as typeof density)}
          options={[
            { value: 'comfortable', label: t('shell.densityComfortable') },
            { value: 'compact', label: t('shell.densityCompact') },
          ]}
        />
        <Checkbox
          label={t('tickets.filter.openOnly')}
          checked={openOnly}
          onChange={(event) => setOpenOnly(event.target.checked)}
        />
      </form>
      <DataTable
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
