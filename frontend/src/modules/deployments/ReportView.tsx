import { useId } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { EmptyState, Skeleton, StatusBadge } from '../../platform/ui/Workspace';
import { Actor } from '../software/components';
import { reportApi } from './api';
import { Section } from './components';
import { codeKey } from './helpers';
import {
  advisoryPath,
  clusterLabel,
  clusterShare,
  durationLabel,
  reasonBars,
  taskPath,
} from './reportHelpers';
import { rateLabel, ringTone, shouldPoll } from './runHelpers';
import { usePolling } from './usePolling';
import type {
  DeploymentDetail,
  DeploymentReport,
  DeploymentSecurityContext,
  RingReport,
} from './types';

const reportPollMs = 30_000;

function Kpi({ label, value, hint }: { label: string; value: string | number; hint?: string }) {
  return (
    <div className="deployments-kpi">
      <dt>{label}</dt>
      <dd>
        <strong>{value}</strong>
        {hint ? <small>{hint}</small> : null}
      </dd>
    </div>
  );
}

function KpiStrip({ report }: { report: DeploymentReport }) {
  const { t } = useI18n();
  const totals = report.totals;
  const sum = Object.values(totals).reduce((acc, n) => acc + n, 0);
  return (
    <dl className="deployments-kpis" aria-label={t('deployments.report.kpis')}>
      <Kpi label={t('deployments.report.kpi.targets')} value={sum} />
      <Kpi label={t('deployments.report.kpi.successful')} value={totals.successful ?? 0} />
      <Kpi label={t('deployments.report.kpi.failed')} value={totals.failed ?? 0} />
      <Kpi label={t('deployments.report.kpi.expired')} value={totals.expired ?? 0} />
      <Kpi
        label={t('deployments.report.kpi.alreadySatisfied')}
        value={totals.already_satisfied ?? 0}
      />
      <Kpi
        label={t('deployments.report.kpi.successRate')}
        value={rateLabel(report.successRatePercent) ?? '—'}
      />
      <Kpi
        label={t('deployments.report.kpi.median')}
        value={durationLabel(report.medianSecondsToSuccess) ?? '—'}
      />
    </dl>
  );
}

function GateRow({ gate }: { gate: RingReport['gates'][number] }) {
  const { t, locale } = useI18n();
  const label = gate.passed
    ? t(codeKey('deployments.report.gate', gate.gate, 'deployments.unknownValue'))
    : t(
        codeKey(
          'deployments.run.reason.code',
          gate.reason ?? gate.gate,
          'deployments.run.reason.code.other',
        ),
        { code: gate.reason ?? gate.gate },
      );
  return (
    <li
      className={`deployments-gate ${gate.passed ? 'deployments-gate-pass' : 'deployments-gate-fail'}`}
    >
      <span aria-hidden="true">{gate.passed ? '✓' : '✕'}</span>
      <strong>
        {gate.passed ? t('deployments.report.gate.passed') : t('deployments.report.gate.failed')}
      </strong>{' '}
      {label}{' '}
      <time className="deployments-muted" dateTime={gate.at}>
        {formatDateTime(locale, gate.at)}
      </time>
    </li>
  );
}

