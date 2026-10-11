import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link } from '../../platform/router/Router';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { Card, Skeleton, StatusBadge } from '../../platform/ui/Workspace';
import { ticketsApi } from './api';
import type { TicketHit } from './ticketLookup';
import { TicketPicker } from './TicketPicker';
import type { RelatedTicket } from './types';

function LinkTicketDialog({
  ticketId,
  linked,
  onClose,
  onDone,
}: {
  ticketId: string;
  linked: readonly RelatedTicket[];
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [picked, setPicked] = useState<TicketHit[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const target = picked[0];
    if (!target || busy) return;
    setBusy(true);
    setError(undefined);
    try {
      await ticketsApi.linkTicket(ticketId, target.id);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('ticketRelated.link.title')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <TicketPicker
          label={t('ticketRelated.link.search')}
          selected={picked}
          excludeIds={[
            ticketId,
            ...linked.flatMap((link) => (link.ticketId ? [link.ticketId] : [])),
          ]}
          disabled={busy}
          onChange={setPicked}
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={picked.length === 0}>
            {t('ticketRelated.link.submit')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

function TicketRow({ link, action }: { link: RelatedTicket; action?: React.ReactNode }) {
  const { t } = useI18n();
  return (
    <li>
      {link.hidden || !link.ticketId ? (
        <span>{t('ticketRelated.hidden')}</span>
      ) : (
        <>
          <Link to={`/tickets/${encodeURIComponent(link.ticketId)}`}>
            {link.reference} {link.title}
          </Link>{' '}
          {link.status ? (
            <StatusBadge tone="neutral">
              {t(`tickets.status.${link.status}` as MessageKey)}
            </StatusBadge>
          ) : null}
        </>
      )}{' '}
      {action}
    </li>
  );
}

/** Duplicates of this Ticket and the Tickets linked to it; staff with work access link and unlink. */
export function TicketRelatedCard({
  ticketId,
  canEdit,
  onChanged,
}: {
  ticketId: string;
  canEdit: boolean;
  onChanged?: () => void;
}) {
  const { t } = useI18n();
  const duplicates = useAsync(
    async (signal) => (await ticketsApi.duplicates(ticketId, signal)).items,
    [ticketId],
  );
  const links = useAsync(
    async (signal) => (await ticketsApi.relatedTickets(ticketId, signal)).items,
    [ticketId],
  );
  const [dialog, setDialog] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [removing, setRemoving] = useState<string | null>(null);
  const items = links.data ?? [];
  const dups = duplicates.data ?? [];
  const unlink = async (otherId: string) => {
    setRemoving(otherId);
    setError(undefined);
    try {
      await ticketsApi.unlinkTicket(ticketId, otherId);
      links.reload();
      onChanged?.();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setRemoving(null);
    }
  };
  if (links.error && links.error.status === 403) return null;
  if (links.data && duplicates.data && items.length === 0 && dups.length === 0 && !canEdit)
    return null;
  return (
    <Card title={t('ticketRelated.title')}>
      <h2>{t('ticketRelated.title')}</h2>
      {links.error ? <ApiErrorAlert error={links.error} onRetry={links.reload} /> : null}
      {duplicates.error ? (
        <ApiErrorAlert error={duplicates.error} onRetry={duplicates.reload} />
      ) : null}
      {error ? <ApiErrorAlert error={error} /> : null}
      {links.loading && !links.data ? <Skeleton lines={2} /> : null}
      {links.data && items.length === 0 ? <p className="empty">{t('ticketRelated.none')}</p> : null}
      <ul className="plain-list">
        {items.map((link) => (
          <TicketRow
            key={link.id}
            link={link}
            action={
              canEdit && link.ticketId ? (
                <Button
                  busy={removing === link.ticketId}
                  onClick={() => void unlink(link.ticketId ?? '')}
                >
                  {t('ticketRelated.unlink')}
                </Button>
              ) : undefined
            }
          />
        ))}
      </ul>
      {dups.length > 0 ? (
        <>
          <h3>{t('ticketRelated.duplicates')}</h3>
          <ul className="plain-list">
            {dups.map((link) => (
              <TicketRow key={link.id} link={link} />
            ))}
          </ul>
        </>
      ) : null}
      {canEdit ? <Button onClick={() => setDialog(true)}>{t('ticketRelated.link')}</Button> : null}
      {dialog ? (
        <LinkTicketDialog
          ticketId={ticketId}
          linked={items}
          onClose={() => setDialog(false)}
          onDone={() => {
            setDialog(false);
            links.reload();
            onChanged?.();
          }}
        />
      ) : null}
    </Card>
  );
}
