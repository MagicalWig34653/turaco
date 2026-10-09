import type { FieldIssue } from '../../platform/api/client';
import type { AffectedCandidate, Change, ResourceType } from './types';
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
  // Planned work this week: approved, scheduled or running Changes whose window overlaps it.
  if (!['approved', 'scheduled', 'in_progress'].includes(change.status)) return false;
  const start = Date.parse(change.windowStart ?? '');
  if (!Number.isFinite(start)) return false;
  const parsedEnd = Date.parse(change.windowEnd ?? '');
  const end = Number.isFinite(parsedEnd) && parsedEnd > start ? parsedEnd : start;
  return start < nextMonday.getTime() && end >= monday.getTime();
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

/** What a Change still lacks before it can be submitted, in the order the server reports it. */
export const readinessFields = ['windowStart', 'rollbackPlan', 'affectedResources'] as const;
export type ReadinessField = (typeof readinessFields)[number];
/** Where a field-level issue is shown in the create form. */
export type FormStep = 'basics' | 'planning' | 'resources';

/** Maximum number of affected resources per Change (mirrors the server limit). */
export const maxAffected = 25;

/** The fields a Change needs before it can be submitted; mirrors the server rules. */
export function requiredForSubmit(change: Pick<Change, 'kind' | 'risk'>): ReadinessField[] {
  const required: ReadinessField[] = ['windowStart'];
  if (change.risk !== 'low') required.push('rollbackPlan');
  if (change.kind !== 'standard') required.push('affectedResources');
  return required;
}

/** Local readiness check; the server stays authoritative. */
export function missingForSubmit(
  change: Pick<Change, 'kind' | 'risk' | 'windowStart' | 'rollbackPlan'>,
  affectedCount: number,
): ReadinessField[] {
  return requiredForSubmit(change).filter((field) =>
    field === 'windowStart'
      ? !change.windowStart
      : field === 'rollbackPlan'
        ? !(change.rollbackPlan ?? '').trim()
        : affectedCount === 0,
  );
}

/** The required fields a 400 response names; unknown fields and codes are ignored. */
export function readinessFromIssues(issues: readonly FieldIssue[]): ReadinessField[] {
  const named = new Set(
    issues.filter((issue) => issue.code === 'required').map((issue) => issue.field),
  );
  return readinessFields.filter((field) => named.has(field));
}

/** Union in canonical order. */
export function mergeMissing(...lists: ReadinessField[][]): ReadinessField[] {
  const all = new Set(lists.flat());
  return readinessFields.filter((field) => all.has(field));
}

export function stepOfField(field: ReadinessField): FormStep {
  return field === 'affectedResources' ? 'resources' : 'planning';
}

/** Issues per create-form step, so each step shows only its own. */
export function issuesByStep(issues: readonly FieldIssue[]): Record<FormStep, ReadinessField[]> {
  const out: Record<FormStep, ReadinessField[]> = { basics: [], planning: [], resources: [] };
  for (const field of readinessFromIssues(issues)) out[stepOfField(field)].push(field);
  return out;
}

export function candidateKey(candidate: { type: string; id: string }): string {
  return `${candidate.type}:${candidate.id}`;
}

/** Add the candidate, or remove it when already selected. Respects the Change's resource limit. */
export function toggleCandidate(
  selected: readonly AffectedCandidate[],
  candidate: AffectedCandidate,
  alreadyLinked = 0,
): AffectedCandidate[] {
  const key = candidateKey(candidate);
  if (selected.some((item) => candidateKey(item) === key))
    return selected.filter((item) => candidateKey(item) !== key);
  if (selected.length + alreadyLinked >= maxAffected) return [...selected];
  return [...selected, candidate];
}

export function candidateLabel(candidate: AffectedCandidate): string {
  return candidate.reference ? `${candidate.reference} · ${candidate.name}` : candidate.name;
}
