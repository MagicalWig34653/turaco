import { useState, type FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { Select, TextArea, TextField } from '../../platform/ui/Field';
import { timeTrackingApi } from './api';
import {
  addDays,
  formatDuration,
  formProblem,
  localDate,
  outsideWindow,
  parseDuration,
  workDateRange,
} from './timeModel';
import { deleteReasons, type DeleteReason, type TimeEntry } from './types';

/**
 * Log a new entry on a Ticket (no `entry`) or correct an existing one. A reason is asked for only when the server
 * would demand one: somebody else's entry, or a work date outside the edit window (timetracking.manage).
 */
export function EntryDialog({
  ticketId,
  entry,
  editWindowDays,
  canManage,
  loggedByDate,
  onClose,
  onDone,
}: {
  ticketId: string;
  entry?: TimeEntry | undefined;
  editWindowDays: number;
  canManage: boolean;
  /** Minutes the signed-in person already has per work date (shown entries only; the server decides). */
  loggedByDate?: Record<string, number> | undefined;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const today = localDate(new Date());
  const range = workDateRange(today, editWindowDays);
  const [duration, setDuration] = useState(entry ? formatDuration(entry.durationMinutes) : '');
  const [workDate, setWorkDate] = useState(entry?.workDate ?? today);
  const [note, setNote] = useState(entry?.note ?? '');
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const foreign = !!entry && !entry.own;
  const outside = outsideWindow(workDate, today, editWindowDays);
  const needsReason = foreign || (outside && canManage);
  const logged = Math.max(
    0,
    (loggedByDate?.[workDate] ?? 0) -
      (entry && entry.workDate === workDate ? entry.durationMinutes : 0),
  );
  const problem = formProblem({
    duration,
    workDate,
    today,
    editWindowDays,
    canManage,
    reason,
    loggedThatDay: foreign ? 0 : logged,
    needsReason,
  });
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (problem) return;
    setBusy(true);
    setError(undefined);
    try {
      const minutes = parseDuration(duration) ?? 0; // validated by formProblem; the server decides again
      const body = {
        subjectId: ticketId,
        workDate,
        durationMinutes: minutes,
        note: note.trim(),
        ...(needsReason ? { reason: reason.trim() } : {}),
      };
      if (entry) await timeTrackingApi.update(entry.id, body, entry.version);
      else await timeTrackingApi.create(body);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={entry ? t('timetracking.edit') : t('timetracking.log')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <TextField
          label={t('timetracking.duration')}
          hint={t('timetracking.durationHint')}
          value={duration}
          onChange={(event) => setDuration(event.target.value)}
          error={
            problem === 'duration' && duration
              ? t('timetracking.problem.duration')
              : problem === 'dayLimit'
                ? t('timetracking.problem.dayLimit')
                : undefined
          }
          autoFocus
          required
        />
        <TextField
          label={t('timetracking.workDate')}
          type="date"
          value={workDate}
          min={canManage ? undefined : range.min}
          max={addDays(today, 1)}
          onChange={(event) => setWorkDate(event.target.value)}
          error={
            problem === 'date'
              ? t('timetracking.problem.date', { days: editWindowDays })
              : undefined
          }
          required
        />
        <TextField
          label={t('timetracking.note')}
          hint={t('timetracking.noteHint')}
          value={note}
          maxLength={500}
          onChange={(event) => setNote(event.target.value)}
        />
        {needsReason ? (
          <TextArea
            label={t('timetracking.reason')}
            hint={t('timetracking.reasonHint')}
            value={reason}
            maxLength={300}
            rows={2}
            onChange={(event) => setReason(event.target.value)}
            required
          />
        ) : null}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!!problem}>
            {t('action.save')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** Delete an entry with a closed reason (and a free-text reason when the deletion needs timetracking.manage). */
export function DeleteDialog({
  entry,
  editWindowDays,
  canManage,
  onClose,
  onDone,
}: {
  entry: TimeEntry;
  editWindowDays: number;
  canManage: boolean;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const today = localDate(new Date());
  const needsReason =
    !entry.own || (outsideWindow(entry.workDate, today, editWindowDays) && canManage);
  const [deleteReason, setDeleteReason] = useState<DeleteReason>('wrong_duration');
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await timeTrackingApi.remove(
        entry.id,
        deleteReason,
        entry.version,
        needsReason ? reason.trim() : undefined,
      );
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('timetracking.delete')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <p>
          {formatDuration(entry.durationMinutes)} · {entry.workDate}
        </p>
        <Select
          label={t('timetracking.deleteReason')}
          value={deleteReason}
          onChange={(event) => setDeleteReason(event.target.value as DeleteReason)}
          options={deleteReasons.map((value) => ({
            value,
            label: t(`timetracking.deleteReason.${value}`),
          }))}
        />
        {needsReason ? (
          <TextArea
            label={t('timetracking.reason')}
            hint={t('timetracking.reasonHint')}
            value={reason}
            maxLength={300}
            rows={2}
            onChange={(event) => setReason(event.target.value)}
            required
          />
        ) : null}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button
            type="submit"
            variant="danger"
            busy={busy}
            disabled={needsReason && reason.trim().length < 3}
          >
            {t('timetracking.delete')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
