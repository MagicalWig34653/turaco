import type { FeedEntry } from '../briefing/types';
import { isOverdue } from '../tasks/actions';
import type { Task } from '../tasks/types';

export type AttentionItem =
  | { kind: 'feed'; tone: 'critical' | 'warning'; entry: FeedEntry }
  | { kind: 'approvals'; tone: 'info'; count: number; entry: FeedEntry }
  | { kind: 'task'; tone: 'critical' | 'warning' | 'info'; task: Task; overdue: boolean };

function isOpen(task: Task): boolean {
  return task.status !== 'completed' && task.status !== 'cancelled';
}

/**
 * The Overview's "Needs you" selection. It only reorders existing records:
 * critical briefing signals first, then pending approvals, then urgent or overdue
 * work, then high-priority work and warnings. Nothing is invented or counted twice.
 */
export function buildAttention(
  tasks: readonly Task[],
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
  const open = tasks.filter(isOpen);
  const pressing = open.filter((task) => task.priority === 'urgent' || isOverdue(task, now));
  for (const task of pressing)
    result.push({
      kind: 'task',
      tone: task.priority === 'urgent' ? 'critical' : 'warning',
      task,
      overdue: isOverdue(task, now),
    });
  for (const task of open)
    if (task.priority === 'high' && !pressing.includes(task))
      result.push({ kind: 'task', tone: 'warning', task, overdue: false });
  for (const entry of signals)
    if (entry.severity === 'warning') result.push({ kind: 'feed', tone: 'warning', entry });
  for (const task of open)
    if (!pressing.includes(task) && task.priority !== 'high')
      result.push({ kind: 'task', tone: 'info', task, overdue: false });
  return result.slice(0, limit);
}

/** Overview KPI values derived only from loaded records and the briefing feed. */
export function overviewMetrics(tasks: readonly Task[], entries: readonly FeedEntry[], now: Date) {
  const open = tasks.filter(isOpen);
  return {
    open: open.length,
    overdue: open.filter((task) => isOverdue(task, now)).length,
    approvals: entries.find((entry) => entry.kind === 'pending_approvals')?.count,
    alerts: entries.filter(
      (entry) =>
        entry.kind !== 'pending_approvals' &&
        (entry.severity === 'critical' || entry.severity === 'warning'),
    ).length,
  };
}
