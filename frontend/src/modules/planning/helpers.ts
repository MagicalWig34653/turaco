import type { CalendarEntry, Item, InitiativeDetail } from './types';
export function availableActions(detail: InitiativeDetail): string[] {
  return detail.allowedOperations ?? [];
}
export function itemPath(item: Item): string | null {
  if (item.hidden || item.missing) return null;
  const base: Record<string, string> = {
    change: '/changes',
    task: '/tasks',
    service: '/services',
  };
  return base[item.type] ? `${base[item.type]}/${encodeURIComponent(item.id)}` : null;
}
export function localDate(date: Date): string {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
}
export function groupCalendarByDay(items: CalendarEntry[]): Record<string, CalendarEntry[]> {
  const days: Record<string, CalendarEntry[]> = {};
  for (const item of items) {
    const day = localDate(new Date(item.windowStart));
    (days[day] ??= []).push(item);
  }
  return days;
}
export function calendarRange(
  anchor: string,
  view: 'week' | 'month',
): { from: string; to: string } {
  const parsed = new Date(`${anchor}T00:00:00`);
  const date = Number.isNaN(parsed.getTime()) ? new Date() : parsed;
  if (view === 'week') {
    date.setDate(date.getDate() - ((date.getDay() + 6) % 7));
    const end = new Date(date);
    end.setDate(end.getDate() + 7);
    return { from: date.toISOString(), to: end.toISOString() };
  }
  const from = new Date(date.getFullYear(), date.getMonth(), 1);
  const to = new Date(date.getFullYear(), date.getMonth() + 1, 1);
  return { from: from.toISOString(), to: to.toISOString() };
}
