import { useMemo, useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Select, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Table } from '../../platform/ui/Table';
import { Card, Skeleton } from '../../platform/ui/Workspace';
import { defaultPeriod, periodError } from '../sla/slaModel';
import { ticketQueuesApi } from '../tickets/api';
import { timeTrackingApi } from './api';
import { formatDuration } from './timeModel';

const groupings = ['queue', 'month', 'ticket'] as const;

/**
 * Effort report (F17 T-C): time per Queue, month or Ticket within the Queues the caller may view. There is no
 * grouping by person; groups with too few contributors are suppressed by the server and shown as such.
 */
export function EffortReportScreen() {
  const { t } = useI18n();
  const initial = useMemo(() => defaultPeriod(new Date()), []);
  const [from, setFrom] = useState(initial.from);
  const [to, setTo] = useState(initial.to);
  const [groupBy, setGroupBy] = useState<(typeof groupings)[number]>('queue');
  const queues = useAsync(async (signal) => (await ticketQueuesApi.list(false, signal)).items, []);
  const problem = periodError(from, to);
  const report = useAsync(
    (signal) =>
      problem ? Promise.resolve(undefined) : timeTrackingApi.report({ from, to, groupBy }, signal),
    [from, to, groupBy, problem],
  );
  const queueName = (id: string) => queues.data?.find((queue) => queue.id === id)?.name ?? id;
  return (
    <>
      <PageHeader
        eyebrow={t('timetracking.eyebrow')}
        title={t('timetracking.report.title')}
        intro={t('timetracking.report.intro')}
      />
      <Card title={t('timetracking.report.filters')}>
        <div className="manager-filters">
          <TextField
            label={t('slaReport.from')}
            type="date"
            value={from}
            max={to}
            onChange={(event) => setFrom(event.target.value)}
          />
          <TextField
            label={t('slaReport.to')}
            type="date"
            value={to}
            min={from}
            onChange={(event) => setTo(event.target.value)}
            error={problem ? t(`slaReport.error.${problem}`) : undefined}
          />
          <Select
            label={t('slaReport.groupBy')}
            value={groupBy}
            onChange={(event) => setGroupBy(event.target.value as (typeof groupings)[number])}
            options={groupings.map((value) => ({
              value,
              label: t(`timetracking.report.groupBy.${value}`),
            }))}
          />
        </div>
        <p className="subtitle">{t('timetracking.report.note')}</p>
      </Card>
      {problem ? null : report.error ? (
        <ApiErrorAlert error={report.error} onRetry={report.reload} />
      ) : !report.data ? (
        <Skeleton lines={4} />
      ) : report.data.groups.length === 0 ? (
        <Card title={t('timetracking.report.title')}>
          <p className="subtitle">{t('timetracking.report.empty')}</p>
        </Card>
      ) : (
        <Card title={t('timetracking.report.title')}>
          <Table>
            <caption className="visually-hidden">{t('timetracking.report.title')}</caption>
            <thead>
              <tr>
                <th scope="col">{t(`timetracking.report.groupBy.${report.data.groupBy}`)}</th>
                <th scope="col">{t('timetracking.duration')}</th>
                <th scope="col">{t('timetracking.report.entries')}</th>
              </tr>
            </thead>
            <tbody>
              {report.data.groups.map((group) => (
                <tr key={group.key}>
                  <td>
                    {report.data!.groupBy === 'queue'
                      ? queueName(group.key)
                      : (group.label ?? group.key)}
                  </td>
                  {group.suppressed ? (
                    <td colSpan={2}>
                      {t('timetracking.report.suppressed', { count: report.data!.minGroupSize })}
                    </td>
                  ) : (
                    <>
                      <td>{formatDuration(group.minutes ?? 0)}</td>
                      <td>{group.entries ?? 0}</td>
                    </>
                  )}
                </tr>
              ))}
            </tbody>
          </Table>
          {report.data.truncated ? (
            <p className="field-hint">{t('timetracking.report.truncated')}</p>
          ) : null}
        </Card>
      )}
    </>
  );
}
