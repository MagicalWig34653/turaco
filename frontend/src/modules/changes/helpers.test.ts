import { describe, expect, it } from 'vitest';
import {
  allowedActions,
  issuesByStep,
  maxAffected,
  mergeMissing,
  missingForSubmit,
  readinessFromIssues,
  requiredForSubmit,
  toggleCandidate,
  resourceLabel,
  changeMetrics,
  matchesChangeMetric,
  windowMinutes,
} from './helpers';
import type { AffectedCandidate, Change } from './types';
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

describe('change submit readiness', () => {
  const ready = { kind: 'normal', risk: 'low', windowStart: null, rollbackPlan: null } as const;
  it('lists what a normal change lacks, in the server order', () => {
    expect(missingForSubmit({ ...ready, risk: 'high' }, 0)).toEqual([
      'windowStart',
      'rollbackPlan',
      'affectedResources',
    ]);
    expect(missingForSubmit({ ...ready, windowStart: '2026-10-10T10:00:00Z' }, 1)).toEqual([]);
  });
  it('needs no affected resource for standard changes and no plan for low risk', () => {
    expect(requiredForSubmit({ kind: 'standard', risk: 'low' })).toEqual(['windowStart']);
    expect(missingForSubmit({ ...ready, kind: 'standard', rollbackPlan: ' ' }, 0)).toEqual([
      'windowStart',
    ]);
    expect(
      missingForSubmit({ ...ready, risk: 'medium', windowStart: 'x', rollbackPlan: '  ' }, 2),
    ).toEqual(['rollbackPlan']);
  });
  it('reads field-level 400 details and ignores unknown fields', () => {
    const issues = [
      { field: 'affectedResources', code: 'required' },
      { field: 'windowStart', code: 'required' },
      { field: 'somethingNew', code: 'required' },
      { field: 'rollbackPlan', code: 'too_long' },
    ];
    expect(readinessFromIssues(issues)).toEqual(['windowStart', 'affectedResources']);
    expect(issuesByStep(issues)).toEqual({
      basics: [],
      planning: ['windowStart'],
      resources: ['affectedResources'],
    });
    expect(mergeMissing(['affectedResources'], ['windowStart'])).toEqual([
      'windowStart',
      'affectedResources',
    ]);
  });
});

describe('affected resource selection', () => {
  const svc: AffectedCandidate = { type: 'service', id: 'a', name: 'Mail', reference: 'SVC-1' };
  const vm: AffectedCandidate = { type: 'vm', id: 'a', name: 'mail-01' };
  it('keeps items of different types with the same id apart and toggles off', () => {
    const both = toggleCandidate(toggleCandidate([], svc), vm);
    expect(both).toHaveLength(2);
    expect(toggleCandidate(both, svc)).toEqual([vm]);
  });
  it('stops adding at the resource limit, counting already linked ones', () => {
    expect(toggleCandidate([], svc, maxAffected)).toEqual([]);
    expect(toggleCandidate([svc], vm, maxAffected - 1)).toEqual([svc]);
    expect(toggleCandidate([svc], vm, maxAffected - 2)).toEqual([svc, vm]);
  });
});
