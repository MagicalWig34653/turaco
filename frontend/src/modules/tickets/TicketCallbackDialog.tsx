import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError } from '../../platform/api/useAsync';
import { useSession } from '../../platform/session/SessionProvider';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DateTimeField } from '../../platform/ui/DateTimeField';
import { Dialog } from '../../platform/ui/Dialog';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { ticketsApi } from './api';
import type { TicketDetail } from './types';

/** Schedules a callback: a Task for the Ticket with a due time and an assignee (default: me). */
export function TicketCallbackDialog({
  ticket,
  onClose,
  onDone,
}: {
  ticket: TicketDetail;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const { session } = useSession();
  const [due, setDue] = useState('');
  const [assignee, setAssignee] = useState<Assignee | null>(
    session?.userId ? { id: session.userId, label: session.displayName ?? session.userId } : null,
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const dueDate = due ? new Date(due) : null;
  const valid =
    dueDate !== null && !Number.isNaN(dueDate.getTime()) && dueDate.getTime() > Date.now();
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!valid || !dueDate || busy) return;
    setBusy(true);
    setError(undefined);
    try {
      await ticketsApi.scheduleCallback(ticket.id, {
        dueAt: dueDate.toISOString(),
        ...(assignee ? { assigneeId: assignee.id } : {}),
      });
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('ticketCallback.title')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <p className="field-hint">{t('ticketCallback.hint')}</p>
        <DateTimeField
          label={t('ticketCallback.due')}
          hint={t('ticketCallback.dueHint')}
          value={due}
          onChange={setDue}
          required
        />
        <AssigneePicker
          type="user"
          label={t('ticketCallback.assignee')}
          value={assignee}
          onChange={setAssignee}
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!valid}>
            {t('ticketCallback.submit')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
