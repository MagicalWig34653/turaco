import { useEffect, useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { TextField } from '../../platform/ui/Field';
import { timeTrackingApi } from './api';
import { formatDuration, parseDuration } from './timeModel';
import { elapsedMinutes, isStaleTimer } from './timerModel';

/**
 * The personal timer on the Ticket panel (F17 T-B). One timer runs per person; starting on another Ticket is
 * refused until the running one is stopped or discarded. A timer older than 12 hours asks for the duration to log.
 */
export function TimerControl({ ticketId, onLogged }: { ticketId: string; onLogged: () => void }) {
  const { t } = useI18n();
  const current = useAsync((signal) => timeTrackingApi.timer(signal), []);
  const [now, setNow] = useState(() => Date.now());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const [duration, setDuration] = useState('');
  const timer = current.data?.timer ?? null;
  useEffect(() => {
    if (!timer) return undefined;
    const id = window.setInterval(() => setNow(Date.now()), 30000);
    return () => window.clearInterval(id);
  }, [timer]);

  const run = async (action: () => Promise<unknown>, logged = false) => {
    setBusy(true);
    setError(undefined);
    try {
      await action();
      setDuration('');
      current.reload();
      if (logged) onLogged();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };

  if (current.error) return <ApiErrorAlert error={current.error} onRetry={current.reload} />;
  if (!current.data) return null;
  const onThisTicket = timer?.subjectId === ticketId;
  const stale = timer ? isStaleTimer(timer.startedAt, now) : false;
  const explicit = parseDuration(duration);
  return (
    <div className="time-timer">
      {timer ? (
        <>
          <p>
            {onThisTicket ? t('timetracking.timer.running') : t('timetracking.timer.otherTicket')}{' '}
            <strong>{formatDuration(elapsedMinutes(timer.startedAt, now))}</strong>
          </p>
          {stale ? (
            <TextField
              label={t('timetracking.timer.staleDuration')}
              hint={t('timetracking.timer.staleHint')}
              value={duration}
              onChange={(event) => setDuration(event.target.value)}
            />
          ) : null}
          <Button
            variant="primary"
            busy={busy}
            disabled={busy || (stale && explicit === undefined)}
            onClick={() => void run(() => timeTrackingApi.stopTimer(explicit), true)}
          >
            {t('timetracking.timer.stop')}
          </Button>
          <Button
            busy={busy}
            disabled={busy}
            onClick={() => void run(() => timeTrackingApi.discardTimer())}
          >
            {t('timetracking.timer.discard')}
          </Button>
        </>
      ) : (
        <Button
          busy={busy}
          disabled={busy}
          onClick={() => void run(() => timeTrackingApi.startTimer(ticketId))}
        >
          {t('timetracking.timer.start')}
        </Button>
      )}
      {error ? <ApiErrorAlert error={error} /> : null}
    </div>
  );
}
