import { useMemo, useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { formatDate } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Table } from '../../platform/ui/Table';
import { Card, EmptyState, Skeleton } from '../../platform/ui/Workspace';
import { timeTrackingApi } from './api';
import { DeleteDialog, EntryDialog } from './EntryDialogs';
import { addDays, dayTotals, formatDuration, localDate, weekOf } from './timeModel';
import type { TimeEntry } from './types';
import './timetracking.css';

/**
 * My time (F17 T-A): the person's own entries of one week. Entries are logged on the Ticket; here they can be
 * corrected or deleted. Nobody else's time is shown and there is no per-person comparison anywhere.
 */
export function MyTimeScreen() {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const [anchor, setAnchor] = useState(() => localDate(new Date()));
  const week = useMemo(() => weekOf(anchor), [anchor]);
  const loaded = useAsync(
    (signal) => timeTrackingApi.mine(week.from, week.to, signal),
    [week.from, week.to],
  );
  const [dialog, setDialog] = useState<
    { kind: 'edit'; entry: TimeEntry } | { kind: 'delete'; entry: TimeEntry }
  >();
  const data = loaded.data;
  const totals = dayTotals(data?.items ?? []);
  const canManage = can('timetracking.manage');
  const done = () => {
    setDialog(undefined);
    loaded.reload();
  };
  return (
    <>
      <PageHeader
        eyebrow={t('timetracking.eyebrow')}
        title={t('timetracking.my')}
        intro={t('timetracking.myIntro')}
      />
      <div className="time-week-nav">
        <Button onClick={() => setAnchor(addDays(week.from, -7))}>
          {t('timetracking.prevWeek')}
        </Button>
        <strong>
          {formatDate(locale, week.from)} – {formatDate(locale, week.to)}
        </strong>
        <Button onClick={() => setAnchor(addDays(week.from, 7))}>
          {t('timetracking.nextWeek')}
        </Button>
        <Button onClick={() => setAnchor(localDate(new Date()))}>
          {t('timetracking.thisWeek')}
        </Button>
      </div>
      {loaded.error ? (
        <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />
      ) : !data ? (
        <Skeleton lines={4} />
      ) : data.items.length === 0 ? (
        <EmptyState title={t('timetracking.myEmpty')} description={t('timetracking.myEmptyHint')} />
      ) : (
        <Card title={t('timetracking.my')}>
          <p className="time-total">
            <small>{t('timetracking.weekTotal')}</small>
            <strong>{formatDuration(data.totalMinutes)}</strong>
          </p>
          <Table>
            <caption className="visually-hidden">{t('timetracking.my')}</caption>
            <thead>
              <tr>
                <th scope="col">{t('timetracking.workDate')}</th>
                <th scope="col">{t('timetracking.ticket')}</th>
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
                  <td>
                    {entry.reference ? (
                      <Link to={`/support/${entry.subjectId}`}>{entry.reference}</Link>
                    ) : (
                      t('timetracking.ticketHidden')
                    )}
                  </td>
                  <td>{formatDuration(entry.durationMinutes)}</td>
                  <td>{entry.note ?? ''}</td>
                  <td className="time-actions">
                    {entry.editable && entry.reference ? (
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
          <ul className="time-days">
            {week.days.map((day) => (
              <li key={day}>
                <span>{formatDate(locale, day)}</span>{' '}
                <strong>{formatDuration(totals[day] ?? 0)}</strong>
              </li>
            ))}
          </ul>
        </Card>
      )}
      {dialog?.kind === 'edit' ? (
        <EntryDialog
          ticketId={dialog.entry.subjectId}
          entry={dialog.entry}
          editWindowDays={data?.editWindowDays ?? 14}
          canManage={canManage}
          loggedByDate={totals}
          onClose={() => setDialog(undefined)}
          onDone={done}
        />
      ) : null}
      {dialog?.kind === 'delete' ? (
        <DeleteDialog
          entry={dialog.entry}
          editWindowDays={data?.editWindowDays ?? 14}
          canManage={canManage}
          onClose={() => setDialog(undefined)}
          onDone={done}
        />
      ) : null}
    </>
  );
}
