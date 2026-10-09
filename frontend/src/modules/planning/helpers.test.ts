import { describe, expect, it } from 'vitest';
import {
  availableActions,
  calendarLegend,
  calendarRange,
  groupCalendarByDay,
  isProposedWindow,
  itemPath,
} from './helpers';
import type { InitiativeDetail, Item } from './types';
describe('planning helpers', () => {
  it('uses backend operations and never links hidden records', () => {
    expect(availableActions({ allowedOperations: ['propose'] } as InitiativeDetail)).toEqual([
      'propose',
    ]);
    expect(itemPath({ type: 'change', id: 'abc', hidden: true } as Item)).toBeNull();
    expect(itemPath({ type: 'change', id: 'abc' } as Item)).toBe('/changes/abc');
  });
  it('groups calendar entries and bounds week and month ranges', () => {
    expect(
      Object.keys(groupCalendarByDay([{ windowStart: '2026-10-04T10:00:00Z' }] as never)),
    ).toEqual(['2026-10-04']);
    expect(calendarRange('2026-10-04', 'week')).toEqual({
      from: new Date(2026, 8, 28).toISOString(),
      to: new Date(2026, 9, 5).toISOString(),
    });
    expect(calendarRange('2026-10-04', 'month')).toEqual({
      from: new Date(2026, 9, 1).toISOString(),
      to: new Date(2026, 10, 1).toISOString(),
    });
  });
  it('marks submitted but unapproved windows as proposed', () => {
    expect(isProposedWindow({ proposed: true, status: 'approved' })).toBe(true);
    expect(isProposedWindow({ proposed: false, status: 'assessment' })).toBe(false);
    expect(isProposedWindow({ status: 'pending_approval' })).toBe(true);
    expect(isProposedWindow({ status: 'scheduled' })).toBe(false);
  });
  it('lists only the legend entries that appear', () => {
    expect(calendarLegend([])).toEqual({ proposed: false, firm: false });
    expect(calendarLegend([{ status: 'assessment' }, { status: 'scheduled' }])).toEqual({
      proposed: true,
      firm: true,
    });
    expect(calendarLegend([{ proposed: true, status: 'assessment' }])).toEqual({
      proposed: true,
      firm: false,
    });
  });
});
