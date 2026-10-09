import type { MessageKey } from '../../platform/i18n/i18n';
import type { WorkCount, WorkItem, WorkSource } from './api';
import { workSources } from './api';

/** Pure logic of the merged My Work feed: source filter, de-duplication, counts and overdue. */

export type SourceFilter = 'all' | WorkSource;

export function parseSourceFilter(value: string | null): SourceFilter {
  return (workSources as readonly string[]).includes(value ?? '') ? (value as WorkSource) : 'all';
}

/** The `sources` request parameter for a filter; undefined asks for every source. */
export const sourcesFor = (filter: SourceFilter): readonly WorkSource[] | undefined =>
  filter === 'all' ? undefined : [filter];

/** A page can repeat an item when a failing source is retried; the first occurrence wins. */
export function dedupeItems(items: readonly WorkItem[]): WorkItem[] {
  const seen = new Set<string>();
  return items.filter((item) => {
    const key = `${item.source}:${item.id}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

const terminal = new Set(['completed', 'cancelled', 'resolved', 'closed']);

export function isItemOverdue(item: Pick<WorkItem, 'status' | 'dueAt'>, now: Date): boolean {
  if (!item.dueAt || terminal.has(item.status)) return false;
  const due = Date.parse(item.dueAt);
  return Number.isFinite(due) && due < now.getTime();
}

export type CountView = {
  source: string;
  /** undefined when the count is unavailable; never 0 in that case. */
  count: number | undefined;
  capped: boolean;
  unavailable: boolean;
};

export function countViews(counts: readonly WorkCount[] | undefined): Map<string, CountView> {
  return new Map(
    (counts ?? []).map((entry) => [
      entry.source,
      {
        source: entry.source,
        count: entry.status === 'ok' && typeof entry.count === 'number' ? entry.count : undefined,
        capped: entry.status === 'ok' && entry.capped === true,
        unavailable: entry.status !== 'ok' || typeof entry.count !== 'number',
      },
    ]),
  );
}

export type Total = {
  value: number;
  capped: boolean;
  /** At least one source could not be counted, so the value is a lower bound. */
  partial: boolean;
  /** Every source failed: there is no number to show. */
  unknown: boolean;
};

/** Sum over the available sources; unavailable ones make the total partial, never zero. */
export function totalOf(counts: ReadonlyMap<string, CountView>): Total {
  const views = [...counts.values()];
  const known = views.filter((view) => view.count !== undefined);
  return {
    value: known.reduce((sum, view) => sum + (view.count ?? 0), 0),
    capped: known.some((view) => view.capped),
    partial: views.some((view) => view.unavailable),
    unknown: views.length === 0 || known.length === 0,
  };
}

/** Text of a count: "–" when unavailable, "1000+" when capped. */
export function countText(view: Pick<CountView, 'count' | 'capped'> | undefined): string {
  if (!view || view.count === undefined) return '–';
  return view.capped ? `${view.count}+` : String(view.count);
}

const priorityRank: Record<string, number> = { urgent: 0, high: 1, normal: 2, low: 3 };
export const priorityOf = (
  item: Pick<WorkItem, 'priority'>,
): 'urgent' | 'high' | 'normal' | 'low' =>
  item.priority in priorityRank
    ? (item.priority as 'urgent' | 'high' | 'normal' | 'low')
    : 'normal';

const sourceLabels: Record<WorkSource, MessageKey> = {
  tickets: 'myWork.source.tickets',
  team_tickets: 'myWork.source.team_tickets',
  tasks: 'myWork.source.tasks',
};
/** Display name of a source; an unknown key reads as a generic "some work". */
export const sourceLabelKey = (source: string): MessageKey =>
  (sourceLabels as Record<string, MessageKey | undefined>)[source] ?? 'myWork.source.unknown';

export type Figure = {
  value: number;
  capped: boolean;
  /** At least one of the sources is unavailable, so the value is a lower bound. */
  partial: boolean;
  /** No source answered (or none is offered): there is no number to show. */
  unknown: boolean;
  /** The caller has none of these sources (module off or no access); the figure is not offered. */
  absent: boolean;
};

/** One labelled figure from selected sources, e.g. "assigned to me" = tickets + tasks. */
export function figureOf(
  counts: ReadonlyMap<string, CountView>,
  sources: readonly string[],
): Figure {
  const views = sources.flatMap((source) => {
    const view = counts.get(source);
    return view ? [view] : [];
  });
  const known = views.filter((view) => view.count !== undefined);
  return {
    value: known.reduce((sum, view) => sum + (view.count ?? 0), 0),
    capped: known.some((view) => view.capped),
    partial: views.some((view) => view.unavailable),
    unknown: views.length > 0 && known.length === 0,
    absent: views.length === 0,
  };
}
