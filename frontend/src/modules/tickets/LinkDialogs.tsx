import { useState } from 'react';
import type { FormEvent } from 'react';
import { ApiError } from '../../platform/api/client';
import type { ApiError as ApiErrorType } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { Select, TextArea } from '../../platform/ui/Field';
import { incidentsApi } from '../incidents/api';
import { problemsApi } from '../problems/api';
import { ticketsApi } from './api';
import { TicketPicker } from './TicketPicker';
import type { TicketHit } from './ticketLookup';
import type { TicketDetail } from './types';

/** Links this Ticket to an open Problem or an active Major Incident chosen from a list. */
export function LinkToDialog({
  kind,
  ticketId,
  onClose,
  onDone,
}: {
  kind: 'problem' | 'incident';
  ticketId: string;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const targets = useAsync(
    async (signal) => {
      if (kind === 'problem') {
        const page = await problemsApi.list('', undefined, signal);
        return page.items
          .filter((problem) => problem.status !== 'closed')
          .map((problem) => ({ id: problem.id, label: `${problem.reference} · ${problem.title}` }));
      }
      const page = await incidentsApi.list(true, undefined, signal);
      return page.items.map((incident) => ({
        id: incident.id,
        label: `${incident.reference} · ${incident.title}`,
      }));
    },
    [kind],
  );
  const [target, setTarget] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiErrorType>();
  const chosen = target || targets.data?.[0]?.id || '';
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!chosen) return;
    setBusy(true);
    setError(undefined);
    try {
      if (kind === 'problem') await problemsApi.linkTicket(chosen, ticketId);
      else await incidentsApi.linkTicket(chosen, ticketId);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog
      title={t(kind === 'problem' ? 'ticketLink.toProblem' : 'ticketLink.toIncident')}
      onClose={onClose}
    >
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        {targets.error ? <ApiErrorAlert error={targets.error} onRetry={targets.reload} /> : null}
        {targets.data && targets.data.length === 0 ? (
          <p className="empty">
            {t(kind === 'problem' ? 'ticketLink.noProblems' : 'ticketLink.noIncidents')}
          </p>
        ) : null}
        {targets.data && targets.data.length > 0 ? (
          <Select
            label={t(kind === 'problem' ? 'ticketLink.problem' : 'ticketLink.incident')}
            value={chosen}
            onChange={(event) => setTarget(event.target.value)}
            options={targets.data.map((item) => ({ value: item.id, label: item.label }))}
          />
        ) : null}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!chosen}>
            {t('ticketLink.link')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** Marks the Ticket as a duplicate of another Ticket found by number or title. */
export function DuplicateDialog({
  ticket,
  onClose,
  onDone,
}: {
  ticket: TicketDetail;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [picked, setPicked] = useState<TicketHit[]>([]);
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiErrorType>();
  const unsupported = error instanceof ApiError && (error.status === 404 || error.status === 405);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const target = picked[0];
    if (!target) return;
    setBusy(true);
    setError(undefined);
    try {
      await ticketsApi.markDuplicate(ticket.id, ticket.version, target.id, note.trim());
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('ticketLink.duplicate')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {unsupported ? (
          <Alert kind="warning">{t('ticketLink.duplicate.unavailable')}</Alert>
        ) : error ? (
          <ApiErrorAlert error={error} />
        ) : null}
        <TicketPicker
          label={t('ticketLink.duplicate.of')}
          hint={t('ticketLink.duplicate.hint')}
          selected={picked}
          excludeIds={[ticket.id]}
          disabled={busy}
          onChange={setPicked}
        />
        <TextArea
          label={t('ticketLink.duplicate.note')}
          value={note}
          rows={3}
          maxLength={1000}
          onChange={(event) => setNote(event.target.value)}
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={picked.length === 0}>
            {t('ticketLink.duplicate.confirm')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
