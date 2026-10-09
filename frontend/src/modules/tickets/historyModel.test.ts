import { describe, expect, it } from 'vitest';
import { describeHistory, isAssignmentEntry, mergeTimeline, viaKey } from './historyModel';
import type { TicketComment, TicketHistoryEntry } from './types';

const comment = (id: string, createdAt: string): TicketComment => ({
  id,
  authorId: 'u',
  body: id,
  internal: false,
  createdAt,
});
const entry = (id: string, at: string, kind: string, extra = {}): TicketHistoryEntry => ({
  id,
  at,
  kind,
  ...extra,
});

describe('mergeTimeline', () => {
  it('orders comments and changes chronologically and drops the created entry', () => {
    const items = mergeTimeline(
      [comment('c1', '2026-10-01T10:05:00Z'), comment('c2', '2026-10-01T10:20:00Z')],
      [
        entry('h0', '2026-10-01T10:00:00Z', 'created'),
        entry('h1', '2026-10-01T10:10:00Z', 'assigned'),
      ],
    );
    expect(
      items.map((item) => (item.type === 'comment' ? item.comment.id : item.entry.id)),
    ).toEqual(['c1', 'h1', 'c2']);
  });
  it('tolerates absent history and unparsable times', () => {
    expect(mergeTimeline([comment('c1', 'bad')], undefined)).toHaveLength(1);
    expect(mergeTimeline([], [])).toEqual([]);
  });
  it('keeps history first at equal times so a change precedes the reply it explains', () => {
    const items = mergeTimeline(
      [comment('c1', '2026-10-01T10:00:00Z')],
      [entry('h1', '2026-10-01T10:00:00Z', 'assigned')],
    );
    expect(items[0]?.type).toBe('history');
  });
});

describe('describeHistory', () => {
  const name = (id: string | null | undefined) => (id ? `n:${id}` : '-');
  const label = (kind: string, value: string) => `${kind}:${value}`;
  it('names the recipient of an assignment', () => {
    expect(describeHistory(entry('1', 'x', 'assigned', { toUserId: 'a' }), name, label)).toEqual({
      key: 'ticketHistory.assigned',
      params: { to: 'n:a' },
    });
  });
  it('names both sides of a reassignment and the enumerations of a status change', () => {
    expect(
      describeHistory(
        entry('1', 'x', 'reassigned', { fromUserId: 'a', toUserId: 'b' }),
        name,
        label,
      ).params,
    ).toEqual({ from: 'n:a', to: 'n:b' });
    expect(
      describeHistory(
        entry('1', 'x', 'status_changed', { fromStatus: 'new', toStatus: 'open' }),
        name,
        label,
      ).params,
    ).toEqual({ from: 'status:new', to: 'status:open' });
  });
  it('falls back for kinds a newer server may add', () => {
    expect(describeHistory(entry('1', 'x', 'future_kind'), name, label).key).toBe(
      'ticketHistory.other',
    );
  });
  it('highlights assignment entries and explains automatic ones', () => {
    expect(isAssignmentEntry({ kind: 'team_routed' })).toBe(true);
    expect(isAssignmentEntry({ kind: 'status_changed' })).toBe(false);
    expect(viaKey('start')).toBe('ticketHistory.via.start');
    expect(viaKey('assign')).toBeUndefined();
  });
});
