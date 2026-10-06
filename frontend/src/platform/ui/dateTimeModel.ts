/**
 * Local date-time values use the `YYYY-MM-DDTHH:mm` shape of `datetime-local`, so existing
 * form state and `new Date(value)` conversion keep working unchanged.
 */
export type LocalParts = { year: number; month: number; day: number; hour: number; minute: number };
export type CalendarDay = { year: number; month: number; day: number; inMonth: boolean };

const pad = (value: number) => String(value).padStart(2, '0');

export function parseLocal(value: string): LocalParts | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})/.exec(value);
  if (!match) return null;
  const [year, month, day, hour, minute] = match.slice(1).map(Number) as [
    number,
    number,
    number,
    number,
    number,
  ];
  const probe = new Date(year, month - 1, day, hour, minute);
  if (probe.getMonth() !== month - 1 || probe.getDate() !== day || hour > 23 || minute > 59)
    return null;
  return { year, month, day, hour, minute };
}

export function toLocalValue({ year, month, day, hour, minute }: LocalParts): string {
  return `${year}-${pad(month)}-${pad(day)}T${pad(hour)}:${pad(minute)}`;
}

/** Sunday-first for English, Monday-first otherwise (ISO 8601 weeks). */
export function weekStartFor(locale: string): 0 | 1 {
  return locale.toLowerCase().startsWith('en') ? 0 : 1;
}

/** Six full weeks around the month, so the grid height never jumps. Month is 1-based. */
export function calendarWeeks(year: number, month: number, weekStart: 0 | 1): CalendarDay[][] {
  const first = new Date(year, month - 1, 1);
  const offset = (first.getDay() - weekStart + 7) % 7;
  const weeks: CalendarDay[][] = [];
  for (let week = 0; week < 6; week += 1) {
    const days: CalendarDay[] = [];
    for (let weekday = 0; weekday < 7; weekday += 1) {
      const date = new Date(year, month - 1, 1 - offset + week * 7 + weekday);
      days.push({
        year: date.getFullYear(),
        month: date.getMonth() + 1,
        day: date.getDate(),
        inMonth: date.getMonth() === month - 1,
      });
    }
    weeks.push(days);
  }
  return weeks;
}

/** Minute options in quarter hours, keeping an existing off-grid minute selectable. */
export function minuteOptions(current?: number): number[] {
  const base = [0, 15, 30, 45];
  return current === undefined || base.includes(current)
    ? base
    : [...base, current].sort((a, b) => a - b);
}
