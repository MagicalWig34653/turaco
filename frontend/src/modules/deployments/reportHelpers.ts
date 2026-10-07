import { formatCountdown } from './runHelpers';
import type { FailureCluster, Rollout } from './types';

/** The report exists once a Deployment was scheduled (a cancelled plan only if it had been scheduled). */
export function hasReport(status: string, scheduledAt: string | null | undefined): boolean {
  if (status === 'cancelled') return Boolean(scheduledAt);
  return [
    'scheduled',
    'resolving_targets',
    'ready',
    'running',
    'paused',
    'completed',
    'completed_with_errors',
    'failed',
  ].includes(status);
}

/** Median time to success, or null when nothing succeeded yet. */
export function durationLabel(seconds: number | null | undefined): string | null {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds) || seconds < 0) {
    return null;
  }
  if (seconds < 60) return `${Math.round(seconds)} s`;
  return formatCountdown(seconds);
}

/** Whole percent of `part` in `total`; 0 for an empty total. */
export function sharePercent(part: number, total: number): number {
  if (total <= 0 || part <= 0) return 0;
  return Math.min(100, Math.round((part / total) * 100));
}

export const clusterShare = (cluster: Pick<FailureCluster, 'failed' | 'total'>) =>
  sharePercent(cluster.failed, cluster.total);

/** "Model: Latitude 7440"; the dimension label is already translated. */
export const clusterLabel = (dimensionLabel: string, value: string) =>
  `${dimensionLabel}: ${value}`;

export type ReasonBar = { code: string; count: number; percent: number };

/** Failure reasons, most frequent first, with bar widths relative to the largest one. */
export function reasonBars(reasons: readonly { code: string; count: number }[]): ReasonBar[] {
  const sorted = [...reasons]
    .filter((reason) => reason.count > 0)
    .sort((a, b) => b.count - a.count || a.code.localeCompare(b.code));
  const max = sorted[0]?.count ?? 0;
  return sorted.map((reason) => ({ ...reason, percent: sharePercent(reason.count, max) }));
}

/** Gates that passed carry a fixed code; a failed gate carries the halt reason code. */
export const passedGates = ['success_threshold', 'soak', 'promotion'] as const;

/** Progress of a rollout row as whole percent of decided targets. */
export const rolloutPercent = (row: Pick<Rollout, 'decided' | 'targetTotal'>) =>
  sharePercent(row.decided, row.targetTotal);

export const rolloutFilters = ['active', 'completed', 'ended'] as const;
export type RolloutFilter = (typeof rolloutFilters)[number];

/** The `status` query value of a filter; empty means the API default (active rollouts). */
export function rolloutStatusQuery(filter: string): string {
  switch (filter) {
    case 'completed':
      return 'completed';
    case 'ended':
      return 'completed_with_errors,failed,cancelled';
    default:
      return '';
  }
}

/** Rollouts refresh every 30 seconds while visible. */
export const rolloutPollMs = 30_000;

export const advisoryPath = (id: string) => `/security/advisories/${encodeURIComponent(id)}`;
export const taskPath = (id: string) => `/tasks/${encodeURIComponent(id)}`;
export const deploymentPath = (id: string) => `/deployments/${encodeURIComponent(id)}`;
