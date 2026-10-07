import { useState } from 'react';
import { usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Select } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { EmptyState, Skeleton, StatusBadge } from '../../platform/ui/Workspace';
import { reportApi } from './api';
import { DeploymentStatusBadge } from './components';
import { codeKey } from './helpers';
import {
  deploymentPath,
  rolloutFilters,
  rolloutPercent,
  rolloutPollMs,
  rolloutStatusQuery,
} from './reportHelpers';
import { progressSegments, rateLabel, ringTone, targetTone } from './runHelpers';
import type { Rollout } from './types';
import { usePolling } from './usePolling';

function RolloutCard({ row }: { row: Rollout }) {
  const { t } = useI18n();
  const d = row.deployment;
  const segments = progressSegments(row.counts);
  const percent = rolloutPercent(row);
  return (
    <li className="deployments-rollout">
      <div className="deployments-card-heading">
        <h2>
          <Link to={deploymentPath(d.id)}>{d.name}</Link>
        </h2>
        <DeploymentStatusBadge status={d.status} />
      </div>
      <p className="deployments-muted">
        {d.reference} · {d.productName} <code>{d.productVersion}</code>
      </p>
      <p>
        {row.currentRingName ? (
          <>
            {t('deployments.rollouts.currentRing', {
              position: row.currentPosition ?? 0,
              total: row.ringCount,
              name: row.currentRingName,
            })}{' '}
            {row.currentRingStatus ? (
              <StatusBadge tone={ringTone(row.currentRingStatus)}>
                {t(
                  codeKey(
                    'deployments.run.ring',
                    row.currentRingStatus,
                    'deployments.unknownValue',
                  ),
                )}
              </StatusBadge>
            ) : null}{' '}
          </>
        ) : null}
        {row.awaitingPromotion ? (
          <StatusBadge tone="warning">{t('deployments.rollouts.awaitingPromotion')}</StatusBadge>
        ) : null}{' '}
        {row.haltedRings > 0 ? (
          <StatusBadge tone="danger">
            {t('deployments.rollouts.halted', { count: row.haltedRings })}
          </StatusBadge>
        ) : null}
      </p>
      <div
        className="deployments-progress"
        role="img"
        aria-label={t('deployments.run.progress', { percent, total: row.targetTotal })}
      >
        {segments.map((segment) => (
          <span
            key={segment.state}
            className={`deployments-progress-seg deployments-progress-${targetTone(segment.state)}`}
            style={{ width: `${segment.percent}%` }}
          />
        ))}
      </div>
      <p className="deployments-muted">
        {t('deployments.rollouts.decided', { decided: row.decided, total: row.targetTotal })}
        {rateLabel(row.successRatePercent)
          ? ` · ${t('deployments.report.kpi.successRate')}: ${rateLabel(row.successRatePercent)}`
          : ''}
      </p>
    </li>
  );
}

/** Active rollouts across Deployments with progress per current ring; refreshes every 30 s while visible. */
export function RolloutsScreen() {
  const { t } = useI18n();
  const [filter, setFilter] = useState<string>('active');
  const list = usePagedList(
    (cursor, signal) => reportApi.rollouts(rolloutStatusQuery(filter), cursor, signal),
    [filter],
  );
  usePolling(list.reload, filter === 'active', rolloutPollMs);
  return (
    <div className="deployments-workspace">
      <PageHeader
        title={t('deployments.rollouts.title')}
        actions={
          <Link className="btn btn-secondary" to="/deployments">
            {t('deployments.plan.back')}
          </Link>
        }
      />
      <Select
        label={t('deployments.rollouts.filter')}
        value={filter}
        onChange={(event) => setFilter(event.target.value)}
        options={rolloutFilters.map((value) => ({
          value,
          label: t(`deployments.rollouts.filter.${value}`),
        }))}
      />
      {list.error ? <ApiErrorAlert error={list.error} onRetry={list.reload} /> : null}
      {list.loading && list.items.length === 0 ? <Skeleton lines={5} /> : null}
      {!list.loading && !list.error && list.items.length === 0 ? (
        <EmptyState title={t('deployments.rollouts.empty')} />
      ) : null}
      <ul className="deployments-rollout-list">
        {list.items.map((row) => (
          <RolloutCard key={row.deployment.id} row={row} />
        ))}
      </ul>
      {list.hasMore ? (
        <Button onClick={list.loadMore} busy={list.loadingMore}>
          {t('deployments.rollouts.more')}
        </Button>
      ) : null}
    </div>
  );
}
