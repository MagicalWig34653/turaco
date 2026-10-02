import { describe, expect, it } from 'vitest';
import { translate } from '../../platform/i18n/i18n';
import { describeRule } from './describe';
import type { RecurrenceRule } from './types';

const base: RecurrenceRule = {
  frequency: 'daily',
  interval: 1,
  weekday: null,
  dayOfMonth: null,
  timeOfDay: '09:00',
  timezone: 'Europe/Berlin',
  startsOn: '2026-01-01',
};
const en = (key: Parameters<typeof translate>[1], params?: Record<string, string | number>) =>
  translate('en', key, params);
const de = (key: Parameters<typeof translate>[1], params?: Record<string, string | number>) =>
  translate('de', key, params);

describe('describeRule', () => {
  it('describes each frequency with its interval, time and zone', () => {
    expect(describeRule(en, base)).toContain('09:00 (Europe/Berlin)');
    expect(describeRule(en, { ...base, interval: 3 })).toContain('3');
    expect(describeRule(en, { ...base, frequency: 'weekly', weekday: 5 })).toContain('Friday');
    expect(describeRule(de, { ...base, frequency: 'weekly', weekday: 5 })).toContain('Freitag');
    expect(
      describeRule(en, { ...base, frequency: 'monthly', dayOfMonth: 31, interval: 2 }),
    ).toContain('31');
  });

  it('never leaves unreplaced placeholders', () => {
    for (const rule of [
      base,
      { ...base, interval: 2 },
      { ...base, frequency: 'weekly' as const, weekday: 7 },
      { ...base, frequency: 'weekly' as const, weekday: 1, interval: 2 },
      { ...base, frequency: 'monthly' as const, dayOfMonth: 1 },
      { ...base, frequency: 'monthly' as const, dayOfMonth: 1, interval: 2 },
    ]) {
      expect(describeRule(en, rule)).not.toMatch(/\{\w+\}/);
      expect(describeRule(de, rule)).not.toMatch(/\{\w+\}/);
    }
  });
});
