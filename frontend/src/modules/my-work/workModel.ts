import type { FeedEntry } from '../briefing/types';
import type { Task } from '../tasks/types';

/** Missing aggregate entries remain unknown, never a fabricated zero. */
export function summarizeFeed(entries: readonly FeedEntry[]) {
  const approvals = entries.find((entry) => entry.kind === 'pending_approvals')?.count;
  const highlight =
    entries.find((entry) => entry.kind === 'major_incident') ??
    entries.find((entry) => entry.kind === 'maintenance') ??
    entries.find((entry) => entry.kind === 'manual_item');
  const recent = entries
    .filter((entry) => entry.occurredAt && Number.isFinite(Date.parse(entry.occurredAt)))
    .sort((a, b) => Date.parse(b.occurredAt!) - Date.parse(a.occurredAt!))
    .slice(0, 5);
  return { approvals, highlight, recent };
}

type FocusTask = Pick<Task, 'id' | 'priority' | 'status' | 'dueAt'>;

/** My Work side panel: loaded open work by priority and the next due items. */
export function focusSummary<T extends FocusTask>(tasks: readonly T[], now: Date, limit = 3) {
  const open = tasks.filter((task) => task.status !== 'completed' && task.status !== 'cancelled');
  const byPriority = { urgent: 0, high: 0, normal: 0, low: 0 };
  for (const task of open) byPriority[task.priority] += 1;
  const dueSoon = open
    .filter((task) => task.dueAt && Date.parse(task.dueAt) >= now.getTime())
    .sort((a, b) => Date.parse(a.dueAt!) - Date.parse(b.dueAt!))
    .slice(0, limit);
  return { total: open.length, byPriority, dueSoon };
}
