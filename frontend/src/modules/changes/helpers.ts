import type { Change, ResourceType } from './types';
/** The detail response is authoritative, including when it omits an empty list. */
export function allowedActions(change: Change): string[] {
  return change.allowedOperations ?? [];
}
export function resourceLabel(
  node: {
    type: ResourceType;
    id: string;
    name?: string | null;
    reference?: string | null;
    hidden?: boolean;
  },
  restricted: (type: ResourceType) => string,
): string {
  return node.hidden ? restricted(node.type) : node.name || node.reference || node.id;
}

export const lifecycle = [
  'draft',
  'assessment',
  'pending_approval',
  'approved',
  'scheduled',
  'in_progress',
  'completed',
  'review',
  'closed',
] as const;
export type ChangeMetricKey = 'scheduled' | 'awaiting' | 'running' | 'failed';
export function matchesChangeMetric(change: Change, metric: ChangeMetricKey, now: Date): boolean {
  if (metric === 'awaiting') return change.status === 'pending_approval';
  if (metric === 'running') return change.status === 'in_progress';
  if (metric === 'failed') {
    const completed = Date.parse(change.completedAt ?? '');
    // Fail records rollbackDone (including false); review and close retain this outcome.
    const failed =
      change.status === 'failed' ||
      (['review', 'closed'].includes(change.status) && typeof change.rollbackDone === 'boolean');
    return failed && completed <= now.getTime() && completed >= now.getTime() - 30 * 86400000;
  }
  const monday = new Date(now);
  monday.setHours(0, 0, 0, 0);
  monday.setDate(monday.getDate() - ((monday.getDay() + 6) % 7));
  const nextMonday = new Date(monday);
  nextMonday.setDate(nextMonday.getDate() + 7);
  const start = Date.parse(change.windowStart ?? '');
  return change.status === 'scheduled' && start >= monday.getTime() && start < nextMonday.getTime();
}
export function changeMetrics(changes: Change[], now: Date): Record<ChangeMetricKey, number> {
  return Object.fromEntries(
    (['scheduled', 'awaiting', 'running', 'failed'] as const).map((key) => [
      key,
      changes.filter((change) => matchesChangeMetric(change, key, now)).length,
    ]),
  ) as Record<ChangeMetricKey, number>;
}
export function windowMinutes(start: string | null, end: string | null): number | null {
  const duration = (Date.parse(end ?? '') - Date.parse(start ?? '')) / 60000;
  return Number.isFinite(duration) && duration > 0 ? Math.round(duration) : null;
}
