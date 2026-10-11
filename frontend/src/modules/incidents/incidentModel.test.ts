import { describe, expect, it } from 'vitest';
import { incidentFreshness, incidentLocationNames, nextUpdateState } from './incidentModel';

it('shows named affected locations and keeps an empty set explicit', () => {
  expect(incidentLocationNames(undefined, 0, 'Unavailable')).toBeNull();
  expect(incidentLocationNames([], 0, 'Unavailable')).toBeNull();
  expect(incidentLocationNames([], 1, 'Unavailable')).toBe('Unavailable');
  expect(incidentLocationNames([{ name: 'Main Campus' }], 2, 'Unavailable')).toBe(
    'Main Campus, Unavailable',
  );
  expect(
    incidentLocationNames([{ name: 'Main Campus' }, { name: 'North Wing' }], 2, 'Unavailable'),
  ).toBe('Main Campus, North Wing');
});

describe('nextUpdateState', () => {
  const now = new Date('2026-10-10T12:00:00Z');
  it('classifies the promised update time', () => {
    expect(nextUpdateState(null, now)).toBe('none');
    expect(nextUpdateState('garbage', now)).toBe('none');
    expect(nextUpdateState('2026-10-10T13:00:00Z', now)).toBe('upcoming');
    expect(nextUpdateState('2026-10-10T11:00:00Z', now)).toBe('overdue');
  });
});

describe('incidentFreshness', () => {
  const now = new Date('2026-10-11T12:00:00Z');
  it('flags an overdue promise, shows a planned one and ages an unplanned incident', () => {
    expect(
      incidentFreshness(
        { nextUpdateDue: '2026-10-11T11:00:00Z', updatedAt: '2026-10-09T10:00:00Z' },
        now,
      ).kind,
    ).toBe('overdue');
    expect(
      incidentFreshness(
        { nextUpdateDue: '2026-10-11T13:00:00Z', updatedAt: '2026-10-09T10:00:00Z' },
        now,
      ).kind,
    ).toBe('planned');
    expect(
      incidentFreshness({ nextUpdateDue: null, updatedAt: '2026-10-09T12:00:00Z' }, now),
    ).toEqual({ kind: 'none', ageHours: 48 });
  });
});
