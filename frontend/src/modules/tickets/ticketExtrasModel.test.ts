import { describe, expect, it } from 'vitest';
import {
  addMention,
  changeCandidates,
  historyLocationIds,
  locationFromProfile,
  maxMentions,
  mentionIds,
  removeMention,
} from './ticketExtrasModel';
import type { TicketHistoryEntry } from './types';

const entry = (over: Partial<TicketHistoryEntry>): TicketHistoryEntry => ({
  id: 'h',
  at: '2026-10-01T10:00:00Z',
  kind: 'location_changed',
  ...over,
});

describe('mentions', () => {
  it('adds a person once and caps at the server limit', () => {
    let list = addMention([], { id: 'a', label: 'A' });
    list = addMention(list, { id: 'a', label: 'A' });
    expect(list).toHaveLength(1);
    for (let i = 0; i < 20; i++) list = addMention(list, { id: `u${i}`, label: `U${i}` });
    expect(list).toHaveLength(maxMentions);
  });
  it('removes and sends ids only for internal notes', () => {
    const list = [
      { id: 'a', label: 'A' },
      { id: 'b', label: 'B' },
    ];
    expect(removeMention(list, 'a')).toEqual([{ id: 'b', label: 'B' }]);
    expect(mentionIds(list, true)).toEqual(['a', 'b']);
    expect(mentionIds(list, false)).toEqual([]);
  });
});

describe('location history', () => {
  it('knows whether the location was ever corrected', () => {
    expect(locationFromProfile(undefined)).toBe(true);
    expect(locationFromProfile([entry({ kind: 'assigned' })])).toBe(true);
    expect(locationFromProfile([entry({})])).toBe(false);
  });
  it('collects distinct location ids', () => {
    const ids = historyLocationIds([
      entry({ fromLocationId: 'l1', toLocationId: 'l2' }),
      entry({ fromLocationId: 'l2', toLocationId: null }),
      entry({ kind: 'assigned', toLocationId: 'x' }),
    ]);
    expect(ids).toEqual(['l1', 'l2']);
  });
});

describe('changeCandidates', () => {
  const changes = [
    { id: '1', reference: 'CHG-1', title: 'Switch update', status: 'approved' as const },
    { id: '2', reference: 'CHG-2', title: 'Mail relay', status: 'draft' as const },
  ];
  it('filters by text and hides linked changes', () => {
    expect(changeCandidates(changes, 'mail', [])).toHaveLength(1);
    expect(changeCandidates(changes, 'chg-', ['1']).map((c) => c.id)).toEqual(['2']);
    expect(changeCandidates(changes, '', [])).toHaveLength(2);
  });
});
