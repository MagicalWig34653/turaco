import { useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { formatDate } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Table } from '../../platform/ui/Table';
import { Card, Skeleton } from '../../platform/ui/Workspace';
import { timeTrackingApi } from './api';
import { DeleteDialog, EntryDialog } from './EntryDialogs';
import { TimerControl } from './TimerControl';
import { dayTotals, formatDuration } from './timeModel';
import type { TimeEntry } from './types';
import './timetracking.css';

/**
 * Time on a Ticket (F17 T-A): the total and, for people who work the Ticket and may log time, the entries with
 * manual logging. The server decides what is shown; a Ticket the person cannot view yields nothing here.
 */
export function TicketTimePanel({ ticketId }: { ticketId: string }) {
  const { t, locale } = useI18n();
  const { can, session } = useSession();
  const loaded = useAsync((signal) => timeTrackingApi.forTicket(ticketId, signal), [ticketId]);
  const [dialog, setDialog] = useState<
    { kind: 'log' } | { kind: 'edit'; entry: TimeEntry } | { kind: 'delete'; entry: TimeEntry }
  >();
  const canManage = can('timetracking.manage');
  const data = loaded.data;
  if (loaded.error) {
    // 404 means no access to the Ticket's time; show nothing instead of an error card.
    return loaded.error.status === 404 &&
      loaded.error.code !== 'platform.module_disabled' ? null : (
      <Card title={t('timetracking.title')}>
        <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />
      </Card>
    );
  }
  if (!data) {
    return (
      <Card title={t('timetracking.title')}>
        <Skeleton lines={2} />
      </Card>
    );
  }
  const mine = dayTotals(data.items.filter((entry) => entry.userId === session?.userId));
  const done = () => {
    setDialog(undefined);
    loaded.reload();
  };
  return (
    <Card title={t('timetracking.title')} className="time-panel">
      <div className="time-panel-head">
        <h2>{t('timetracking.title')}</h2>
        <p className="time-total">
          <small>{t('timetracking.total')}</small>
          <strong>{formatDuration(data.totalMinutes)}</strong>
        </p>
        {data.canLog ? (
          <Button variant="primary" onClick={() => setDialog({ kind: 'log' })}>
            {t('timetracking.log')}
          </Button>
        ) : null}
      </div>
      {data.canLog ? <TimerControl ticketId={ticketId} onLogged={loaded.reload} /> : null}
      {!data.canLog ? <p className="field-hint">{t('timetracking.totalOnly')}</p> : null}
      {data.canLog && data.items.length === 0 && !data.othersMinutes ? (
        <p>{t('timetracking.empty')}</p>
      ) : null}
      {data.items.length > 0 ? (
        <Table>
          <caption className="visually-hidden">{t('timetracking.title')}</caption>
          <thead>
            <tr>
              <th scope="col">{t('timetracking.workDate')}</th>
              {data.showEntryAuthors ? <th scope="col">{t('timetracking.person')}</th> : null}
              <th scope="col">{t('timetracking.duration')}</th>
              <th scope="col">{t('timetracking.note')}</th>
              <th scope="col">
                <span className="visually-hidden">{t('timetracking.actions')}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {data.items.map((entry) => (
              <tr key={entry.id}>
                <td>{formatDate(locale, entry.workDate)}</td>
                {data.showEntryAuthors ? <td>{entry.userName ?? ''}</td> : null}
                <td>{formatDuration(entry.durationMinutes)}</td>
                <td>{entry.note ?? ''}</td>
                <td className="time-actions">
                  {entry.editable ? (
                    <>
                      <Button onClick={() => setDialog({ kind: 'edit', entry })}>
                        {t('timetracking.edit')}
                      </Button>
                      <Button onClick={() => setDialog({ kind: 'delete', entry })}>
                        {t('timetracking.delete')}
                      </Button>
                    </>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      ) : null}
      {data.othersMinutes ? (
        <p className="field-hint">
          {t('timetracking.others', { duration: formatDuration(data.othersMinutes) })}
        </p>
      ) : null}
      {dialog?.kind === 'log' ? (
        <EntryDialog
          ticketId={ticketId}
          editWindowDays={data.editWindowDays}
          canManage={canManage}
          loggedByDate={mine}
          onClose={() => setDialog(undefined)}
          onDone={done}
        />
      ) : null}
      {dialog?.kind === 'edit' ? (
        <EntryDialog
          ticketId={ticketId}
          entry={dialog.entry}
          editWindowDays={data.editWindowDays}
          canManage={canManage}
          loggedByDate={mine}
          onClose={() => setDialog(undefined)}
          onDone={done}
        />
      ) : null}
      {dialog?.kind === 'delete' ? (
        <DeleteDialog
          entry={dialog.entry}
          editWindowDays={data.editWindowDays}
          canManage={canManage}
          onClose={() => setDialog(undefined)}
          onDone={done}
        />
      ) : null}
    </Card>
  );
}
