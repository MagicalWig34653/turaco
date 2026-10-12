import { useMemo, useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Select, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Card, Skeleton } from '../../platform/ui/Workspace';
import { ticketQueuesApi } from '../tickets/api';
import { slaReportApi } from './reportApi';
import {
  defaultPeriod,
  periodError,
  reportLink,
  splitDuration,
  type ReportGroup,
  type ReportGroupBy,
  type SlaReport,
} from './slaModel';
import '../../app/manager-overview.css';

const groupings: ReportGroupBy[] = ['queue', 'priority', 'month'];

/**
 * Minimal SLA report (F17 S-E): finished clocks per Queue, Priority or month for the Queues the caller may view.
 * It never shows people: there is no grouping by assignee or Team and no export.
 */
export function SlaReportScreen() {
  const { t } = useI18n();
  const initial = useMemo(() => defaultPeriod(new Date()), []);
  const [from, setFrom] = useState(initial.from);
  const [to, setTo] = useState(initial.to);
  const [groupBy, setGroupBy] = useState<ReportGroupBy>('queue');
  const [queueId, setQueueId] = useState('');
  const queues = useAsync(async (signal) => (await ticketQueuesApi.list(false, signal)).items, []);
  const problem = periodError(from, to);
  const report = useAsync(
    (signal) =>
      problem
        ? Promise.resolve(undefined)
        : slaReportApi.get({ from, to, groupBy, ...(queueId ? { queueId } : {}) }, signal),
    [from, to, groupBy, queueId, problem],
  );
  return (
    <>
      <PageHeader
        eyebrow={t('slaReport.eyebrow')}
        title={t('slaReport.title')}
        intro={t('slaReport.intro')}
      />
      <Card title={t('slaReport.filters')}>
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
            onChange={(event) => setGroupBy(event.target.value as ReportGroupBy)}
            options={groupings.map((value) => ({ value, label: t(`slaReport.groupBy.${value}`) }))}
          />
          <Select
            label={t('slaReport.queue')}
            value={queueId}
            onChange={(event) => setQueueId(event.target.value)}
            options={[
              { value: '', label: t('slaReport.allQueues') },
              ...(queues.data ?? []).map((queue) => ({ value: queue.id, label: queue.name })),
            ]}
          />
        </div>
        <p className="subtitle">{t('slaReport.note')}</p>
      </Card>
      {problem ? null : report.error ? (
        <ApiErrorAlert error={report.error} onRetry={report.reload} />
      ) : !report.data ? (
        <Skeleton lines={4} />
      ) : (
        <ReportTable report={report.data} />
      )}
    </>
  );
}

function groupLabel(report: SlaReport, group: ReportGroup, priority: (key: string) => string) {
  if (report.groupBy === 'queue')
    return group.queue ? `${group.queue.name} (${group.queue.prefix})` : '';
  if (report.groupBy === 'priority') return priority(group.key);
  return group.key;
}

function ReportTable({ report }: { report: SlaReport }) {
  const { t } = useI18n();
  const duration = (seconds: number | null) => {
    if (seconds === null) return '–';
    const { hours, minutes } = splitDuration(seconds);
    return hours > 0
      ? t('slaReport.durationHM', { hours, minutes })
      : t('slaReport.durationM', { minutes });
  };
  if (report.groups.length === 0) {
    return (
      <Card title={t('slaReport.title')}>
        <p className="subtitle">{t('slaReport.empty')}</p>
      </Card>
    );
  }
  return (
    <Card title={t('slaReport.title')}>
      <table className="manager-table">
        <caption className="visually-hidden">{t('slaReport.title')}</caption>
        <thead>
          <tr>
            <th scope="col">{t(`slaReport.groupBy.${report.groupBy}`)}</th>
            <th scope="col">{t('slaReport.kind')}</th>
            <th scope="col">{t('slaReport.measured')}</th>
            <th scope="col">{t('slaReport.met')}</th>
            <th scope="col">{t('slaReport.breached')}</th>
            <th scope="col">{t('slaReport.exempted')}</th>
            <th scope="col">{t('slaReport.metRate')}</th>
            <th scope="col">{t('slaReport.median')}</th>
            <th scope="col">{t('slaReport.p90')}</th>
          </tr>
        </thead>
        <tbody>
          {report.groups.flatMap((group) =>
            group.kinds.map((kind, index) => (
              <tr key={`${group.key}-${kind.kind}`}>
                {index === 0 ? (
                  <th scope="row" rowSpan={group.kinds.length}>
                    {groupLabel(report, group, (key) =>
                      t(`sla.priority.${key}` as 'sla.priority.low'),
                    )}
                  </th>
                ) : null}
                <td>{t(`sla.kind.${kind.kind}`)}</td>
                <td>{kind.measured}</td>
                <td>
                  {kind.met > 0 ? (
                    <Link to={reportLink(report, group, kind.kind, 'met')}>{kind.met}</Link>
                  ) : (
                    kind.met
                  )}
                </td>
                <td>
                  {kind.breached > 0 ? (
                    <Link to={reportLink(report, group, kind.kind, 'breached')}>
                      {kind.breached}
                    </Link>
                  ) : (
                    kind.breached
                  )}
                </td>
                <td>{kind.exempted}</td>
                <td>{kind.metRate === null ? '–' : `${Math.round(kind.metRate * 100)} %`}</td>
                <td>{duration(kind.medianSeconds)}</td>
                <td>{duration(kind.p90Seconds)}</td>
              </tr>
            )),
          )}
        </tbody>
      </table>
    </Card>
  );
}
