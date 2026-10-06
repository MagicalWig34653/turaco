import { describe, expect, it } from 'vitest';
import {
  calendarWeeks,
  minuteOptions,
  parseLocal,
  toLocalValue,
  weekStartFor,
} from './dateTimeModel';

describe('date-time model', () => {
  it('round-trips datetime-local values', () => {
    const parts = parseLocal('2026-10-06T09:05');
    expect(parts).toEqual({ year: 2026, month: 10, day: 6, hour: 9, minute: 5 });
    expect(toLocalValue(parts!)).toBe('2026-10-06T09:05');
  });
  it('rejects impossible or empty values', () => {
    expect(parseLocal('')).toBeNull();
    expect(parseLocal('2026-02-30T10:00')).toBeNull();
    expect(parseLocal('2026-10-06T24:00')).toBeNull();
  });
  it('starts weeks on Sunday in English and Monday in German', () => {
    expect(weekStartFor('en')).toBe(0);
    expect(weekStartFor('de')).toBe(1);
    // 1 October 2026 is a Thursday.
    const sunday = calendarWeeks(2026, 10, 0);
    const monday = calendarWeeks(2026, 10, 1);
    expect(sunday[0]?.[4]).toEqual({ year: 2026, month: 10, day: 1, inMonth: true });
    expect(monday[0]?.[3]).toEqual({ year: 2026, month: 10, day: 1, inMonth: true });
    expect(monday[0]?.[0]).toEqual({ year: 2026, month: 9, day: 28, inMonth: false });
    expect(sunday).toHaveLength(6);
    expect(sunday.every((week) => week.length === 7)).toBe(true);
  });
  it('offers quarter hours and keeps an existing minute', () => {
    expect(minuteOptions()).toEqual([0, 15, 30, 45]);
    expect(minuteOptions(5)).toEqual([0, 5, 15, 30, 45]);
  });
});
