import { describe, expect, it } from 'vitest';
import { groupFeedEntries, resolveFeedTitle, sourceKey } from './feed';
import type { FeedEntry } from './types';

const entry = (overrides: Partial<FeedEntry> = {}): FeedEntry => ({
  kind: 'manual_item',
  severity: 'info',
  titleKey: 'briefing.feed.manual_item',
  params: { title: 'Hello' },
  linkPath: '/briefing/1',
  source: 'briefing',
  ...overrides,
});

describe('briefing feed helpers', () => {
  it('resolves known titles and falls back for unknown server keys', () => {
    expect(resolveFeedTitle(entry())).toEqual({
      key: 'briefing.feed.manual_item',
      params: { title: 'Hello' },
    });
    expect(
      resolveFeedTitle(
        entry({ titleKey: 'future.key', params: { title: 'Safe', nested: { value: 1 } } }),
      ),
    ).toEqual({
      key: 'briefing.feed.unknown',
      params: { title: 'Safe' },
    });
    expect(sourceKey('risk_review_due')).toBe('briefing.source.security');
    expect(sourceKey('future')).toBe('briefing.source.unknown');
  });

  it('groups critical before warning before info and sorts each group by due or occurrence time', () => {
    const entries = [
      entry({ severity: 'warning', dueAt: '2026-10-08T00:00:00Z' }),
      entry({ severity: 'critical', occurredAt: '2026-10-07T00:00:00Z' }),
      entry({ severity: 'warning', occurredAt: '2026-10-06T00:00:00Z' }),
      entry({ severity: 'warning' }),
      entry(),
    ];
    const groups = groupFeedEntries(entries);
    expect(groups.map((group) => group.severity)).toEqual(['critical', 'warning', 'info']);
    expect(groups[1]?.entries).toEqual([entries[2], entries[0], entries[3]]);
  });
});
