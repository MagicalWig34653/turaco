import { describe, expect, it } from 'vitest';
import { focusSummary, summarizeFeed } from './workModel';
import type { FeedEntry } from '../briefing/types';
import type { Task } from '../tasks/types';
const entry = (kind: FeedEntry['kind'], fields: Partial<FeedEntry> = {}): FeedEntry => ({
  kind,
  severity: 'info',
  titleKey: '',
  params: {},
  source: 'briefing',
  linkPath: '/briefing',
  ...fields,
});
describe('dashboard feed summary', () => {
  it('does not invent a zero approval count from an absent source', () => {
    expect(summarizeFeed([]).approvals).toBeUndefined();
    expect(summarizeFeed([entry('pending_approvals', { count: 0 })]).approvals).toBe(0);
  });
  it('prioritizes major incidents over planned maintenance', () => {
    expect(summarizeFeed([entry('maintenance'), entry('major_incident')]).highlight?.kind).toBe(
      'major_incident',
    );
  });
  it('shows only dated events newest first and leaves the source order intact', () => {
    const entries = [
      entry('manual_item', { occurredAt: '2026-10-01' }),
      entry('maintenance', { dueAt: '2026-10-09' }),
      entry('major_incident', { occurredAt: '2026-10-05' }),
      entry('manual_item', { occurredAt: 'invalid' }),
    ];
    expect(summarizeFeed(entries).recent.map((e) => e.kind)).toEqual([
      'major_incident',
      'manual_item',
    ]);
    expect(entries[0]?.kind).toBe('manual_item');
  });
});

describe('My Work focus summary', () => {
  const now = new Date('2026-10-06T12:00:00Z');
  const item = (
    id: string,
    priority: Task['priority'],
    dueAt: string | null,
    status: Task['status'] = 'open',
  ) => ({ id, priority, dueAt, status });
  it('counts open work by priority and lists upcoming due dates first', () => {
    const summary = focusSummary(
      [
        item('a', 'high', '2026-10-09T00:00:00Z'),
        item('b', 'low', '2026-10-07T00:00:00Z'),
        item('late', 'urgent', '2026-10-01T00:00:00Z'),
        item('done', 'high', '2026-10-08T00:00:00Z', 'completed'),
        item('none', 'normal', null),
      ],
      now,
    );
    expect(summary.total).toBe(4);
    expect(summary.byPriority).toEqual({ urgent: 1, high: 1, normal: 1, low: 1 });
    expect(summary.dueSoon.map((task) => task.id)).toEqual(['b', 'a']);
  });
});
