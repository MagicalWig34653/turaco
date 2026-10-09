import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Alert } from '../../platform/ui/Alert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { Select } from '../../platform/ui/Field';
import { Skeleton } from '../../platform/ui/Workspace';
import { ticketQueuesApi, ticketsApi } from './api';
import { moveTargets, queueChoiceLabel } from './queueModel';
import { moveReasonCodes, type MoveReasonCode, type Ticket, type TicketDetail } from './types';

/**
 * "Move to queue": choose the target and a reason, confirm that the ticket gets a new number and
 * keeps the old one as an alias, then see the new number. The server authorizes the move.
 */
export function TicketMoveDialog({
  ticket,
  onClose,
  onDone,
}: {
  ticket: TicketDetail;
  onClose: () => void;
  /** Called after the dialog's result is closed, so the page can reload the ticket. */
  onDone: () => void;
}) {
  const { t } = useI18n();
  const targets = useAsync(async (signal) => (await ticketQueuesApi.list(true, signal)).items, []);
  const options = moveTargets(targets.data, ticket.queueId);
  const [target, setTarget] = useState('');
  const [reason, setReason] = useState<MoveReasonCode>('misrouted');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [moved, setMoved] = useState<Ticket>();
  const targetId = options.some((queue) => queue.id === target) ? target : (options[0]?.id ?? '');
  const targetQueue = options.find((queue) => queue.id === targetId);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!targetId || busy) return;
    setBusy(true);
    setError(undefined);
    try {
      setMoved(
        await ticketsApi.moveToQueue(ticket.id, {
          expectedVersion: ticket.version,
          targetQueueId: targetId,
          reasonCode: reason,
        }),
      );
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };

  if (moved) {
    const cleared = ticket.assigneeId !== null && moved.assigneeId === null;
    return (
      <Dialog title={t('tickets.move.doneTitle')} onClose={onDone}>
        <div className="form" role="status">
          <p>
            {t('tickets.move.doneBody', {
              number: moved.reference,
              queue: moved.queue?.name ?? targetQueue?.name ?? '',
            })}
          </p>
          <p>{t('tickets.move.aliasKept', { old: ticket.reference })}</p>
          {cleared ? <Alert kind="warning">{t('tickets.move.assigneeCleared')}</Alert> : null}
          <div className="dialog-actions">
            <Button variant="primary" autoFocus onClick={onDone}>
              {t('action.close')}
            </Button>
          </div>
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog title={t('tickets.move.title')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        {targets.error ? <ApiErrorAlert error={targets.error} onRetry={targets.reload} /> : null}
        {targets.loading ? <Skeleton lines={2} /> : null}
        {!targets.loading && !targets.error && options.length === 0 ? (
          <p className="field-hint">{t('tickets.move.noTargets')}</p>
        ) : null}
        {options.length > 0 ? (
          <>
            <Select
              label={t('tickets.move.target')}
              value={targetId}
              disabled={busy}
              onChange={(event) => setTarget(event.target.value)}
              options={options.map((queue) => ({
                value: queue.id,
                label: queueChoiceLabel(queue, true),
              }))}
            />
            <Select
              label={t('tickets.move.reason')}
              value={reason}
              disabled={busy}
              onChange={(event) => setReason(event.target.value as MoveReasonCode)}
              options={moveReasonCodes.map((code) => ({
                value: code,
                label: t(`tickets.move.reason.${code}`),
              }))}
            />
            <Alert kind="info">
              <p>
                {t('tickets.move.confirm', {
                  old: ticket.reference,
                  queue: targetQueue?.name ?? '',
                  prefix: targetQueue?.prefix ?? '',
                })}
              </p>
            </Alert>
          </>
        ) : null}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!targetId}>
            {t('tickets.move.submit')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
