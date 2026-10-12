import { emptyState, serialize, type Node } from '../../platform/ui/query/filterModel';

/** The values of the `sla_state` query field of the Ticket list (staff only, present while the sla switch is on). */
export type SlaState = 'none' | 'on_track' | 'at_risk' | 'breached' | 'paused' | 'met';

/** The staff-only SLA summary of a Ticket row. */
export type SlaSummary = {
  state: SlaState;
  responseDueAt: string | null;
  resolutionDueAt: string | null;
};

export type SlaTone = 'neutral' | 'success' | 'warning' | 'danger' | 'info';

export function slaTone(state: SlaState): SlaTone {
  switch (state) {
    case 'breached':
      return 'danger';
    case 'at_risk':
      return 'warning';
    case 'on_track':
      return 'info';
    case 'met':
      return 'success';
    default:
      return 'neutral';
  }
}

export type RelativeDue = { unit: 'minutes' | 'hours' | 'days'; count: number; past: boolean };

/** How far an instant lies from now, in the coarsest whole unit (at least one minute). */
export function relativeDue(iso: string | null | undefined, now: Date): RelativeDue | undefined {
  if (!iso) return undefined;
  const at = new Date(iso).getTime();
  if (Number.isNaN(at)) return undefined;
  const diff = at - now.getTime();
  const minutes = Math.max(1, Math.round(Math.abs(diff) / 60_000));
  const past = diff < 0;
  if (minutes < 60) return { unit: 'minutes', count: minutes, past };
  const hours = Math.round(minutes / 60);
  if (hours < 48) return { unit: 'hours', count: hours, past };
  return { unit: 'days', count: Math.round(hours / 24), past };
}

/** Hours and minutes of a duration in seconds (the report shows business time to complete). */
export function splitDuration(seconds: number): { hours: number; minutes: number } {
  const minutes = Math.max(0, Math.round(seconds / 60));
  return { hours: Math.floor(minutes / 60), minutes: minutes % 60 };
}

export type ReportGroupBy = 'queue' | 'priority' | 'month';
export type ReportKindName = 'first_response' | 'resolution';

export type ReportKind = {
  kind: ReportKindName;
  measured: number;
  met: number;
  breached: number;
  exempted: number;
  metRate: number | null;
  medianSeconds: number | null;
  p90Seconds: number | null;
};

export type ReportGroup = {
  key: string;
  queue?: { id: string; key: string; prefix: string; name: string };
  kinds: ReportKind[];
};

export type SlaReport = {
  from: string;
  to: string;
  groupBy: ReportGroupBy;
  groups: ReportGroup[];
};

const condition = (field: string, op: string, value?: unknown): Node => ({
  type: 'condition',
  field,
  op,
  ...(value === undefined ? {} : { value }),
});

const lastDayOfMonth = (key: string): string => {
  const [year, month] = key.split('-').map(Number) as [number, number];
  return new Date(Date.UTC(year, month, 0)).toISOString().slice(0, 10);
};

/**
 * The Ticket list that shows the Tickets behind a breached or met count: the outcome of the clock kind, the group's
 * Queue, Priority or month, and, for the resolution clock, the period in which the Ticket was resolved. The list
 * uses the viewer's time zone for day boundaries, the report counts UTC days. The first-response clock has no
 * Ticket date of its own, so its link carries the outcome and the group only.
 */
export function reportLink(
  report: Pick<SlaReport, 'from' | 'to' | 'groupBy'>,
  group: ReportGroup,
  kind: ReportKindName,
  outcome: 'breached' | 'met',
): string {
  const children: Node[] = [
    condition(
      kind === 'resolution' ? 'sla_resolution_outcome' : 'sla_response_outcome',
      'equals',
      outcome,
    ),
  ];
  let from = report.from;
  let to = report.to;
  if (report.groupBy === 'queue') children.push(condition('queue', 'equals', group.key));
  if (report.groupBy === 'priority') children.push(condition('priority', 'equals', group.key));
  if (report.groupBy === 'month') {
    const first = `${group.key}-01`;
    const last = lastDayOfMonth(group.key);
    from = first > from ? first : from;
    to = last < to ? last : to;
  }
  if (kind === 'resolution') children.push(condition('resolved_at', 'between', [from, to]));
  const root: Node = { type: 'group', logic: 'and', children };
  return `/service-desk?query=${encodeURIComponent(serialize({ ...emptyState(), filter: { v: 1, root } }))}`;
}

/** Today minus `days - 1` days up to today, as UTC dates, for the report's default period. */
export function defaultPeriod(now: Date, days = 30): { from: string; to: string } {
  const to = now.toISOString().slice(0, 10);
  const from = new Date(now.getTime() - (days - 1) * 86_400_000).toISOString().slice(0, 10);
  return { from, to };
}

/** True when the period is valid for the report endpoint (from not after to, at most 366 days). */
export function periodError(from: string, to: string): 'order' | 'length' | undefined {
  if (!from || !to || from > to) return 'order';
  const days = (Date.parse(`${to}T00:00:00Z`) - Date.parse(`${from}T00:00:00Z`)) / 86_400_000 + 1;
  return days > 366 ? 'length' : undefined;
}

/** The Ticket list filter behind the At risk and Breached counts of a Queue (or of all Queues). */
export function stateLink(states: SlaState[], queueId?: string): string {
  const children: Node[] = [
    condition('status', 'in', ['new', 'open', 'in_progress', 'waiting']),
    condition(
      'sla_state',
      states.length === 1 ? 'equals' : 'in',
      states.length === 1 ? states[0] : states,
    ),
  ];
  if (queueId) children.push(condition('queue', 'equals', queueId));
  const root: Node = { type: 'group', logic: 'and', children };
  return `/service-desk?query=${encodeURIComponent(serialize({ ...emptyState(), filter: { v: 1, root } }))}`;
}
