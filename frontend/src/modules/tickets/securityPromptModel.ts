import { toLocalValue } from '../../platform/ui/dateTimeModel';
import type { TicketDetail } from './types';

/** The Security Queue is the only current security-ticket signal. A Task is always proposed, never created here. */
export function shouldSuggestDataProtectionTask(
  ticket: TicketDetail,
  tasks: { title: string }[] | undefined,
  knownTitles: readonly string[],
): boolean {
  return (
    ticket.queue?.key === 'security' &&
    !!ticket.abilities?.scheduleCallback &&
    !!tasks &&
    !tasks.some((task) => knownTitles.includes(task.title))
  );
}

/** Creation time is a planning hint, not a legal determination of when a notification period began. */
export function dataProtectionDueValue(createdAt: string, now = Date.now()): string {
  const due = new Date(new Date(createdAt).getTime() + 72 * 60 * 60 * 1000);
  if (!Number.isFinite(due.getTime()) || due.getTime() <= now + 60_000) return '';
  return toLocalValue({
    year: due.getFullYear(),
    month: due.getMonth() + 1,
    day: due.getDate(),
    hour: due.getHours(),
    minute: due.getMinutes(),
  });
}
