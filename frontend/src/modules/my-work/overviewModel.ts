import type { FeedEntry } from '../briefing/types';
import type { WorkItem } from './api';
import { isItemOverdue, priorityOf } from './feedModel';

export type AttentionItem =
  | { kind: 'feed'; tone: 'critical' | 'warning'; entry: FeedEntry }
  | { kind: 'approvals'; tone: 'info'; count: number; entry: FeedEntry }
  | { kind: 'work'; tone: 'critical' | 'warning' | 'info'; item: WorkItem; overdue: boolean };

function isOpen(item: WorkItem): boolean {
  return !['completed', 'cancelled', 'resolved', 'closed'].includes(item.status);
}

/**
 * The Overview's "Needs you" selection. It only reorders existing records:
 * critical briefing signals first, then pending approvals, then urgent or overdue
 * work, then high-priority work and warnings. Nothing is invented or counted twice.
 */
export function buildAttention(
  work: readonly WorkItem[],
  entries: readonly FeedEntry[],
  now: Date,
  limit = 3,
): AttentionItem[] {
  const result: AttentionItem[] = [];
  const signals = entries.filter((entry) => entry.kind !== 'pending_approvals');
  for (const entry of signals)
    if (entry.severity === 'critical') result.push({ kind: 'feed', tone: 'critical', entry });
  const approvals = entries.find((entry) => entry.kind === 'pending_approvals');
  if (approvals?.count)
    result.push({ kind: 'approvals', tone: 'info', count: approvals.count, entry: approvals });
  const open = work.filter(isOpen);
  const pressing = open.filter((item) => priorityOf(item) === 'urgent' || isItemOverdue(item, now));
  for (const item of pressing)
    result.push({
      kind: 'work',
      tone: priorityOf(item) === 'urgent' ? 'critical' : 'warning',
      item,
      overdue: isItemOverdue(item, now),
    });
  for (const item of open)
    if (priorityOf(item) === 'high' && !pressing.includes(item))
      result.push({ kind: 'work', tone: 'warning', item, overdue: false });
  for (const entry of signals)
    if (entry.severity === 'warning') result.push({ kind: 'feed', tone: 'warning', entry });
  for (const item of open)
    if (!pressing.includes(item) && priorityOf(item) !== 'high')
      result.push({ kind: 'work', tone: 'info', item, overdue: false });
  return result.slice(0, limit);
}

/** Overview KPI values derived only from loaded work items and the briefing feed. */
export function overviewMetrics(
  work: readonly WorkItem[],
  entries: readonly FeedEntry[],
  now: Date,
) {
  const open = work.filter(isOpen);
  return {
    open: open.length,
    overdue: open.filter((item) => isItemOverdue(item, now)).length,
    approvals: entries.find((entry) => entry.kind === 'pending_approvals')?.count,
    alerts: entries.filter(
      (entry) =>
        entry.kind !== 'pending_approvals' &&
        (entry.severity === 'critical' || entry.severity === 'warning'),
    ).length,
  };
}
