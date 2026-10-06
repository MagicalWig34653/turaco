import type { FeedEntry } from '../briefing/types';

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
