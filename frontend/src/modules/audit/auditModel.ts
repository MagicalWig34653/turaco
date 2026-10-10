import { isoToLocalInput } from '../../platform/format/format';
import type { ApiError } from '../../platform/api/client';
import type { MessageKey } from '../../platform/i18n/i18n';
import { emptyForm, type FormState } from './auditFilter';
import type { AuditEvent, AuditFilter, AuditLabel } from './types';

export const MAX_EXPORT_DAYS = 92;
const DAY = 86_400_000;
const HOUR = 3_600_000;

/** The default range is the last 7 days; `to` is exclusive, so it is "now". */
export function rangeForm(days: number, now: Date = new Date()): Pick<FormState, 'from' | 'to'> {
  return rangeFormHours(days * 24, now);
}

/** Quick ranges shorter than a day, e.g. "last hour". */
export function rangeFormHours(
  hours: number,
  now: Date = new Date(),
): Pick<FormState, 'from' | 'to'> {
  return {
    from: isoToLocalInput(new Date(now.getTime() - hours * HOUR).toISOString()),
    to: isoToLocalInput(now.toISOString()),
  };
}

export function defaultForm(now: Date = new Date()): FormState {
  return { ...emptyForm, ...rangeForm(7, now) };
}

export type RangeProblem = 'required' | 'tooLong' | 'inverted';

/** Client-side check before an export; the server enforces the same rules. */
export function rangeProblem(filter: Pick<AuditFilter, 'from' | 'to'>): RangeProblem | undefined {
  if (!filter.from || !filter.to) return 'required';
  const from = Date.parse(filter.from);
  const to = Date.parse(filter.to);
  if (Number.isNaN(from) || Number.isNaN(to) || to <= from) return 'inverted';
  if (to - from > MAX_EXPORT_DAYS * DAY) return 'tooLong';
  return undefined;
}

/** Query of the export request: the list filters plus the details switch. */
export function exportQuery(filter: AuditFilter, includeDetails: boolean) {
  return { ...filter, includeDetails: includeDetails ? true : undefined };
}

/** Message key for an export failure; unknown errors fall back to the generic mapping. */
export function exportErrorKey(error: Pick<ApiError, 'status' | 'code'>): MessageKey | undefined {
  switch (error.code) {
    case 'audit.range_required':
      return 'audit.export.error.rangeRequired';
    case 'audit.range_too_long':
      return 'audit.export.error.rangeTooLong';
    case 'audit.export_too_large':
      return 'audit.export.error.tooLarge';
    case 'audit.export_rate_limited':
      return 'audit.export.error.rateLimited';
    default:
      return error.status === 413
        ? 'audit.export.error.tooLarge'
        : error.status === 429
          ? 'audit.export.error.rateLimited'
          : undefined;
  }
}

/** A numeric `details.count` of a 413 answer, when present. */
export function tooLargeCount(error: Pick<ApiError, 'details'>): number | undefined {
  const details = error.details;
  if (typeof details !== 'object' || details === null) return undefined;
  const count = (details as { count?: unknown }).count;
  return typeof count === 'number' && Number.isFinite(count) ? count : undefined;
}

export type Named = { name: string; id: string; gone: boolean; resolved: boolean };

const suffix = (id: string) => (id.length > 6 ? id.slice(-6) : id);

/** Resolved name with fallback to the id; a removed entity keeps its id suffix. */
export function nameOf(label: AuditLabel | undefined, id: string | undefined): Named {
  const raw = id ?? '';
  if (label?.gone) return { name: suffix(raw), id: raw, gone: true, resolved: true };
  if (label?.text) return { name: label.text, id: raw, gone: false, resolved: true };
  return { name: raw, id: raw, gone: false, resolved: false };
}

export type ActorView =
  | { kind: 'user'; named: Named }
  | { kind: 'system'; name: string }
  | { kind: 'metadata'; name: string }
  | { kind: 'unknown' };

/** Who acted: a resolved user, a named system actor, a legacy metadata actor, or unknown. */
export function actorView(event: AuditEvent): ActorView {
  if (event.actorId) return { kind: 'user', named: nameOf(event.actor, event.actorId) };
  if (event.systemActor) return { kind: 'system', name: event.systemActor };
  const legacy = event.metadata.actor;
  if (typeof legacy === 'string' && legacy) return { kind: 'metadata', name: legacy };
  return { kind: 'unknown' };
}

export type DiffRow = {
  path: string;
  before: string | undefined;
  after: string | undefined;
  kind: 'added' | 'removed' | 'changed' | 'same';
};

const MAX_ROWS = 200;
const MAX_DEPTH = 4;

function flatten(value: unknown, path: string, depth: number, out: Map<string, string>) {
  if (out.size >= MAX_ROWS) return;
  const isObject = typeof value === 'object' && value !== null && !Array.isArray(value);
  if (isObject && depth < MAX_DEPTH && Object.keys(value as object).length > 0) {
    for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
      flatten(child, path ? `${path}.${key}` : key, depth + 1, out);
    }
    return;
  }
  out.set(path, typeof value === 'string' ? value : (JSON.stringify(value) ?? String(value)));
}

function toMap(value: unknown): Map<string, string> {
  const out = new Map<string, string>();
  if (value === undefined || value === null) return out;
  flatten(value, '', 0, out);
  return out;
}

/**
 * Key-by-key comparison of the recorded before and after states. A missing side yields only
 * added or removed rows; scalars compare under the empty path. Unchanged keys come last.
 */
export function diffRows(before: unknown, after: unknown): DiffRow[] {
  const left = toMap(before);
  const right = toMap(after);
  const keys = [...new Set([...left.keys(), ...right.keys()])].sort();
  const rows = keys.map((path): DiffRow => {
    const b = left.get(path);
    const a = right.get(path);
    const kind =
      b === undefined ? 'added' : a === undefined ? 'removed' : a === b ? 'same' : 'changed';
    return { path, before: b, after: a, kind };
  });
  return [
    ...rows.filter((row) => row.kind !== 'same'),
    ...rows.filter((row) => row.kind === 'same'),
  ];
}

export function hasChanges(rows: readonly DiffRow[]): boolean {
  return rows.some((row) => row.kind !== 'same');
}

/** The OS user of a CLI event is operator-identifying; only audit exporters may see it. */
export function visibleMetadata(
  metadata: Record<string, unknown>,
  canSeeOsUser: boolean,
): Record<string, unknown> {
  if (canSeeOsUser || !('osUser' in metadata)) return metadata;
  const rest = { ...metadata };
  delete rest.osUser;
  return rest;
}

export function osUserOf(metadata: Record<string, unknown>): string | undefined {
  const value = metadata.osUser;
  return typeof value === 'string' && value ? value : undefined;
}
