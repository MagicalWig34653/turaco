import type { TimeEntry } from './types';

/** Largest duration of one entry and of one person per work date (minutes). */
export const maxMinutes = 24 * 60;

/** 90 becomes "1:30". Durations are shown as hours and minutes without a locale. */
export function formatDuration(minutes: number): string {
  const safe = Math.max(0, Math.round(minutes));
  const hours = Math.floor(safe / 60);
  const rest = safe % 60;
  return `${hours}:${String(rest).padStart(2, '0')}`;
}

/**
 * Parses what people type: "90", "90m", "45 min", "1:30", "1h", "1h30", "1 h 30 m", "1.5h", "1,5 h".
 * Returns whole minutes from 1 to 1440, or undefined when the text is not a valid duration.
 */
export function parseDuration(input: string): number | undefined {
  const text = input.trim().toLowerCase();
  let minutes: number | undefined;
  let match: RegExpMatchArray | null;
  if ((match = text.match(/^(\d{1,2}):([0-5]\d)$/))) {
    minutes = Number(match[1]) * 60 + Number(match[2]);
  } else if ((match = text.match(/^(\d+)\s*h(?:\s*(\d{1,2})\s*(?:m|min)?)?$/))) {
    minutes = Number(match[1]) * 60 + Number(match[2] ?? 0);
  } else if ((match = text.match(/^(\d+(?:[.,]\d+)?)\s*h$/))) {
    minutes = Math.round(Number(match[1]!.replace(',', '.')) * 60);
  } else if ((match = text.match(/^(\d+)\s*(?:m|min)?$/))) {
    minutes = Number(match[1]);
  }
  if (minutes === undefined || !Number.isFinite(minutes) || minutes < 1 || minutes > maxMinutes) {
    return undefined;
  }
  return minutes;
}

/** The local calendar date of an instant as YYYY-MM-DD. */
export function localDate(now: Date): string {
  const month = String(now.getMonth() + 1).padStart(2, '0');
  const day = String(now.getDate()).padStart(2, '0');
  return `${now.getFullYear()}-${month}-${day}`;
}

function parse(date: string): Date {
  const [y, m, d] = date.split('-').map(Number);
  return new Date(Date.UTC(y!, m! - 1, d!));
}

function format(date: Date): string {
  return date.toISOString().slice(0, 10);
}

/** Adds whole days to a YYYY-MM-DD date. */
export function addDays(date: string, days: number): string {
  const result = parse(date);
  result.setUTCDate(result.getUTCDate() + days);
  return format(result);
}

/** The Monday-to-Sunday week that contains the date. */
export function weekOf(date: string): { from: string; to: string; days: string[] } {
  const day = parse(date).getUTCDay(); // 0 = Sunday
  const from = addDays(date, -((day + 6) % 7));
  return {
    from,
    to: addDays(from, 6),
    days: Array.from({ length: 7 }, (_, i) => addDays(from, i)),
  };
}

/** The work date range an own entry may use: from `today - window` to `today`. */
export function workDateRange(today: string, editWindowDays: number): { min: string; max: string } {
  return { min: addDays(today, -editWindowDays), max: today };
}

/** True when the work date is older than the edit window (a reason and timetracking.manage are needed). */
export function outsideWindow(workDate: string, today: string, editWindowDays: number): boolean {
  return workDate < addDays(today, -editWindowDays);
}

/** Minutes per work date. */
export function dayTotals(entries: readonly TimeEntry[]): Record<string, number> {
  const totals: Record<string, number> = {};
  for (const entry of entries) {
    totals[entry.workDate] = (totals[entry.workDate] ?? 0) + entry.durationMinutes;
  }
  return totals;
}

export type FormProblem = 'duration' | 'date' | 'reason' | 'dayLimit';

/** Validates a time form before it is sent; the server decides again. */
export function formProblem(input: {
  duration: string;
  workDate: string;
  today: string;
  editWindowDays: number;
  canManage: boolean;
  reason: string;
  /** Minutes already logged by the person on that work date, without the entry being edited. */
  loggedThatDay?: number;
  needsReason?: boolean;
}): FormProblem | undefined {
  const minutes = parseDuration(input.duration);
  if (minutes === undefined) return 'duration';
  if (!input.workDate || input.workDate > addDays(input.today, 1)) return 'date';
  const outside = outsideWindow(input.workDate, input.today, input.editWindowDays);
  if (outside && !input.canManage) return 'date';
  if ((outside || input.needsReason) && input.reason.trim().length < 3) return 'reason';
  if ((input.loggedThatDay ?? 0) + minutes > maxMinutes) return 'dayLimit';
  return undefined;
}
