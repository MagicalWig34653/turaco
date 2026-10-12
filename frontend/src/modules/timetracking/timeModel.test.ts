import { describe, expect, it } from 'vitest';
import {
  addDays,
  dayTotals,
  formatDuration,
  formProblem,
  localDate,
  outsideWindow,
  parseDuration,
  weekOf,
  workDateRange,
} from './timeModel';
import type { TimeEntry } from './types';

describe('durations', () => {
  it('formats minutes as hours and minutes', () => {
    expect(formatDuration(0)).toBe('0:00');
    expect(formatDuration(5)).toBe('0:05');
    expect(formatDuration(90)).toBe('1:30');
    expect(formatDuration(1440)).toBe('24:00');
  });

  it('parses what people type', () => {
    const cases: Record<string, number> = {
      '90': 90,
      '90m': 90,
      '45 min': 45,
      '1:30': 90,
      '01:05': 65,
      '1h': 60,
      '1h30': 90,
      '1 h 30 m': 90,
      '1.5h': 90,
      '1,5 h': 90,
      '24h': 1440,
    };
    for (const [text, minutes] of Object.entries(cases)) {
      expect(parseDuration(text), text).toBe(minutes);
    }
  });

  it('rejects empty, zero, too long and malformed durations', () => {
    for (const text of ['', ' ', '0', '0:00', '1441', '25h', '1:75', 'abc', '-5', '1h 99x']) {
      expect(parseDuration(text), text).toBeUndefined();
    }
  });
});

describe('dates', () => {
  it('computes local dates and day arithmetic', () => {
    expect(localDate(new Date(2026, 9, 5, 23, 30))).toBe('2026-10-05');
    expect(addDays('2026-10-31', 1)).toBe('2026-11-01');
    expect(addDays('2026-03-01', -1)).toBe('2026-02-28');
  });

  it('builds Monday-to-Sunday weeks', () => {
    const week = weekOf('2026-10-11'); // a Sunday
    expect(week.from).toBe('2026-10-05');
    expect(week.to).toBe('2026-10-11');
    expect(week.days).toHaveLength(7);
    expect(weekOf('2026-10-05').from).toBe('2026-10-05');
  });

  it('knows the edit window', () => {
    expect(workDateRange('2026-10-12', 14)).toEqual({ min: '2026-09-28', max: '2026-10-12' });
    expect(outsideWindow('2026-09-28', '2026-10-12', 14)).toBe(false);
    expect(outsideWindow('2026-09-27', '2026-10-12', 14)).toBe(true);
  });
});

describe('form validation', () => {
  const base = {
    duration: '1:00',
    workDate: '2026-10-12',
    today: '2026-10-12',
    editWindowDays: 14,
    canManage: false,
    reason: '',
  };

  it('accepts a normal entry', () => {
    expect(formProblem(base)).toBeUndefined();
  });

  it('finds the first problem', () => {
    expect(formProblem({ ...base, duration: 'x' })).toBe('duration');
    expect(formProblem({ ...base, workDate: '' })).toBe('date');
    expect(formProblem({ ...base, workDate: '2026-10-20' })).toBe('date');
    expect(formProblem({ ...base, workDate: '2026-09-01' })).toBe('date');
    expect(formProblem({ ...base, workDate: '2026-09-01', canManage: true })).toBe('reason');
    expect(
      formProblem({ ...base, workDate: '2026-09-01', canManage: true, reason: 'forgotten' }),
    ).toBeUndefined();
    expect(formProblem({ ...base, needsReason: true })).toBe('reason');
    expect(formProblem({ ...base, duration: '2h', loggedThatDay: 23 * 60 })).toBe('dayLimit');
  });
});

describe('totals', () => {
  it('sums minutes per work date', () => {
    const entry = (workDate: string, durationMinutes: number) =>
      ({ workDate, durationMinutes }) as TimeEntry;
    expect(
      dayTotals([entry('2026-10-12', 30), entry('2026-10-12', 15), entry('2026-10-11', 5)]),
    ).toEqual({
      '2026-10-12': 45,
      '2026-10-11': 5,
    });
  });
});