function RingTable({ rings }: { rings: RingReport[] }) {
  const { t } = useI18n();
  if (rings.length === 0)
    return <p className="deployments-muted">{t('deployments.report.noRings')}</p>;
  return (
    <div className="deployments-table-scroll">
      <table className="deployments-report-table">
        <thead>
          <tr>
            <th scope="col">{t('deployments.report.ring')}</th>
            <th scope="col">{t('deployments.report.ringStatus')}</th>
            <th scope="col">{t('deployments.report.kpi.successful')}</th>
            <th scope="col">{t('deployments.report.kpi.failed')}</th>
            <th scope="col">{t('deployments.report.kpi.successRate')}</th>
            <th scope="col">{t('deployments.report.kpi.median')}</th>
            <th scope="col">{t('deployments.report.gates')}</th>
          </tr>
        </thead>
        <tbody>
          {rings.map((ring) => (
            <tr key={ring.ringRunId}>
              <th scope="row">
                {ring.position}. {ring.name}
              </th>
              <td>
                <StatusBadge tone={ringTone(ring.status)}>
                  {t(codeKey('deployments.run.ring', ring.status, 'deployments.unknownValue'))}
                </StatusBadge>
              </td>
              <td className="deployments-count">{ring.counts.successful ?? 0}</td>
              <td className="deployments-count">
                {(ring.counts.failed ?? 0) + (ring.counts.expired ?? 0)}
              </td>
              <td>
                {rateLabel(ring.successRatePercent) ?? '—'}{' '}
                <small className="deployments-muted">
                  {t('deployments.report.threshold', { percent: ring.successThresholdPercent })}
                </small>
              </td>
              <td>{durationLabel(ring.medianSecondsToSuccess) ?? '—'}</td>
              <td>
                {ring.gates.length === 0 ? (
                  <span className="deployments-muted">{t('deployments.report.noGates')}</span>
                ) : (
                  <ul className="deployments-gates">
                    {ring.gates.map((gate, index) => (
                      <GateRow key={`${gate.gate}-${gate.at}-${index}`} gate={gate} />
                    ))}
                  </ul>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function FailureReasons({ reasons }: { reasons: DeploymentReport['topFailureReasons'] }) {
  const { t } = useI18n();
  const bars = reasonBars(reasons);
  if (bars.length === 0)
    return <p className="deployments-muted">{t('deployments.report.noFailures')}</p>;
  return (
    <div className="deployments-bars">
      <dl>
        {bars.map((bar) => (
          <div key={bar.code}>
            <dt>
              <code>{bar.code}</code>
            </dt>
            <dd>
              <span className="deployments-bar" aria-hidden="true">
                <span style={{ width: `${bar.percent}%` }} />
              </span>
              <span className="deployments-count">{bar.count}</span>
            </dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

function Clusters({ clusters }: { clusters: DeploymentReport['clusters'] }) {
  const { t } = useI18n();
  if (clusters.length === 0)
    return <p className="deployments-muted">{t('deployments.report.noClusters')}</p>;
  return (
    <ul className="deployments-cluster-cards">
      {clusters.map((cluster) => (
        <li key={cluster.findingId} className="deployments-cluster-card">
          <strong>
            {clusterLabel(
              t(
                codeKey(
                  'deployments.report.dimension',
                  cluster.dimension,
                  'deployments.unknownValue',
                ),
              ),
              cluster.value,
            )}
          </strong>
          <span>
            {t('deployments.report.clusterCounts', {
              failed: cluster.failed,
              total: cluster.total,
              share: clusterShare(cluster),
            })}
          </span>
          <StatusBadge tone="neutral">{t('deployments.report.derived')}</StatusBadge>
        </li>
      ))}
    </ul>
  );
}

function People({ report }: { report: DeploymentReport }) {
  const { t, locale } = useI18n();
  const { people } = report;
  const rows: { label: string; id: string | null | undefined }[] = [
    { label: t('deployments.report.people.owner'), id: people.ownerUserId },
    { label: t('deployments.report.people.submitted'), id: people.submittedBy },
    { label: t('deployments.report.people.scheduled'), id: people.scheduledBy },
    { label: t('deployments.report.people.started'), id: people.startedBy },
  ];
  const ringName = (ringId: string) => report.rings.find((r) => r.ringId === ringId)?.name ?? '—';
  return (
    <dl className="deployments-facts">
      {rows
        .filter((row) => row.id)
        .map((row) => (
          <div key={row.label} className="deployments-fact-row">
            <dt>{row.label}</dt>
            <dd>
              <Actor userId={row.id ?? null} />
            </dd>
          </div>
        ))}
      {people.approvedAt ? (
        <div className="deployments-fact-row">
          <dt>{t('deployments.report.people.approved')}</dt>
          <dd>
            <time dateTime={people.approvedAt}>{formatDateTime(locale, people.approvedAt)}</time>
          </dd>
        </div>
      ) : null}
      {(people.promotions ?? []).map((promotion) => (
        <div key={`${promotion.ringId}-${promotion.at}`} className="deployments-fact-row">
          <dt>{t('deployments.report.people.promoted', { ring: ringName(promotion.ringId) })}</dt>
          <dd>
            <Actor userId={promotion.by ?? null} />
            {' · '}
            <time dateTime={promotion.at}>{formatDateTime(locale, promotion.at)}</time>
          </dd>
        </div>
      ))}
    </dl>
  );
}

function FollowUps({ followUps }: { followUps: DeploymentReport['followUps'] }) {
  const { t, locale } = useI18n();
  if (followUps.length === 0)
    return <p className="deployments-muted">{t('deployments.report.noFollowUps')}</p>;
  return (
    <ul className="deployments-followups">
      {followUps.map((item) => (
        <li key={item.taskId}>
          <Link to={taskPath(item.taskId)}>
            {t(
              codeKey(
                'deployments.report.followUp',
                item.reason,
                'deployments.report.followUp.other',
              ),
            )}
          </Link>{' '}
          <time className="deployments-muted" dateTime={item.createdAt}>
            {formatDateTime(locale, item.createdAt)}
          </time>
        </li>
      ))}
    </ul>
  );
}

function SecurityContextCard({ context }: { context: DeploymentSecurityContext }) {
  const { t } = useI18n();
  return (
    <>
      <p>
        {t('deployments.report.security.summary', {
          product: `${context.productName} ${context.productVersion}`,
          advisories: context.advisoryCount,
          findings: context.openFindingCount,
        })}
      </p>
      {context.truncated ? (
        <p className="deployments-muted">{t('deployments.report.security.truncated')}</p>
      ) : null}
      {!context.detailed ? (
        <Alert kind="info">{t('deployments.report.security.countsOnly')}</Alert>
      ) : (context.advisories ?? []).length === 0 ? (
        <p className="deployments-muted">{t('deployments.report.security.none')}</p>
      ) : (
        <ul className="deployments-advisories">
          {(context.advisories ?? []).map((advisory) => (
            <li key={advisory.id}>
              <Link to={advisoryPath(advisory.id)}>
                <code>{advisory.reference}</code> {advisory.title}
              </Link>{' '}
              <StatusBadge tone="neutral">
                {t(codeKey('security.severity', advisory.severity, 'security.severity.unknown'))}
              </StatusBadge>{' '}
              {advisory.knownExploited ? (
                <StatusBadge tone="danger">{t('deployments.report.security.kev')}</StatusBadge>
              ) : null}{' '}
              <span className="deployments-muted">
                {t('deployments.report.security.counts', {
                  findings: advisory.openFindings,
                  devices: advisory.affectedDevices,
                })}
              </span>
            </li>
          ))}
        </ul>
      )}
    </>
  );
}

/** The rollout report of a Deployment (F9 G4): outcome, rings and gates, failure analysis, people and security context. */
export function ReportView({ plan }: { plan: DeploymentDetail }) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const headingId = useId();
  const report = useAsync((signal) => reportApi.report(plan.id, signal), [plan.id]);
  const security = useAsync((signal) => reportApi.securityContext(plan.id, signal), [plan.id]);
  usePolling(
    () => {
      report.reload();
      security.reload();
    },
    shouldPoll(plan.status),
    reportPollMs,
  );
  const data = report.data;
  if (report.error) return <ApiErrorAlert error={report.error} onRetry={report.reload} />;
  if (!data) return <Skeleton lines={8} />;
  return (
    <div className="deployments-report" aria-labelledby={headingId}>
      <div className="deployments-card-heading">
        <h2 id={headingId} className="visually-hidden">
          {t('deployments.report.title')}
        </h2>
        <span className="deployments-muted">
          {t('deployments.report.generated', { at: formatDateTime(locale, data.generatedAt) })}
        </span>
        <a className="btn btn-secondary" href={reportApi.csvUrl(plan.id)} download>
          {t('deployments.report.csv')}
        </a>
      </div>
      {!can('endpoints.view') ? (
        <p className="deployments-muted">{t('deployments.report.csvNames')}</p>
      ) : null}
      <KpiStrip report={data} />
      <Section title={t('deployments.report.rings')}>
        <RingTable rings={data.rings} />
      </Section>
      <div className="deployments-report-grid">
        <Section title={t('deployments.report.reasons')}>
          <FailureReasons reasons={data.topFailureReasons} />
        </Section>
        <Section title={t('deployments.report.clusters')}>
          <p className="deployments-muted">{t('deployments.report.clustersHint')}</p>
          <Clusters clusters={data.clusters} />
        </Section>
        <Section title={t('deployments.report.people')}>
          <People report={data} />
        </Section>
        <Section title={t('deployments.report.followUps')}>
          <FollowUps followUps={data.followUps} />
        </Section>
      </div>
      <Section title={t('deployments.report.security')}>
        {security.error ? (
          <ApiErrorAlert error={security.error} onRetry={security.reload} />
        ) : security.data ? (
          <SecurityContextCard context={security.data} />
        ) : (
          <Skeleton lines={2} />
        )}
      </Section>
      {data.rings.length === 0 && Object.keys(data.totals).length === 0 ? (
        <EmptyState title={t('deployments.report.empty')} />
      ) : null}
    </div>
  );
}
