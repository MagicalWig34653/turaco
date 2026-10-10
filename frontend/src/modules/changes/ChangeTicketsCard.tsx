import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { StatusBadge } from '../../platform/ui/Workspace';
import { changesApi } from './api';

/** Tickets linked to a Change (only those the viewer may see); hidden entirely without access. */
export function ChangeTicketsCard({ changeId }: { changeId: string }) {
  const { t } = useI18n();
  const tickets = useAsync(
    async (signal) => (await changesApi.tickets(changeId, signal)).items,
    [changeId],
  );
  if (tickets.error && (tickets.error.status === 403 || tickets.error.status === 404)) return null;
  return (
    <section className="change-card">
      <h2>{t('changes.tickets.title')}</h2>
      {tickets.error ? <ApiErrorAlert error={tickets.error} onRetry={tickets.reload} /> : null}
      {tickets.data && tickets.data.length === 0 ? (
        <p className="change-empty">{t('changes.tickets.none')}</p>
      ) : null}
      <ul className="plain-list">
        {(tickets.data ?? []).map((ticket) => (
          <li key={ticket.ticketId}>
            <Link to={`/support/${encodeURIComponent(ticket.ticketId)}`}>
              {ticket.reference} {ticket.title}
            </Link>{' '}
            <StatusBadge tone="neutral">
              {t(`tickets.status.${ticket.status as 'new'}`)}
            </StatusBadge>
          </li>
        ))}
      </ul>
    </section>
  );
}
