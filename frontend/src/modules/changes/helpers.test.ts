import { describe, expect, it } from 'vitest';
import {
  allowedActions,
  resourceLabel,
  changeMetrics,
  matchesChangeMetric,
  windowMinutes,
} from './helpers';
import type { Change } from './types';
describe('change helpers', () => {
  it('uses only operations supplied by the detail response', () => {
    const change = { status: 'assessment', requesterId: 'requester' } as Change;
    expect(allowedActions(change)).toEqual([]);
    expect(allowedActions({ ...change, allowedOperations: ['cancel'] })).toEqual(['cancel']);
    expect(allowedActions({ ...change, status: 'review', allowedOperations: [] })).toEqual([]);
  });
  it('never shows placeholder ids for hidden resources', () => {
    expect(
      resourceLabel(
        { type: 'asset', id: 'hidden-1', hidden: true },
        (type) => `Restricted ${type}`,
      ),
    ).toBe('Restricted asset');
  });
});

describe('loaded change overview', () => {
  it('uses the local calendar week, excluding its end boundary and unscheduled changes', () => {
    const now = new Date(2026, 9, 7, 12);
    const change = (status: Change['status'], windowStart: Date) =>
      ({ status, windowStart: windowStart.toISOString() }) as Change;
    expect(
      changeMetrics(
        [
          change('scheduled', new Date(2026, 9, 5)),
          change('scheduled', new Date(2026, 9, 12)),
          change('draft', new Date(2026, 9, 7)),
        ],
        now,
      ).scheduled,
    ).toBe(1);
  });
  it('counts approved, scheduled and running Changes whose window overlaps this week', () => {
    const now = new Date(2026, 9, 7, 12);
    const change = (status: Change['status'], start: Date, end?: Date) =>
      ({
        status,
        windowStart: start.toISOString(),
        windowEnd: end ? end.toISOString() : null,
      }) as Change;
    const changes = [
      change('approved', new Date(2026, 9, 8, 22)),
      change('scheduled', new Date(2026, 9, 9)),
      change('in_progress', new Date(2026, 9, 3), new Date(2026, 9, 6, 2)),
      change('in_progress', new Date(2026, 9, 2), new Date(2026, 9, 4)),
      change('pending_approval', new Date(2026, 9, 7)),
      change('completed', new Date(2026, 9, 6)),
      { status: 'approved', windowStart: null, windowEnd: null } as Change,
    ];
    expect(changeMetrics(changes, now).scheduled).toBe(3);
  });
  it('counts failures by completion time and never infers a failure from updatedAt', () => {
    const now = new Date('2026-10-07T12:00:00Z');
    const changes = [
      { status: 'failed', completedAt: '2026-10-01T12:00:00Z' },
      { status: 'failed', completedAt: null },
      { status: 'failed', completedAt: '2026-10-08T12:00:00Z' },
      { status: 'failed', completedAt: '2026-08-01T12:00:00Z' },
    ] as Change[];
    expect(changeMetrics(changes, now).failed).toBe(1);
    expect(changes.filter((change) => matchesChangeMetric(change, 'failed', now))).toHaveLength(1);
  });
  it('retains failure counts after review and close, with or without a completed rollback', () => {
    const now = new Date('2026-10-07T12:00:00Z');
    const changes = [
      { status: 'review', rollbackDone: true, completedAt: '2026-10-01T12:00:00Z' },
      { status: 'closed', rollbackDone: false, completedAt: '2026-10-01T12:00:00Z' },
      { status: 'closed', rollbackDone: null, completedAt: '2026-10-01T12:00:00Z' },
    ] as Change[];
    expect(changeMetrics(changes, now).failed).toBe(2);
  });
  it('omits invalid and reversed window durations', () => {
    expect(windowMinutes(null, null)).toBeNull();
    expect(windowMinutes('invalid', '2026-10-07')).toBeNull();
    expect(windowMinutes('2026-10-07T12:00Z', '2026-10-07T11:00Z')).toBeNull();
    expect(windowMinutes('2026-10-07T12:00Z', '2026-10-07T13:30Z')).toBe(90);
  });
});
