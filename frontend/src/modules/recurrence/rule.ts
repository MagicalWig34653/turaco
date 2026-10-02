import type { Frequency, RecurrenceRule } from './types';

export type RuleForm = {
  frequency: Frequency;
  interval: string;
  weekday: string;
  dayOfMonth: string;
  timeOfDay: string;
  timezone: string;
  startsOn: string;
};

/** Local calendar date YYYY-MM-DD of `now` (not UTC): the schedule's start date is a local date. */
export function localToday(now: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
}

export function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

export function defaultRuleForm(now: Date, timezone: string): RuleForm {
  return {
    frequency: 'weekly',
    interval: '1',
    weekday: '1',
    dayOfMonth: '1',
    timeOfDay: '09:00',
    timezone,
    startsOn: localToday(now),
  };
}

export function ruleToForm(rule: RecurrenceRule): RuleForm {
  return {
    frequency: rule.frequency,
    interval: String(rule.interval),
    weekday: String(rule.weekday ?? 1),
    dayOfMonth: String(rule.dayOfMonth ?? 1),
    timeOfDay: rule.timeOfDay,
    timezone: rule.timezone,
    startsOn: rule.startsOn,
  };
}

export type RuleFormError = 'interval' | 'time' | 'timezone' | 'startsOn';

/** Builds the API rule from the form; the server validates again and stays authoritative. */
export function formToRule(form: RuleForm): RecurrenceRule | RuleFormError {
  const interval = Number.parseInt(form.interval, 10);
  if (
    !Number.isInteger(interval) ||
    interval < 1 ||
    interval > 365 ||
    String(interval) !== form.interval.trim()
  ) {
    return 'interval';
  }
  if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(form.timeOfDay)) return 'time';
  if (form.timezone.trim() === '') return 'timezone';
  if (!/^\d{4}-\d{2}-\d{2}$/.test(form.startsOn)) return 'startsOn';
  return {
    frequency: form.frequency,
    interval,
    weekday: form.frequency === 'weekly' ? Number.parseInt(form.weekday, 10) : null,
    dayOfMonth: form.frequency === 'monthly' ? Number.parseInt(form.dayOfMonth, 10) : null,
    timeOfDay: form.timeOfDay,
    timezone: form.timezone.trim(),
    startsOn: form.startsOn,
  };
}

/** Whole hours from the form field; empty means no due offset, anything else invalid is an error. */
export function parseDueAfterHours(value: string): number | null | 'invalid' {
  const trimmed = value.trim();
  if (trimmed === '') return null;
  const hours = Number.parseInt(trimmed, 10);
  if (!/^\d+$/.test(trimmed) || hours < 1 || hours > 8760) return 'invalid';
  return hours;
}

export function isRuleError(value: RecurrenceRule | RuleFormError): value is RuleFormError {
  return typeof value === 'string';
}
