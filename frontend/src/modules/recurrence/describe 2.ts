import type { MessageKey } from '../../platform/i18n/i18n';
import type { RecurrenceRule } from './types';

type Translate = (key: MessageKey, params?: Record<string, string | number>) => string;

const weekdayKeys = [
  'recurrence.weekday.1',
  'recurrence.weekday.2',
  'recurrence.weekday.3',
  'recurrence.weekday.4',
  'recurrence.weekday.5',
  'recurrence.weekday.6',
  'recurrence.weekday.7',
] as const;

/** One-line, localized description of when a rule runs, for the list and detail views. */
export function describeRule(t: Translate, rule: RecurrenceRule): string {
  const time = `${rule.timeOfDay} (${rule.timezone})`;
  switch (rule.frequency) {
    case 'daily':
      return rule.interval === 1
        ? t('recurrence.describe.daily', { time })
        : t('recurrence.describe.dailyEvery', { interval: rule.interval, time });
    case 'weekly': {
      const key = weekdayKeys[(rule.weekday ?? 1) - 1] ?? weekdayKeys[0];
      const weekday = t(key);
      return rule.interval === 1
        ? t('recurrence.describe.weekly', { weekday, time })
        : t('recurrence.describe.weeklyEvery', { interval: rule.interval, weekday, time });
    }
    case 'monthly': {
      const day = rule.dayOfMonth ?? 1;
      return rule.interval === 1
        ? t('recurrence.describe.monthly', { day, time })
        : t('recurrence.describe.monthlyEvery', { interval: rule.interval, day, time });
    }
  }
}
