import { describe, expect, it } from 'vitest';
import {
  defaultRuleForm,
  formToRule,
  isRuleError,
  localToday,
  parseDueAfterHours,
  ruleToForm,
} from './rule';

const form = defaultRuleForm(new Date(2026, 9, 2, 12, 0), 'Europe/Berlin');

describe('localToday', () => {
  it('uses the local calendar date', () => {
    expect(localToday(new Date(2026, 0, 5, 23, 59))).toBe('2026-01-05');
    expect(localToday(new Date(2026, 11, 31, 0, 0))).toBe('2026-12-31');
  });
});

describe('formToRule', () => {
  it('builds a weekly rule with only the weekday', () => {
    const rule = formToRule({ ...form, frequency: 'weekly', weekday: '5' });
    expect(rule).toEqual({
      frequency: 'weekly',
      interval: 1,
      weekday: 5,
      dayOfMonth: null,
      timeOfDay: '09:00',
      timezone: 'Europe/Berlin',
      startsOn: '2026-10-02',
    });
  });

  it('builds monthly and daily rules with the matching fields only', () => {
    const monthly = formToRule({ ...form, frequency: 'monthly', dayOfMonth: '31' });
    expect(isRuleError(monthly)).toBe(false);
    expect(monthly).toMatchObject({ weekday: null, dayOfMonth: 31 });
    const daily = formToRule({ ...form, frequency: 'daily', interval: '3' });
    expect(daily).toMatchObject({ interval: 3, weekday: null, dayOfMonth: null });
  });

  it('reports the first invalid field', () => {
    expect(formToRule({ ...form, interval: '0' })).toBe('interval');
    expect(formToRule({ ...form, interval: '366' })).toBe('interval');
    expect(formToRule({ ...form, interval: '1.5' })).toBe('interval');
    expect(formToRule({ ...form, interval: 'abc' })).toBe('interval');
    expect(formToRule({ ...form, timeOfDay: '24:00' })).toBe('time');
    expect(formToRule({ ...form, timeOfDay: '9:00' })).toBe('time');
    expect(formToRule({ ...form, timezone: '  ' })).toBe('timezone');
    expect(formToRule({ ...form, startsOn: '02.10.2026' })).toBe('startsOn');
  });
});

describe('ruleToForm', () => {
  it('round-trips through formToRule', () => {
    const rule = formToRule({ ...form, frequency: 'monthly', dayOfMonth: '15', interval: '3' });
    expect(isRuleError(rule)).toBe(false);
    if (!isRuleError(rule)) expect(formToRule(ruleToForm(rule))).toEqual(rule);
  });
});

describe('parseDueAfterHours', () => {
  it('accepts empty as none and whole hours in range', () => {
    expect(parseDueAfterHours('')).toBeNull();
    expect(parseDueAfterHours(' 48 ')).toBe(48);
    expect(parseDueAfterHours('8760')).toBe(8760);
  });
  it('rejects everything else', () => {
    for (const value of ['0', '8761', '-1', '1.5', 'x', '1e3']) {
      expect(parseDueAfterHours(value), value).toBe('invalid');
    }
  });
});
