import { describe, expect, it } from 'vitest';
import { summarizeFeed } from './workModel';
import type { FeedEntry } from '../briefing/types';
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
